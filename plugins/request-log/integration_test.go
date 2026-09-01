package requestlog

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/contracts"
	"loadout/plugins/types"
)

// TestIntegrationCrossPluginLink 真实联动：route-log.Start 写 running → request-log
// 在 before-attempt 生成 UUID 并 UPDATE route_requests.request_log_id → route-log
// List/Detail 带出该字段 → request-log 收尾 success。
func TestIntegrationCrossPluginLink(t *testing.T) {
	// 真实 loadout.db（自带迁移）+ 真实 request-log.db
	loadout, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	defer loadout.Close()
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()

	// route-log 装配
	routeLog := routelog.NewService(loadout, nil)

	// request-log 装配（能力路由：全模型 proxy）
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(st, slog.New(slog.DiscardHandler), reqDB, loadout)

	ctx := context.Background()
	// 1) route-log.Start 写 running 占位
	started := time.Now().Add(-time.Second)
	if err := routeLog.Start(ctx, contracts.RouteRequest{RequestID: "it-1", RequestedModel: "gpt-4o", StartedAt: started}); err != nil {
		t.Fatal(err)
	}

	// 2) request-log 在请求发出前抓取 + 生成 UUID
	pipe := testPipe("it-1")
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatal(err)
	}
	uuid, _ := pipe.Metadata[metadataKey].(string)
	if uuid == "" {
		t.Fatal("uuid missing")
	}

	// 3) route-log 列表带出 request_log_id
	page, err := routeLog.List(ctx, contracts.RouteLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].RequestLogID != uuid {
		t.Fatalf("route-log List 未带出 request_log_id: %+v", page.Items)
	}

	// 4) request-log 收尾（非流式 2xx）
	pipe.Metadata["__last_tried_channel"] = "c1"
	if _, err := svc.HandleAfterUpstream(&modelgateway.AfterUpstreamPayload{
		Pipe: pipe,
		Response: &modelgateway.ProxyResponse{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       []byte(`{"choices":[{"message":{"content":"ok"}}]}`),
		},
	}); err != nil {
		t.Fatal(err)
	}

	// 5) request-log 详情：success + 完整 request/response
	d, err := svc.Detail(ctx, uuid)
	if err != nil {
		t.Fatal(err)
	}
	if d.Result != "success" {
		t.Fatalf("result = %q, want success", d.Result)
	}
	var snap requestSnapshot
	if err := json.Unmarshal(d.RequestJSON, &snap); err != nil {
		t.Fatal(err)
	}
	if snap.Model != "gpt-4o" || snap.Path != "chat/completions" {
		t.Fatalf("request snapshot wrong: %+v", snap)
	}
	if len(d.ResponseJSON) == 0 {
		t.Fatal("response_json should be set")
	}
}

// mockGatewayCtx 测试用事件总线：把 model-gateway 与 request-log 接在同一 ctx 上，
// 实现 plugin.Context 接口（与 model-gateway 内部 mockCtx 同构）。
type mockGatewayCtx struct {
	handlers map[string][]plugin.Handler
}

func (m *mockGatewayCtx) Get(name string) any            { return nil }
func (m *mockGatewayCtx) Set(name string, svc any) plugin.Disposer { return func() {} }
func (m *mockGatewayCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}
func (m *mockGatewayCtx) Emit(event string, payload any) {
	for _, h := range m.handlers[event] {
		_, _ = h(payload)
	}
}
func (m *mockGatewayCtx) Waterfall(event string, payload any) (any, error) {
	for _, h := range m.handlers[event] {
		var err error
		payload, err = h(payload)
		if err != nil {
			return payload, err
		}
	}
	return payload, nil
}
func (m *mockGatewayCtx) Effect(fn func()) plugin.Disposer { return func() {} }
func (m *mockGatewayCtx) Logger() *slog.Logger            { return slog.Default() }
func (m *mockGatewayCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockGatewayCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer {
	return func() {}
}

// TestIntegrationSubRequestFailureRecorded 端到端：子请求（__sub_request=true，
// multimodal-mcp 语音识别/视觉续流）渠道尝试失败（上游 4xx）时，request-log 必须
// 记录该失败——result=failed、http_status=400、response_json 带上游错误体。
// 修复前 proxyAttemptLog 对子请求提前 return、ProxyAttemptFailed 不发，request_logs
// 行永远卡 running、上游错误丢失。
func TestIntegrationSubRequestFailureRecorded(t *testing.T) {
	ctxBus := &mockGatewayCtx{handlers: map[string][]plugin.Handler{}}
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// model-gateway：渠道指向会返回 400 的 echo server。
	gw := modelgateway.NewService(st, slog.New(slog.DiscardHandler), ctxBus)
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"InvalidParameter","message":"content type is not supported","param":"input.content","type":"BadRequest"}}`))
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "ch-a", Name: "渠道A", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	// request-log 能力路由：全模型 proxy。
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{
		{Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy},
	}); err != nil {
		t.Fatal(err)
	}

	// request-log 装配：真实独立库 + 订阅同一事件总线。
	reqDB, err := openRequestLogDB(t.TempDir() + "/request-log.db")
	if err != nil {
		t.Fatal(err)
	}
	defer reqDB.Close()
	svc := NewService(st, slog.New(slog.DiscardHandler), reqDB, nil)
	svc.SetRepository(nil)
	svc.subscribe(ctxBus)

	pipe := &modelgateway.ProxyPipeline{
		Request: &modelgateway.ProxyRequest{
			Method: http.MethodPost,
			Path:   "responses",
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body:   []byte(`{"model":"doubao","input":[{"role":"user","content":[{"type":"input_audio","data":"AQID"}]}],"stream":false}`),
			Model:  "doubao",
			Stream: false,
		},
		Metadata: map[string]any{},
	}

	// 非流式子请求失败。
	_, _, fwdErr := gw.ForwardSubRequest(context.Background(), pipe, nil)
	if fwdErr == nil {
		t.Fatal("上游 400 时 ForwardSubRequest 应返回错误")
	}

	// 子请求经过 before-attempt 后 metadata 里应有 request-log UUID。
	uuid, _ := pipe.Metadata[modelgateway.MetadataRequestLogID].(string)
	if uuid == "" {
		uuid, _ = pipe.Metadata[modelgateway.MetadataRequestLogAttemptID].(string)
	}
	if uuid == "" {
		t.Fatal("request-log UUID 缺失，无法定位日志行")
	}
	detail, err := svc.Detail(context.Background(), uuid)
	if err != nil {
		t.Fatalf("查 request-log 详情失败: %v", err)
	}
	if detail.Result != "failed" {
		t.Fatalf("子请求失败后 request-log result = %q, 期望 failed", detail.Result)
	}
	if detail.HTTPStatus != 400 {
		t.Fatalf("http_status = %d, 期望 400", detail.HTTPStatus)
	}
	if len(detail.ResponseJSON) == 0 {
		t.Fatal("失败子请求的 response_json 应为空（应含上游错误体）")
	}
	if !strings.Contains(string(detail.ResponseJSON), "content type is not supported") {
		t.Fatalf("response_json 应包含上游错误信息: %s", detail.ResponseJSON)
	}
}
