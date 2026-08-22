package visionv2

import (
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// ToolCall 一次工具调用（收齐后）。
type ToolCall struct {
	ID      string
	Name    string
	ImageID string
	Prompt  string
}

// StreamAccumulator 三格式工具调用解析器。按行 Feed，工具调用收齐后追加到 *calls。
type StreamAccumulator struct {
	format visionProxyFormat
	mu     sync.Mutex
	// chat 按 index；claude 按 content block index；responses 按 item_id
	byKey  map[string]*callAcc
	order  []string
	lastEv string // claude：最近的 event: 事件名
	// nonVision 当前轮出现非本插件工具（如 web_search）时为 true：
	// 混合轮或全非视觉轮，调用方应整体透传、不拦截。
	nonVision bool
}

// IsNonVision 报告当前累积轮是否混入/全为非本插件工具调用。
func (a *StreamAccumulator) IsNonVision() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.nonVision
}

// Reset 清空累积内部状态，允许后续行继续 Feed/透传。
func (a *StreamAccumulator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.byKey = map[string]*callAcc{}
	a.order = nil
	a.lastEv = ""
	a.nonVision = false
}

type callAcc struct {
	id     string
	name   string
	args   []byte
	closed bool
}

func NewStreamAccumulator(format visionProxyFormat) *StreamAccumulator {
	return &StreamAccumulator{format: format, byKey: map[string]*callAcc{}}
}

// Feed 处理一行 SSE；工具调用收齐后追加到 *calls。线程安全。
func (a *StreamAccumulator) Feed(line string, calls *[]ToolCall) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch a.format {
	case formatClaude:
		a.feedClaude(line, calls)
	case formatResponses:
		a.feedResponses(line, calls)
	default:
		a.feedChat(line, calls)
	}
}

// stripDataPrefix 去掉可选的 "data:" 前缀并去除首尾空白。
func stripDataPrefix(line string) string {
	s := strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(s, "data:"); ok {
		s = strings.TrimSpace(rest)
	}
	return s
}

// closeAndAppend 将累积参数解析为 ToolCall 并追加，标记已关闭防止重复。
// 非本插件工具不追加，置 nonVision 标记（整轮透传由调用方决策）；
// 一旦本轮已标记 nonVision，后续（含 vision 的）调用也不追加。
func (a *StreamAccumulator) closeAndAppend(ca *callAcc, calls *[]ToolCall) {
	ca.closed = true
	if a.nonVision {
		return
	}
	if ca.name != "" && ca.name != lookAtImageToolName {
		a.nonVision = true
		return
	}
	*calls = append(*calls, parseToolCall(ca))
}

