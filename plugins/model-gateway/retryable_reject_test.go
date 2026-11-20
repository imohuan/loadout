package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"loadout/plugins/types"
)

// TestRetryableRejectFailsOverToNextChannel 回归（volc-free-quota 额度预测的依赖）：
// proxy:before-attempt 返回带 Retryable 的 GatewayError 时，只算「这条渠道这次不行」：
// 网关要落一条 failed attempt，并继续试下一个渠道，而不是整单拒绝客户端。
func TestRetryableRejectFailsOverToNextChannel(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, getRecordsA := newEchoServer(t, `{"id":"resp_a"}`, 0, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_b","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	var mu sync.Mutex
	var seen []string
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe, ok := payload.(*ProxyPipeline)
		if !ok || pipe == nil {
			return payload, nil
		}
		ch, _ := pipe.Metadata[MetadataCurrentChannel].(string)
		mu.Lock()
		seen = append(seen, ch)
		mu.Unlock()
		if ch == "ch-a" {
			return nil, &GatewayError{
				Status:       http.StatusTooManyRequests,
				Type:         "free_quota_exhausted",
				Msg:          "模型免费额度用完",
				FailureClass: "free_quota_exhausted",
				Retryable:    true,
			}
		}
		return pipe, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("可重试拒绝应切到第二个渠道并成功，实际 %d: %s", rr.Code, rr.Body.String())
	}
	mu.Lock()
	order := append([]string(nil), seen...)
	mu.Unlock()
	if len(order) != 2 || order[0] != "ch-a" || order[1] != "ch-b" {
		t.Fatalf("尝试顺序 = %v, 期望 [ch-a ch-b]", order)
	}
	if len(getRecordsA()) != 0 {
		t.Fatal("被预测失败的渠道不应真的发出上游请求")
	}
	if len(getRecordsB()) != 1 {
		t.Fatal("第二个渠道应收到一次真实请求")
	}
}

// TestNonRetryableRejectStillTerminates 对照：普通（不可重试）拒绝仍然终止整个请求，
// 保证安检类拒绝的原有语义没有被削弱。
func TestNonRetryableRejectStillTerminates(t *testing.T) {
	svc, _ := newTestService(t)
	echoA, getRecordsA := newEchoServer(t, `{"id":"resp_a"}`, 0, nil)
	defer echoA.Close()
	echoB, getRecordsB := newEchoServer(t, `{"id":"resp_b"}`, 0, nil)
	defer echoB.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		return nil, &GatewayError{Status: http.StatusBadRequest, Type: "sensitive_filter_error", Msg: "命中敏感词"}
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("不可重试拒绝应直接返回 400，实际 %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "命中敏感词") {
		t.Fatalf("响应应包含拒绝原因: %s", rr.Body.String())
	}
	if len(getRecordsA()) != 0 || len(getRecordsB()) != 0 {
		t.Fatal("不可重试拒绝不应发出任何上游请求")
	}
}
