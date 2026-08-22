package visionv2

import (
	"testing"
)

// feedLines 依次 Feed 一行行 SSE 内容。
func feedLines(acc *StreamAccumulator, calls *[]ToolCall, lines ...string) {
	for _, l := range lines {
		acc.Feed(l, calls)
	}
}

func TestStreamChatMultiTool(t *testing.T) {
	acc := NewStreamAccumulator(formatChat)
	var calls []ToolCall

	feedLines(acc, &calls,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"look_at_image","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"p1\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"function":{"arguments":"{\"image_id\":\"bbb222\",\"prompt\":\"p2\"}"}}]}}]}`,
	)
	if len(calls) != 0 {
		t.Fatalf("finish_reason 之前不应有收集结果，got %d", len(calls))
	}

	acc.Feed(`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`, &calls)

	if len(calls) != 2 {
		t.Fatalf("期望 2 个工具调用，got %d", len(calls))
	}
	first, second := calls[0], calls[1]
	if first.ID != "call_1" || first.Name != "look_at_image" || first.ImageID != "aaa111" || first.Prompt != "p1" {
		t.Errorf("第一个调用解析错误: %+v", first)
	}
	if second.ID != "call_2" || second.Name != "look_at_image" || second.ImageID != "bbb222" || second.Prompt != "p2" {
		t.Errorf("第二个调用解析错误: %+v", second)
	}
}

func TestStreamChatContentAndToolMixed(t *testing.T) {
	acc := NewStreamAccumulator(formatChat)
	var calls []ToolCall

	feedLines(acc, &calls,
		`data: {"choices":[{"delta":{"content":"我看到了图片"}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"p1\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)

	if len(calls) != 1 {
		t.Fatalf("期望 1 个工具调用，got %d", len(calls))
	}
	if calls[0].ImageID != "aaa111" || calls[0].Prompt != "p1" || calls[0].Name != "look_at_image" {
		t.Errorf("调用解析错误: %+v", calls[0])
	}
}

func TestStreamClaudeToolUse(t *testing.T) {
	acc := NewStreamAccumulator(formatClaude)
	var calls []ToolCall

	feedLines(acc, &calls,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"look_at_image","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"image_id\":\"aaa111\""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":",\"prompt\":\"p1\"}"}}`,
		``,
	)
	if len(calls) != 0 {
		t.Fatalf("content_block_stop 之前不应有收集结果，got %d", len(calls))
	}

	feedLines(acc, &calls,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	)

	if len(calls) != 1 {
		t.Fatalf("期望 1 个工具调用，got %d", len(calls))
	}
	got := calls[0]
	if got.ID != "toolu_1" || got.Name != "look_at_image" || got.ImageID != "aaa111" || got.Prompt != "p1" {
		t.Errorf("调用解析错误: %+v", got)
	}
}

func TestStreamResponsesToolCall(t *testing.T) {
	acc := NewStreamAccumulator(formatResponses)
	var calls []ToolCall

	feedLines(acc, &calls,
		`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"look_at_image","arguments":""}}`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"image_id\":\"aaa111\""`,
		`data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":",\"prompt\":\"p1\"}"}`,
	)
	if len(calls) != 0 {
		t.Fatalf("output_item.done 之前不应有收集结果，got %d", len(calls))
	}

	acc.Feed(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"fc_1","name":"look_at_image","arguments":"{\"image_id\":\"aaa111\",\"prompt\":\"p1\"}"}}`, &calls)

	if len(calls) != 1 {
		t.Fatalf("期望 1 个工具调用，got %d", len(calls))
	}
	got := calls[0]
	if got.ID != "fc_1" || got.Name != "look_at_image" || got.ImageID != "aaa111" || got.Prompt != "p1" {
		t.Errorf("调用解析错误: %+v", got)
	}
}

func TestPlaceholderFilter(t *testing.T) {
	// 单行完整占位符被剔除
	f1 := &PlaceholderFilter{}
	if got := f1.Filter("前缀 <vision_img_abc> 后缀"); got != "前缀  后缀" {
		t.Errorf("单行剔除错误: %q", got)
	}

	// 跨两行拆分：<vision_img_ 在行尾，abc> 在下一行
	f2 := &PlaceholderFilter{}
	if got := f2.Filter("跨行 <vision_img_"); got != "跨行 " {
		t.Errorf("跨行第一行处理错误: %q", got)
	}
	if got := f2.Filter("abc> 结束"); got != " 结束" {
		t.Errorf("跨行第二行处理错误: %q", got)
	}

	// 正常文本不受影响
	f3 := &PlaceholderFilter{}
	if got := f3.Filter("正常文本没有占位符"); got != "正常文本没有占位符" {
		t.Errorf("正常文本被改动: %q", got)
	}
}

// TestStreamNonVisionToolPassthrough 工具调用是 web_search（非 look_at_image）：
// IsNonVision()=true、calls 为空（本轮不拦截，整轮透传）。
func TestStreamNonVisionToolPassthrough(t *testing.T) {
	acc := NewStreamAccumulator(formatChat)
	var calls []ToolCall
	feedLines(acc, &calls,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"天气\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if len(calls) != 0 {
		t.Fatalf("非 vision 工具不应收集，got %d 条", len(calls))
	}
	if !acc.IsNonVision() {
		t.Error("IsNonVision() 应为 true")
	}
	// 混合轮（look_at_image + web_search）同样标记透传且 vision 调用不追加。
	mixed := NewStreamAccumulator(formatChat)
	var mixedCalls []ToolCall
	feedLines(mixed, &mixedCalls,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"look_at_image","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"web_search","arguments":""}}]}}]}`,
		`data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
	)
	if len(mixedCalls) != 0 {
		t.Fatalf("混合轮工具不应收集，got %d 条", len(mixedCalls))
	}
	if !mixed.IsNonVision() {
		t.Error("混合轮 IsNonVision() 应为 true")
	}
}
