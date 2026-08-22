package visionv2

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"loadout/core/config"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// maxToolRounds 工具循环最大轮数（每轮执行一次识别并续流一次，防止模型反复调工具死循环）。
const maxToolRounds = 5

// isVisionRequest 判断该流是否为本插件激活的视觉请求：
// 路径是视觉三格式之一 且 Metadata 标记 __vision_v2_active。读取时 nil 安全。
func (s *Service) isVisionRequest(pipe *modelgateway.ProxyPipeline) bool {
	if pipe == nil || pipe.Request == nil {
		return false
	}
	if _, ok := visionFormatByPath(pipe.Request.Path); !ok {
		return false
	}
	return pipe.Metadata != nil && pipe.Metadata["__vision_v2_active"] == true
}

// HandleProxyStreamChunk 流式 chunk 拦截（替换 service.go 空实现）：
//   - 非视觉请求直接透传（不建 state）；
//   - 透传 content（经占位符过滤）；
//   - 工具调用相关 chunk 置 nil（不转发）；
//   - 本轮混入/全为非本插件工具（web_search 等）→ 完全透传、不拦截；
//   - 流结束（chat: [DONE]；claude: message_stop；responses: response.completed 或 EOF）
//     且本轮流有工具调用 → 在此次 hook 调用内同步执行 executeToolLoop（主循环阻塞在此 hook 上）；
//   - 已进入接管（st.active）→ 全部置 nil。
func (s *Service) HandleProxyStreamChunk(payload any) (any, error) {
	sp, ok := payload.(*modelgateway.StreamChunkPayload)
	if !ok || sp == nil || sp.Data == nil || sp.Pipe == nil {
		return payload, nil
	}
	if !s.isVisionRequest(sp.Pipe) {
		return sp, nil // 非视觉请求：不建 state，直接透传
	}
	reqID := sp.Pipe.RequestID
	st := s.state(reqID)
	if st.active {
		return nil, nil
	}
	if st.passthrough {
		// 已进入透传：剩余流不拦截，流结束释放 state。
		if isStreamEnd(string(sp.Data), st.format) {
			s.dropState(reqID)
		}
		return sp, nil
	}
	// 首次 chunk 时按路径确定格式（state(id) 创建的空状态没有 format）。
	if st.format == formatUnknown {
		if f, ok := visionFormatByPath(sp.Pipe.Request.Path); ok {
			st.format = f
		}
	}
	if st.filter == nil {
		st.filter = &PlaceholderFilter{}
	}
	line := string(sp.Data)
	if cleaned := st.filter.Filter(line); cleaned != line {
		sp.Data = []byte(cleaned)
		line = cleaned
	}
	if st.acc == nil {
		st.acc = NewStreamAccumulator(st.format)
	}
	st.acc.Feed(line, &st.calls)
	if st.acc.IsNonVision() {
		// 模型调用了非本插件工具（混合或全为非视觉）：本轮完全透传，不拦截。
		// 注意 sp.Data 尚未被置 nil。
		st.passthrough = true
		st.acc.Reset()
		st.calls = nil
		st.pending = false
		return sp, nil
	}
	if len(st.calls) > 0 {
		st.pending = true
	}
	if st.pending {
		sp.Data = nil
		if isStreamEnd(line, st.format) {
			if _, err := s.executeToolLoop(sp.Pipe, st); err != nil {
				s.lg.Warn("vision_v2: 工具循环失败", "req", reqID, "err", err)
				writeSSEErrorChunk(sp.Pipe.ResponseWriter, err.Error(), st.format)
			}
			s.dropState(reqID)
		}
		return sp, nil
	}
	// 工具调用收齐前（pending 尚未置位）的增量 chunk 也要拦截：chat 的 tool_calls delta、
	// claude 的 tool_use / input_json_delta、responses 的 function_call item。
	if isToolChunkLine(line, st.format) {
		sp.Data = nil
	}
	// 无工具调用（普通文本流）流结束：释放 state，避免泄漏。
	if isStreamEnd(line, st.format) {
		s.dropState(reqID)
	}
	return sp, nil
}