// chat 格式。
type chatChunk struct {
	Choices []struct {
		Delta struct {
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

func (a *StreamAccumulator) feedChat(line string, calls *[]ToolCall) {
	s := stripDataPrefix(line)
	if s == "" || s == "[DONE]" {
		return
	}
	var ch chatChunk
	if err := json.Unmarshal([]byte(s), &ch); err != nil {
		return
	}
	for _, c := range ch.Choices {
		for _, tc := range c.Delta.ToolCalls {
			key := strconv.Itoa(tc.Index)
			ca, ok := a.byKey[key]
			if !ok {
				ca = &callAcc{}
				a.byKey[key] = ca
				a.order = append(a.order, key)
			}
			if tc.ID != "" {
				ca.id = tc.ID
			}
			if tc.Function.Name != "" {
				ca.name = tc.Function.Name
				if ca.name != lookAtImageToolName {
					a.nonVision = true // 尽早标记，工具增量行直接透传不吞
				}
			}
			ca.args = append(ca.args, tc.Function.Arguments...)
		}
		if c.FinishReason == "tool_calls" {
			for _, key := range a.order {
				if ca := a.byKey[key]; ca != nil && !ca.closed {
					a.closeAndAppend(ca, calls)
				}
			}
		}
	}
}

// claude 格式。
type claudeChunk struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

func (a *StreamAccumulator) feedClaude(line string, calls *[]ToolCall) {
	s := strings.TrimSpace(line)
	if s == "" {
		return
	}
	if rest, ok := strings.CutPrefix(s, "event:"); ok {
		a.lastEv = strings.TrimSpace(rest)
		return
	}
	if !strings.HasPrefix(s, "data:") {
		return
	}
	var ev claudeChunk
	if err := json.Unmarshal([]byte(stripDataPrefix(s)), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "content_block_start":
		if ev.ContentBlock.Type != "tool_use" {
			return
		}
		key := strconv.Itoa(ev.Index)
		a.byKey[key] = &callAcc{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		a.order = append(a.order, key)
		if ev.ContentBlock.Name != "" && ev.ContentBlock.Name != lookAtImageToolName {
			a.nonVision = true
		}
	case "content_block_delta":
		if ev.Delta.Type != "input_json_delta" {
			return
		}
		key := strconv.Itoa(ev.Index)
		if ca, ok := a.byKey[key]; ok {
			ca.args = append(ca.args, ev.Delta.PartialJSON...)
		}
	case "content_block_stop":
		key := strconv.Itoa(ev.Index)
		if ca, ok := a.byKey[key]; ok && !ca.closed {
			a.closeAndAppend(ca, calls)
		}
	case "message_stop":
		// 防御：流异常结束时强制收齐未关闭的调用。
		for _, key := range a.order {
			if ca := a.byKey[key]; ca != nil && !ca.closed {
				a.closeAndAppend(ca, calls)
			}
		}
	}
}

// responses 格式。
type responsesChunk struct {
	Type   string `json:"type"`
	ItemID string `json:"item_id"`
	Delta  string `json:"delta"`
	Item   *struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"item"`
}

func (a *StreamAccumulator) feedResponses(line string, calls *[]ToolCall) {
	s := stripDataPrefix(line)
	if s == "" {
		return
	}
	var ev responsesChunk
	if err := json.Unmarshal([]byte(s), &ev); err != nil {
		return
	}
	switch ev.Type {
	case "response.output_item.added":
		if ev.Item == nil || ev.Item.Type != "function_call" {
			return
		}
		key := ev.Item.ID
		if _, exists := a.byKey[key]; !exists {
			a.order = append(a.order, key)
		}
		a.byKey[key] = &callAcc{id: ev.Item.ID, name: ev.Item.Name, args: []byte(ev.Item.Arguments)}
		if ev.Item.Name != "" && ev.Item.Name != lookAtImageToolName {
			a.nonVision = true
		}
	case "response.function_call_arguments.delta":
		if ca, ok := a.byKey[ev.ItemID]; ok {
			ca.args = append(ca.args, ev.Delta...)
		}
	case "response.output_item.done":
		if ev.Item == nil || ev.Item.Type != "function_call" {
			return
		}
		key := ev.Item.ID
		if ca, ok := a.byKey[key]; ok && !ca.closed {
			ca.args = []byte(ev.Item.Arguments) // 完整参数覆盖增量
			a.closeAndAppend(ca, calls)
		}
	}
}

// parseToolCall 把累积的 arguments JSON 解析为 ToolCall 字段。
func parseToolCall(ca *callAcc) ToolCall {
	var args struct {
		ImageID string `json:"image_id"`
		Prompt  string `json:"prompt"`
	}
	json.Unmarshal(ca.args, &args)
	return ToolCall{ID: ca.id, Name: ca.name, ImageID: args.ImageID, Prompt: args.Prompt}
}

// PlaceholderFilter 从输出文本中剔除 <vision_img_...> 占位符（处理跨行拆分）。
type PlaceholderFilter struct{ buf string }

// Filter 处理一行，返回剔除占位符后的行。
func (f *PlaceholderFilter) Filter(line string) string {
	s := f.buf + line
	f.buf = ""
	for {
		start := strings.Index(s, "<vision_img_")
		if start < 0 {
			break
		}
		if end := strings.Index(s[start:], ">"); end >= 0 {
			s = s[:start] + s[start+end+1:] // 完整片段：删除
		} else {
			f.buf = s[start:] // 片段未闭合（可能跨行）：暂存
			s = s[:start]
			break
		}
	}
	return s
}
