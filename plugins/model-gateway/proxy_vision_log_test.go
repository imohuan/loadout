package modelgateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleProxyStartBeforeHook 验证：route_requests 占位日志（running）在
// before-upstream hook 执行之前就已写入——视觉识别可能耗时数十秒，UI 在识别期间
// 就能通过 /api/route-logs 轮询到这条记录，而不是等识别完成。
func TestHandleProxyStartBeforeHook(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)

	ctx := newMockCtx()
	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		// hook 执行时占位日志必须已写入（hook 前 Start 已发生）。
		if len(log.starts) != 1 {
			t.Fatalf("hook 执行时应有 1 条占位 start，实际 %d", len(log.starts))
		}
		if log.starts[0].RequestID == "" || log.starts[0].RequestID != pipe.RequestID {
			t.Fatalf("占位 start 的 request_id 与 pipe 不一致: %q vs %q", log.starts[0].RequestID, pipe.RequestID)
		}
		if log.starts[0].RequestedModel != "deepseek-chat" {
			t.Fatalf("占位 start 的 requested_model 应为 deepseek-chat，实际 %q", log.starts[0].RequestedModel)
		}
		return pipe, nil
	})
	svc.ctx = ctx

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	svc.HandleProxy(rec, req)
}

// TestHandleProxyVirtualModelSecondStart 验证：hook 设置 __virtual_model 后，
// 第二次 Start（UPSERT）补全虚拟模型名，且不新增记录（同一 request_id 合并）。
func TestHandleProxyVirtualModelSecondStart(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)

	ctx := newMockCtx()
	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		pipe.Metadata["__virtual_model"] = "auto-demo"
		return pipe, nil
	})
	svc.ctx = ctx

	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	svc.HandleProxy(rec, req)

	if len(log.starts) != 2 {
		t.Fatalf("应有 2 条 start（占位 + 补全），实际 %d", len(log.starts))
	}
	first := log.starts[0]
	if first.RequestedModel != "deepseek-chat" {
		t.Fatalf("占位 start requested_model 应为 deepseek-chat，实际 %q", first.RequestedModel)
	}
	second := log.starts[1]
	if second.RequestID != first.RequestID {
		t.Fatalf("两次 start 应共用同一 request_id: %q vs %q", first.RequestID, second.RequestID)
	}
	if second.VirtualModel != "auto-demo" || second.RequestedModel != "auto-demo" {
		t.Fatalf("补全 start 应带虚拟模型: virtual=%q requested=%q", second.VirtualModel, second.RequestedModel)
	}
}
