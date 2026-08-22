package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loadout/plugins/types"
)

// TestProxyBeforeAttemptFiresPerAttempt 多渠道 failover 时每个渠道尝试都触发
// proxy:before-attempt，注入正确的当前渠道上下文，且第二个渠道收到安检改写后的 body。
func TestProxyBeforeAttemptFiresPerAttempt(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	var mu sync.Mutex
	var attempts []string
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe, ok := payload.(*ProxyPipeline)
		if !ok || pipe == nil {
			return payload, nil
		}
		mu.Lock()
		ch, _ := pipe.Metadata["__current_channel"].(string)
		attempts = append(attempts, ch)
		mu.Unlock()
		// 模拟安检改写 body：加标记字段
		pipe.Request.Body = []byte(`{"marked":true,"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`)
		return pipe, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 {
		t.Fatalf("ProxyBeforeAttempt 触发 %d 次, 期望 2（两个渠道各一次）", len(attempts))
	}
	if attempts[0] != "ch-a" || attempts[1] != "ch-b" {
		t.Fatalf("触发渠道顺序 = %v, 期望 [ch-a ch-b]", attempts)
	}
	recs := getRecordsB()
	if len(recs) != 1 {
		t.Fatalf("渠道B收到 %d 个请求, 期望 1", len(recs))
	}
	if !strings.Contains(string(recs[0].Body), `"marked":true`) {
		t.Fatalf("渠道B收到的 body 不是安检改写后的: %s", recs[0].Body)
	}
}

// TestProxyBeforeAttemptRejectsStopsRequest 安检钩子返回错误时终止整个请求（不换渠道）。
func TestProxyBeforeAttemptRejectsStopsRequest(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		return payload, &GatewayError{Type: "sensitive_filter_error", Msg: "请求命中敏感词规则"}
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"bad"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400（安检拒绝）", rr.Code)
	}
	recs := getRecordsB()
	if len(recs) != 0 {
		t.Fatalf("安检拒绝后渠道B不应收到请求, 实际 %d 个", len(recs))
	}
}
