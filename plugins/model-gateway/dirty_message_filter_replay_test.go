package modelgateway

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 回归（真实事故 16cc88b2f06a8d211fe53554c6744126）：线上 400 后回放真实请求体，
// 确认剥除函数对真实脏消息生效——assistant content="" 且 tool_calls[0].function.name=""。
func TestStripBrokenAssistantReplayRealLog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.TempDir(), "log_1017.json"))
	if err != nil {
		t.Skip("真实日志文件不存在，跳过回放测试")
	}
	var logEntry struct {
		RequestJSON struct {
			Body string `json:"body"`
		} `json:"request_json"`
	}
	if err := json.Unmarshal(raw, &logEntry); err != nil || logEntry.RequestJSON.Body == "" {
		t.Skip("日志里没有完整 body，跳过")
	}
	body := []byte(logEntry.RequestJSON.Body)
	filtered, changed := stripBrokenAssistantMessages(body)
	if !changed {
		t.Fatal("real dirty body should be stripped")
	}
	var out struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(filtered, &out); err != nil {
		t.Fatal(err)
	}
	for i, m := range out.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.Function.Name == "" {
					t.Fatalf("message %d still has empty tool name after strip", i)
				}
			}
		}
	}
	if len(out.Messages) != 10 {
		t.Fatalf("expected 10 messages after strip, got %d", len(out.Messages))
	}
}