// isStreamEnd 按格式判断流结束标记：
// chat: data: [DONE]；claude: event: message_stop；responses: data: {"type":"response.completed"}。
func isStreamEnd(line string, format visionProxyFormat) bool {
	t := strings.TrimSpace(line)
	switch format {
	case formatClaude:
		return strings.HasPrefix(t, "event: message_stop")
	case formatResponses:
		return strings.Contains(t, `"type":"response.completed"`)
	default:
		return strings.HasPrefix(t, "data:") && strings.TrimSpace(strings.TrimPrefix(t, "data:")) == "[DONE]"
	}
}

// isToolChunkLine 判断该行是否携带工具调用增量（流结束前就需拦截，避免客户端看到 tool_calls 字样）。
func isToolChunkLine(line string, format visionProxyFormat) bool {
	switch format {
	case formatClaude:
		return strings.Contains(line, `"type":"tool_use"`) || strings.Contains(line, "input_json_delta")
	case formatResponses:
		return strings.Contains(line, `"type":"function_call"`) || strings.Contains(line, "function_call_arguments")
	default: // formatChat
		return strings.Contains(line, "tool_calls")
	}
}

// writeSSEErrorChunk 按格式写错误块（工具循环失败时），前缀"视觉识别失败"。
func writeSSEErrorChunk(w http.ResponseWriter, msg string, format visionProxyFormat) {
	if w == nil {
		return
	}
	_, _ = fmt.Fprint(w, sseDelta(format, "视觉识别失败: "+msg))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// sseDelta 构造一个 reasoning/text delta 的 SSE 块：
// chat: choices[0].delta.reasoning_content；claude: content_block_delta/text_delta；
// responses: response.output_text.delta。
func sseDelta(format visionProxyFormat, text string) string {
	payload, _ := json.Marshal(text)
	switch format {
	case formatClaude:
		return fmt.Sprintf("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":%s}}\n\n", payload)
	case formatResponses:
		return fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"item_id\":\"vision_v2\",\"output_index\":0,\"delta\":%s}\n\n", payload)
	default: // formatChat
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"reasoning_content\":%s}}]}\n\n", payload)
	}
}

