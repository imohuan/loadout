package modelgateway

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"loadout/plugins/types"
)

// TestForwardSubRequestNonStream 非流式子请求：走主链路完整管线，返回响应 body，
// 强制 __sub_request / __sub_request_skip_security 标记。
func TestForwardSubRequestNonStream(t *testing.T) {
	svc, st := newTestService(t)
	echo, records := newEchoServer(t, `{"id":"resp_ok","object":"response"}`, 0, nil)
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	// 注册安检钩子：验证子请求也触发 ProxyBeforeAttempt 且拿到 __sub_request 标记。
	var seenSubRequest bool
	svc.ctx.On(ProxyBeforeAttempt, func(payload any) (any, error) {
		pipe, _ := payload.(*ProxyPipeline)
		if pipe != nil {
			if v, _ := pipe.Metadata["__sub_request"].(bool); v {
				seenSubRequest = true
			}
		}
		return payload, nil
	})

	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-5",
		},
	}
	final, body, err := svc.ForwardSubRequest(context.Background(), pipe, nil)
	if err != nil {
		t.Fatalf("ForwardSubRequest 报错: %v", err)
	}
	if !strings.Contains(string(body), "resp_ok") {
		t.Fatalf("响应 body 异常: %s", body)
	}
	if !seenSubRequest {
		t.Fatal("子请求应触发 ProxyBeforeAttempt 且 metadata 带 __sub_request")
	}
	if v, _ := final.Metadata["__sub_request"].(bool); !v {
		t.Fatal("返回 pipe 应保留 __sub_request 标记")
	}
	if v, _ := final.Metadata["__sub_request_skip_security"].(bool); !v {
		t.Fatal("返回 pipe 应保留 __sub_request_skip_security 标记")
	}
	if !strings.HasPrefix(final.RequestID, "sub-") {
		t.Fatalf("子请求 RequestID = %q, 期望 sub- 前缀", final.RequestID)
	}
	recs := records()
	if len(recs) != 1 {
		t.Fatalf("上游收到 %d 个请求, 期望 1", len(recs))
	}
}

// TestForwardSubRequestStream 流式子请求：streamWriter 逐行收到上游 SSE 行。
func TestForwardSubRequestStream(t *testing.T) {
	svc, st := newTestService(t)
	echo, _ := newEchoServer(t, "", 0, []string{
		"data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\n",
		"data: [DONE]\n\n",
	})
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Body:   []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-5",
			Stream: true,
		},
	}
	var lines []string
	final, _, err := svc.ForwardSubRequest(context.Background(), pipe, func(line []byte) error {
		lines = append(lines, string(line))
		return nil
	})
	if err != nil {
		t.Fatalf("ForwardSubRequest 流式报错: %v", err)
	}
	// proxyStream 逐行 ReadString('\n')：回调收到每个 SSE 行（含 data 行 + 空行分隔）。
	joined := strings.Join(lines, "")
	if !strings.Contains(joined, "你") || !strings.Contains(joined, "好") || !strings.Contains(joined, "[DONE]") {
		t.Fatalf("streamWriter 未收到完整流: %v", lines)
	}
	if final == nil {
		t.Fatal("返回 pipe 不应为 nil")
	}
}

// TestForwardSubRequestFailover 候选渠道 failover：第一个渠道 500、第二个成功，
// 子请求自动切换，返回第二个渠道的响应。
func TestForwardSubRequestFailover(t *testing.T) {
	svc, st := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	echoB, _ := newEchoServer(t, `{"id":"resp_b","object":"response"}`, 0, nil)
	defer echoB.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
		{ID: "ch-b", Name: "渠道B", BaseURL: echoB.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Body:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-5",
		},
		Metadata: map[string]any{
			"__channel_candidates": []string{"ch-a", "ch-b"},
		},
	}
	_, body, err := svc.ForwardSubRequest(context.Background(), pipe, nil)
	if err != nil {
		t.Fatalf("ForwardSubRequest failover 报错: %v", err)
	}
	if !strings.Contains(string(body), "resp_b") {
		t.Fatalf("failover 后应返回渠道B响应: %s", body)
	}
}

// TestForwardSubRequestAllFail 全部候选失败：返回错误。
func TestForwardSubRequestAllFail(t *testing.T) {
	svc, st := newTestService(t)
	echoA, _ := newEchoServer(t, `{"error":{"message":"boom"}}`, 500, nil)
	defer echoA.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echoA.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Body:   []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-5",
		},
	}
	_, _, err := svc.ForwardSubRequest(context.Background(), pipe, nil)
	if err == nil {
		t.Fatal("全部候选失败应返回错误")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("错误应包含状态码: %v", err)
	}
}

// TestForwardSubRequestCtxCancel 客户端取消：ctx 取消后子请求终止（快速返回，不 hang）。
func TestForwardSubRequestCtxCancel(t *testing.T) {
	svc, st := newTestService(t)
	echo, _ := newEchoServer(t, "", 0, []string{})
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: "POST",
			Path:   "chat/completions",
			Body:   []byte(`{"model":"gpt-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
			Model:  "gpt-5",
			Stream: true,
		},
	}
	// 立即取消：验证 ForwardSubRequest 不 panic、快速返回。
	cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = svc.ForwardSubRequest(ctx, pipe, func(line []byte) error { return nil })
	}()
	select {
	case <-done:
		// OK：取消后正常返回（不强制断言具体错误，不同实现路径可能 nil）
		_ = fmt.Sprintf("ctx cancelled, sub-request returned")
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 ForwardSubRequest 未返回")
	}
}

// TestForwardSubRequestSkipsTopRowAndAttempts 回归：子请求（视觉识别/续流）走
// model-gateway 主链路时不再创建顶级 route_requests 行、不写 attempt 行——
// 视觉/续流的 1.1/1.2 由调用方自己写到主请求折叠下。前端日志列表不应再出现 sub-xxx 顶级行。
func TestForwardSubRequestSkipsTopRowAndAttempts(t *testing.T) {
	svc, st := newTestService(t)
	echo, _ := newEchoServer(t, `{"choices":[{"message":{"content":"hi"}}]}`, 0, nil)
	writeTestChannel(t, st, echo.URL)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)

	pipe := &ProxyPipeline{
		Request: &ProxyRequest{
			Method: http.MethodPost,
			Path:   "chat/completions",
			Body:   []byte(`{"model":"q"}`),
			Model:  "q",
		},
		Metadata: map[string]any{"__channel_candidates": []string{"test"}},
	}

	_, _, err := svc.ForwardSubRequest(context.Background(), pipe, nil)
	if err != nil {
		t.Fatalf("ForwardSubRequest 报错: %v", err)
	}
	if len(log.starts) != 0 {
		t.Errorf("子请求不应创建顶级 route_requests 行（Start=%d 次）", len(log.starts))
	}
	if len(log.attempts) != 0 {
		t.Errorf("子请求不应写 attempt（Attempt=%d 次）", len(log.attempts))
	}
	if len(log.finishs) != 0 {
		t.Errorf("子请求不应 finish 顶级行（Finish=%d 次）", len(log.finishs))
	}
}
