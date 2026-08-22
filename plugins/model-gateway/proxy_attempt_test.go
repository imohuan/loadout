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

// TestProxyBeforeAttemptFiresAfterAggregateFailover 聚合模型切目标后，递归的
// 新渠道尝试同样触发 proxy:before-attempt（修复"切模型跳过安检"）。
func TestProxyBeforeAttemptFiresAfterAggregateFailover(t *testing.T) {
	svc, _ := newTestService(t)
	echo1, _ := newEchoServer(t, `{"error":{"message":"down"}}`, 500, nil)
	defer echo1.Close()
	echo2, _ := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echo2.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-1", Name: "渠道1", BaseURL: echo1.URL, Enabled: true, Models: []string{"gpt-4o"}},
		{ID: "ch-2", Name: "渠道2", BaseURL: echo2.URL, Enabled: true, Models: []string{"claude-3"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// 入口 before 钩子（模拟 aggregate）：虚拟模型 auto → 目标1 gpt-4o
	svc.ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		if pipe.Metadata == nil {
			pipe.Metadata = map[string]any{}
		}
		pipe.Metadata["__virtual_model"] = "auto"
		pipe.Metadata["__aggregate_targets"] = []types.AggregateTarget{
			{Model: "gpt-4o", ChannelID: "ch-1"},
			{Model: "claude-3", ChannelID: "ch-2"},
		}
		pipe.Metadata["__failed_targets"] = []string{}
		pipe.Request.Model = "gpt-4o"
		return pipe, nil
	})
	// failover 钩子（模拟 aggregate 切目标2，重设渠道上下文：与真实 applyTargetMetadata
	// 语义一致，__current_channel_base_url 清空、__channel_candidates 置 nil，
	// 否则残留的旧渠道 base_url/候选列表会让递归 resolveChannels 锁死在旧渠道）
	svc.ctx.On(ProxyUpstreamFailed, func(payload any) (any, error) {
		f := payload.(*ProxyFailurePayload)
		f.Pipe.Metadata["__failed_targets"] = append(f.Pipe.Metadata["__failed_targets"].([]string), "gpt-4o@ch-1")
		f.Pipe.Metadata["__current_channel"] = "ch-2"
		f.Pipe.Metadata["__current_channel_base_url"] = ""
		f.Pipe.Metadata["__channel_candidates"] = nil
		f.Pipe.Request.Model = "claude-3"
		return &ProxyRetry{Pipe: f.Pipe}, nil
	})
	// 安检钩子：记录每次触发的 model@渠道
	var mu sync.Mutex
	var seen []string
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		mu.Lock()
		ch, _ := pipe.Metadata["__current_channel"].(string)
		seen = append(seen, pipe.Request.Model+"@"+ch)
		mu.Unlock()
		return payload, nil
	})

	body := `{"model":"auto"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("ProxyBeforeAttempt 触发 %d 次, 期望 2（目标1 + 目标2 各一次）: %v", len(seen), seen)
	}
	if seen[0] != "gpt-4o@ch-1" || seen[1] != "claude-3@ch-2" {
		t.Fatalf("安检触发顺序 = %v, 期望 [gpt-4o@ch-1 claude-3@ch-2]", seen)
	}
}

// TestProxyBeforeAttemptFiresWhenNoChannels 模型无可用渠道时安检仍执行：
// 敏感词 error 模式命中应返回 400（而非 502），内容违规优先于渠道不可用。
func TestProxyBeforeAttemptFiresWhenNoChannels(t *testing.T) {
	svc, _ := newTestService(t)
	// 不写任何渠道：model 无候选 → 走 proxyForward 早退分支
	var called bool
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		called = true
		return payload, &GatewayError{Type: "sensitive_filter_error", Msg: "请求命中敏感词规则"}
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"bad"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if !called {
		t.Fatal("无渠道时安检未触发")
	}
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("状态码 = %d, 期望 400（安检拒绝优先于 502）", rr.Code)
	}
}

// TestProxyBeforeAttemptAfterHookRejectContinues 非流式 after-hook 拒绝（视为渠道失败）
// 后，下一渠道尝试仍触发安检且能成功。
func TestProxyBeforeAttemptAfterHookRejectContinues(t *testing.T) {
	svc, _ := newTestService(t)
	// 渠道 A：返回 200 但 after-hook 拒绝（如 field-filter 无法处理该响应）
	echoA, _ := newEchoServer(t, `{"id":"resp_a","object":"response"}`, 0, nil)
	defer echoA.Close()
	echoB, _ := newEchoServer(t, `{"id":"resp_b","object":"response"}`, 0, nil)
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
		pipe := payload.(*ProxyPipeline)
		mu.Lock()
		ch, _ := pipe.Metadata["__current_channel"].(string)
		attempts = append(attempts, ch)
		mu.Unlock()
		return payload, nil
	})
	// after-hook：仅拒绝渠道 A 的响应（200 但内容不被接受）
	svc.ctx.On(ProxyAfterUpstream, func(payload any) (any, error) {
		after := payload.(*AfterUpstreamPayload)
		if after.Pipe.Metadata["__current_channel"] == "ch-a" {
			return payload, &GatewayError{Type: "upstream_error", Msg: "响应不被接受"}
		}
		return payload, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（渠道B成功）", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "resp_b") {
		t.Fatalf("响应体应为渠道B内容: %s", rr.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 || attempts[0] != "ch-a" || attempts[1] != "ch-b" {
		t.Fatalf("安检触发 = %v, 期望 [ch-a ch-b]（after-hook 拒绝后下一渠道仍安检）", attempts)
	}
}
