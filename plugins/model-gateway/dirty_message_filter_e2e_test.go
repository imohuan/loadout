package modelgateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/plugins/types"
)

// 端到端回归（真实事故 16cc88b2f06a8d211fe53554c6744126）：
// 把线上 400 的请求体【原封不动】发进 HandleProxy 全链路（不设 gateway key 的测试通道），
// mock 上游捕获实际收到的 body —— 断言脏 assistant 消息（空 content + 空 tool name）
// 已在转发前被剥除，且其引用的 tool 结果一并剥除。
func TestHandleProxyStripsDirtyAssistantRealBody(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(os.TempDir(), "log_1017.json"))
	if err != nil {
		t.Skip("真实日志文件不存在，跳过")
	}
	var logEntry struct {
		RequestJSON struct {
			Body string `json:"body"`
		} `json:"request_json"`
	}
	if err := json.Unmarshal(raw, &logEntry); err != nil || logEntry.RequestJSON.Body == "" {
		t.Skip("日志无完整 body，跳过")
	}
	original := logEntry.RequestJSON.Body

	var received struct {
		Body string `json:"-"`
	}
	var gotBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer upstream.Close()

	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-real", Name: "real-key", ChannelName: "step", BaseURL: upstream.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
	req.Header.Set("X-Request-Id", "replay-16cc88b2")
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("响应状态 = %d, 期望 200（剥除后上游应接受）。响应: %s", rr.Code, rr.Body.String()[:min(300, rr.Body.Len())])
	}
	var forwarded struct {
		Messages []struct {
			Role      string `json:"role"`
			ToolCalls []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(gotBody, &forwarded); err != nil {
		t.Fatalf("解析上游收到的 body 失败: %v", err)
	}
	for i, m := range forwarded.Messages {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				if tc.Function.Name == "" {
					t.Fatalf("上游收到的第 %d 条消息仍含空 tool name —— 剥除未生效", i)
				}
			}
		}
	}
	if len(forwarded.Messages) != 10 {
		t.Fatalf("上游应收到 10 条消息（12 - 脏assistant - 其tool结果），实际 %d", len(forwarded.Messages))
	}
	_ = received
}