// toolStreamWriter 识别过程的流式输出 writer：首次调用先输出「图片理解：」前缀，
// 之后每个 delta 按当前响应格式写 SSE 块到客户端并 flush。
func (s *Service) toolStreamWriter(pipe *modelgateway.ProxyPipeline, format visionProxyFormat) func(string) error {
	first := true
	return func(delta string) error {
		w := pipe.ResponseWriter
		if w == nil {
			return nil
		}
		if first {
			first = false
			if _, err := fmt.Fprint(w, sseDelta(format, "图片理解：")); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprint(w, sseDelta(format, delta)); err != nil {
			return err
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		return nil
	}
}

// executeToolLoop 工具循环：执行工具 → 按格式构造消息 → 新请求（复用原渠道）→ 续流 → 检测新调用。
func (s *Service) executeToolLoop(pipe *modelgateway.ProxyPipeline, st *toolLoopState) (bool, error) {
	var bodyMap map[string]any
	dec := json.NewDecoder(bytes.NewReader(pipe.Request.Body))
	dec.UseNumber()
	if err := dec.Decode(&bodyMap); err != nil {
		return false, err
	}
	messages := proxyMessageArray(bodyMap, st.format)
	if len(messages) == 0 {
		return false, errors.New("vision_v2: 消息数组为空")
	}
	for st.round < maxToolRounds {
		calls := st.calls
		if len(calls) == 0 {
			return true, nil
		}
		// 1. 执行工具（识别）
		var toolResults []any
		handled := false
		for _, c := range calls {
			if c.Name != lookAtImageToolName {
				continue
			}
			handled = true
			route, _ := pipe.Metadata["__vision_v2_route"].(*types.CapabilityRoute)
			if route == nil {
				return false, errors.New("vision_v2: 缺少视觉路由")
			}
			ctx := context.Background()
			if pipe.HTTPRequest != nil && pipe.HTTPRequest.Context() != nil {
				ctx = pipe.HTTPRequest.Context()
			}
			text, err := s.describeWithFailover(ctx, c.ImageID, c.Prompt, s.toolStreamWriter(pipe, st.format), route, pipe.RequestID)
			if err != nil {
				return false, err
			}
			toolResults = append(toolResults, buildToolResultMessage(c, text, st.format))
		}
		if !handled {
			return true, nil // 本轮没有 look_at_image：不接管
		}
		// 2. 构造 assistant 工具消息 + 结果，追加。
		//    chat/claude：全部调用合并进一条 assistant 消息；
		//    responses：input 里每个 function_call 是独立 item，逐条追加。
		if st.format == formatResponses {
			for _, c := range calls {
				messages = append(messages, buildAssistantToolMessage([]ToolCall{c}, st.format))
			}
		} else {
			messages = append(messages, buildAssistantToolMessage(calls, st.format))
		}
		messages = append(messages, toolResults...)
		bodyMap[msgArrayKey(st.format)] = messages
		// 3. 新请求（复用原渠道，stream 保持原值）
		reqBody, err := json.Marshal(bodyMap)
		if err != nil {
			return false, err
		}
		resp, err := s.doUpstream(pipe, reqBody)
		if err != nil {
			return false, err
		}
		// 4. 续流：读新流逐行写客户端 + 检测新工具调用
		var newCalls []ToolCall
		acc := NewStreamAccumulator(st.format)
		flusher, _ := pipe.ResponseWriter.(http.Flusher)
		reader := bufio.NewReader(resp.Body)
		for {
			line, err := reader.ReadString('\n')
			if len(line) > 0 {
				if _, werr := fmt.Fprint(pipe.ResponseWriter, line); werr != nil {
					resp.Body.Close()
					return false, werr
				}
				if flusher != nil {
					flusher.Flush()
				}
				acc.Feed(line, &newCalls)
			}
			if isStreamEnd(line, st.format) || err != nil {
				break
			}
		}
		resp.Body.Close()
		if len(newCalls) == 0 {
			return true, nil
		}
		st.calls = newCalls
		st.round++
	}
	return false, fmt.Errorf("vision_v2: 工具循环超过 %d 轮", maxToolRounds)
}

// toolArguments 工具参数 JSON 结构（chat/responses 的 arguments 用）。
type toolArguments struct {
	ImageID string `json:"image_id"`
	Prompt  string `json:"prompt"`
}

// buildAssistantToolMessage 按格式构造 assistant 工具调用消息：
// chat：tool_calls 数组；claude：content 里的 tool_use 块；responses：input 里的 function_call item。
func buildAssistantToolMessage(calls []ToolCall, format visionProxyFormat) map[string]any {
	switch format {
	case formatClaude:
		blocks := make([]any, 0, len(calls))
		for _, c := range calls {
			blocks = append(blocks, map[string]any{
				"type": "tool_use", "id": c.ID, "name": c.Name,
				"input": map[string]any{"image_id": c.ImageID, "prompt": c.Prompt},
			})
		}
		return map[string]any{"role": "assistant", "content": blocks}
	case formatResponses:
		c := calls[0]
		argsJSON, _ := json.Marshal(toolArguments{ImageID: c.ImageID, Prompt: c.Prompt})
		return map[string]any{
			"type": "function_call", "id": c.ID, "call_id": c.ID, "name": c.Name,
			"arguments": string(argsJSON),
		}
	default: // formatChat
		tcs := make([]any, 0, len(calls))
		for _, c := range calls {
			argsJSON, _ := json.Marshal(toolArguments{ImageID: c.ImageID, Prompt: c.Prompt})
			tcs = append(tcs, map[string]any{
				"id": c.ID, "type": "function",
				"function": map[string]any{"name": c.Name, "arguments": string(argsJSON)},
			})
		}
		return map[string]any{"role": "assistant", "content": nil, "tool_calls": tcs}
	}
}

// buildToolResultMessage 按格式构造工具结果消息：
// chat：role=tool；claude：role=user + tool_result 块；responses：function_call_output item。
func buildToolResultMessage(c ToolCall, text string, format visionProxyFormat) any {
	switch format {
	case formatClaude:
		return map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": c.ID, "content": text},
		}}
	case formatResponses:
		return map[string]any{"type": "function_call_output", "call_id": c.ID, "output": text}
	default: // formatChat
		return map[string]any{"role": "tool", "tool_call_id": c.ID, "content": text}
	}
}

