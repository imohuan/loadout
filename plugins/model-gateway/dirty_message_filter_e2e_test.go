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
// 脏消息清理已拆成 toolcall_repair 能力，本测试只锁定「网关核心不再隐式改写」——
// 未配置该能力时，带空工具名的 body 必须原封不动转发给上游。
// 清理逻辑与「配置后生效」的端到端测试见 plugins/toolcall-repair。
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
		t.Fatalf("响应状态 = %d, 期望 200。响应: %s", rr.Code, rr.Body.String()[:min(300, rr.Body.Len())])
	}
	// 未配置 toolcall_repair 路由：不剥、不补，字节级透传（透明代理语义）。
	if string(gotBody) != original {
		t.Fatalf("未配置能力路由时不应改写请求体：收到 %d 字节，原始 %d 字节", len(gotBody), len(original))
	}
}

