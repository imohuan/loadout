package failurerules

import (
	"strings"
	"testing"
)

// TestStreamCapturesToolCallDeltas 用户明确提到第三种形态：MCP / 工具调用的内容
// 也要能看到。工具参数是分片吐出来的，逐片回调即可——但不能混进 content，
// 否则上层解析规则 JSON 会被工具参数污染。
func TestStreamCapturesToolCallDeltas(t *testing.T) {
	body := sseJSON(
		`{"choices":[{"delta":{"tool_calls":[{"function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"th\":\"a.txt\"}"}}]}}]}`,
		`{"choices":[{"delta":{"function_call":{"name":"mcp__x","arguments":"{}"}}}]}`,
	)
	var seen []string
	var toolText string
	content, _, err := collectStreamDeltas(strings.NewReader(body), func(kind, s string) {
		seen = append(seen, s)
		if kind == StreamKindTool {
			toolText += s
		}
	})
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	joined := strings.Join(seen, "")
	for _, want := range []string{"read_file", "a.txt", "mcp__x"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("工具调用增量缺少 %q，实际收到 %q", want, joined)
		}
	}
	if !strings.Contains(toolText, "read_file") {
		t.Fatalf("工具增量应带 tool 类型标签，实际 %q", toolText)
	}
	// 工具参数不得污染正文（否则规则 JSON 解析失败）。
	if content != "" {
		t.Fatalf("工具调用不应进入正文，实际 %q", content)
	}
}
