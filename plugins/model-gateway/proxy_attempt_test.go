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

// TestProxyAggregateSkipsMissingModel 聚合目标模型不存在（渠道 Models 不含该模型）时：
// candidates=0 → failover 跳过该目标（failedKeys 记 "model@"）→ 选下一个可用目标。
func TestProxyAggregateSkipsMissingModel(t *testing.T) {
	svc, _ := newTestService(t)
	// 渠道 ch-a 只声明 glm-5（qwen3.7-plus 不存在于渠道 Models → candidates=0）
	echo, _ := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echo.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "聚合渠道", BaseURL: echo.URL, Enabled: true, Models: []string{"glm-5"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// 聚合 before hook：free → 第一个目标 qwen3.7-plus（模型不存在）
	svc.ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		if pipe.Metadata == nil {
			pipe.Metadata = map[string]any{}
		}
		pipe.Metadata["__virtual_model"] = "free"
		pipe.Metadata["__aggregate_targets"] = []types.AggregateTarget{
			{Model: "qwen3.7-plus", ChannelID: "ch-a"},
			{Model: "glm-5", ChannelID: "ch-a"},
		}
		pipe.Metadata["__failed_targets"] = []string{}
		pipe.Metadata["__current_channel"] = "ch-a"
		pipe.Metadata["__current_channel_base_url"] = ""
		pipe.Request.Model = "qwen3.7-plus"
		return pipe, nil
	})
	// failover hook：模拟 selectAvailableTarget 修复后的行为——failedKeys 记 "model@"
	//（candidates=0 时 channelID 为空），跳过 qwen3.7-plus 选 glm-5
	svc.ctx.On(ProxyUpstreamFailed, func(payload any) (any, error) {
		f := payload.(*ProxyFailurePayload)
		f.Pipe.Metadata["__failed_targets"] = append(f.Pipe.Metadata["__failed_targets"].([]string), "qwen3.7-plus@")
		f.Pipe.Metadata["__current_channel"] = "ch-a"
		f.Pipe.Metadata["__current_channel_base_url"] = ""
		f.Pipe.Request.Model = "glm-5"
		f.Pipe.Request.Body = []byte(`{"model":"glm-5"}`)
		return &ProxyRetry{Pipe: f.Pipe}, nil
	})

	body := `{"model":"free"}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200（跳过不存在的 qwen3.7-plus 后 glm-5 成功）", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "resp_ok") {
		t.Fatalf("响应体应为 glm-5 上游内容: %s", rr.Body.String())
	}
}

// TestProxyAttemptLogCarriesRequestLogID 回归：proxyAttemptLog 写 attempt 行时必须
// 带出 request-log 插件注入的 MetadataRequestLogAttemptID（per-attempt 独立日志关联）。
func TestProxyAttemptLogCarriesRequestLogID(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)
	echo, _ := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echo.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// 模拟 request-log：before-attempt 时生成 per-attempt UUID 写入 metadata
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe, ok := payload.(*ProxyPipeline)
		if !ok || pipe == nil {
			return payload, nil
		}
		pipe.Metadata[MetadataRequestLogAttemptID] = "uuid-attempt-1"
		return pipe, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d, 期望 200", rr.Code)
	}
	// 非流式路径：running 占位 + success UPSERT 同 step 两次 Attempt 调用，
	// 两次都必须带出 per-attempt 的 RequestLogID。
	if len(log.attempts) != 2 {
		t.Fatalf("attempts = %d, 期望 2（running 占位 + success 收尾）", len(log.attempts))
	}
	for i, a := range log.attempts {
		if a.RequestLogID != "uuid-attempt-1" {
			t.Fatalf("attempts[%d].RequestLogID = %q, 期望 uuid-attempt-1", i, a.RequestLogID)
		}
	}
}

// TestProxyAttemptLogEmitsAttemptFailed 回归：每次渠道尝试失败（非流式 4xx/5xx）必须
// emit proxy:attempt-failed，让 request-log 即时收尾失败 attempt 的半条日志——
// 修复「普通模型失败、聚合模型中间失败 attempt 收不到 ProxyUpstreamFailed」。
func TestProxyAttemptLogEmitsAttemptFailed(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)
	echo, _ := newEchoServer(t, `{"error":{"message":"quota exhausted"}}`, 429, nil)
	defer echo.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	var got *ProxyFailurePayload
	svc.ctx.On(ProxyAttemptFailed, func(payload any) (any, error) {
		if fp, ok := payload.(*ProxyFailurePayload); ok {
			got = fp
		}
		return payload, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", rr.Code)
	}
	if got == nil {
		t.Fatal("ProxyAttemptFailed 未触发（失败 attempt 必须 emit）")
	}
	if got.StatusCode != 429 {
		t.Fatalf("StatusCode = %d, 期望 429", got.StatusCode)
	}
	if !strings.Contains(got.ErrorBody, "quota exhausted") {
		t.Fatalf("ErrorBody = %q, 期望包含 quota exhausted", got.ErrorBody)
	}
	if got.Model != "gpt-5" || got.ChannelID != "ch-a" {
		t.Fatalf("payload model/channel = %q/%q, 期望 gpt-5/ch-a", got.Model, got.ChannelID)
	}
}

// TestProxyAttemptLogEmitsAttemptFailedForSubRequest 回归：子请求（__sub_request=true，
// multimodal-mcp 语音识别/视觉续流）渠道尝试失败时也必须 emit proxy:attempt-failed，
// 让 request-log 即时收尾失败 attempt 的半条日志——修复「子请求失败时 proxyAttemptLog
// 提前 return，ProxyAttemptFailed 不发，request_logs 永远卡 running、上游错误丢失」。
// 同时断言子请求仍不写 route-log attempt（isSubRequest 语义不变）。
func TestProxyAttemptLogEmitsAttemptFailedForSubRequest(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)
	echo, _ := newEchoServer(t, `{"error":{"message":"quota exhausted"}}`, 429, nil)
	defer echo.Close()
	if err := svc.st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	// 标记为子请求（ForwardSubRequest 在转发前设置 __sub_request=true）。
	svc.ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe, ok := payload.(*ProxyPipeline)
		if ok && pipe != nil {
			pipe.Metadata["__sub_request"] = true
			pipe.Metadata["__sub_request_skip_security"] = true
		}
		return payload, nil
	})

	var got *ProxyFailurePayload
	svc.ctx.On(ProxyAttemptFailed, func(payload any) (any, error) {
		if fp, ok := payload.(*ProxyFailurePayload); ok {
			got = fp
		}
		return payload, nil
	})

	body := `{"model":"gpt-5","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	rr := httptest.NewRecorder()
	svc.HandleProxy(rr, req)

	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("状态码 = %d, 期望 429", rr.Code)
	}
	if got == nil {
		t.Fatal("子请求失败也必须 emit ProxyAttemptFailed（P0 修复后应触发）")
	}
	if got.StatusCode != 429 || !strings.Contains(got.ErrorBody, "quota exhausted") {
		t.Fatalf("payload StatusCode/ErrorBody = %d/%q, 期望 429/quota exhausted", got.StatusCode, got.ErrorBody)
	}
	// 子请求仍不应写 route-log attempt（route_requests 由调用方独立折叠），语义不变。
	if len(log.attempts) != 0 {
		t.Fatalf("子请求不应写 route-log attempt, got %d", len(log.attempts))
	}
}