// msgArrayKey 各格式消息数组的 body 字段名：responses 用 input，其余用 messages。
func msgArrayKey(format visionProxyFormat) string {
	if format == formatResponses {
		return "input"
	}
	return "messages"
}

// mainChannelForContinuation 定位续流主渠道（工具结果后的新一轮上游请求）：
// 优先 __current_channel（原主链路渠道），回退 __last_tried_channel。
// 与视觉识别区分：识别走路由 via_options failover，续流必须回到主对话渠道。
func (s *Service) mainChannelForContinuation(pipe *modelgateway.ProxyPipeline) *modelgateway.ResolvedChannel {
	chID := ""
	if v, ok := pipe.Metadata["__current_channel"].(string); ok && v != "" {
		chID = v
	} else if v, ok := pipe.Metadata["__last_tried_channel"].(string); ok && v != "" {
		chID = v
	}
	if chID != "" {
		if ch, err := s.resolveChannel(context.Background(), chID); err == nil {
			return ch
		}
	}
	return nil
}

// doUpstream 发起工具循环的续流请求：POST {channel.BaseURL}/{pipe.Request.Path}，
// 带 Content-Type/Authorization，body 为 reqBody。渠道复用原主链路
// （mainChannelForContinuation：__current_channel → __last_tried_channel → 路由兜底）。
// 返回上游响应（调用方负责 Close Body）。超时 config.VisionTimeout。
func (s *Service) doUpstream(pipe *modelgateway.ProxyPipeline, reqBody []byte) (*http.Response, error) {
	ch := s.mainChannelForContinuation(pipe)
	if ch == nil {
		return nil, errors.New("vision_v2: 无法定位主链路渠道")
	}
	ctx := context.Background()
	if pipe.HTTPRequest != nil && pipe.HTTPRequest.Context() != nil {
		ctx = pipe.HTTPRequest.Context()
	}
	target := strings.TrimRight(ch.BaseURL, "/") + "/" + pipe.Request.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if ch.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+ch.APIKey)
	}
	client := &http.Client{Timeout: config.VisionTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, fmt.Errorf("vision_v2: 续流上游返回错误(%d): %s", resp.StatusCode, string(b))
	}
	return resp, nil
}

// resolveChannel 按 channel_id 查渠道表（SQLite，与主路由同源）并解密 api_key。
// 旧版读 channels.json 文件：渠道存储迁移到 db 后文件不再存在，故统一走 Repository。
func (s *Service) resolveChannel(ctx context.Context, channelID string) (*modelgateway.ResolvedChannel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("vision_v2: 渠道仓储未初始化")
	}
	channels, err := s.repo.ListChannels(ctx)
	if err != nil {
		return nil, fmt.Errorf("vision_v2: 读取渠道表失败: %w", err)
	}
	for _, ch := range channels {
		if ch.ID != channelID {
			continue
		}
		key := ""
		if ch.APIKeyCipher != "" {
			k, err := s.st.Decrypt(ch.APIKeyCipher)
			if err != nil {
				return nil, fmt.Errorf("vision_v2: 解密渠道 %q 的 api_key 失败: %w", channelID, err)
			}
			key = k
		}
		return &modelgateway.ResolvedChannel{ID: ch.ID, Name: ch.Name, ChannelName: ch.ChannelName, BaseURL: ch.BaseURL, APIKey: key}, nil
	}
	return nil, fmt.Errorf("vision_v2: 渠道不存在: %s", channelID)
}
