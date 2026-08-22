package vision

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/db"
	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	modelgateway "loadout/plugins/model-gateway"
	routelog "loadout/plugins/route-log"
	"loadout/plugins/types"
	fakellm "loadout/testkit/fake-llm"
)

// e2eCtx 端到端测试用的最小事件上下文：支持 Waterfall/On，其余方法空实现。
type e2eCtx struct {
	handlers map[string][]plugin.Handler
}

func newE2ECtx() *e2eCtx { return &e2eCtx{handlers: map[string][]plugin.Handler{}} }

func (m *e2eCtx) Get(name string) any                          { return nil }
func (m *e2eCtx) Set(name string, svc any) plugin.Disposer     { return func() {} }
func (m *e2eCtx) Effect(fn func()) plugin.Disposer             { return func() {} }
func (m *e2eCtx) Logger() *slog.Logger                         { return slog.Default() }
func (m *e2eCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *e2eCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer {
	return func() {}
}
func (m *e2eCtx) Emit(event string, payload any) {}
func (m *e2eCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}
func (m *e2eCtx) Waterfall(event string, payload any) (any, error) {
	var err error
	for _, h := range m.handlers[event] {
		payload, err = h(payload)
		if err != nil {
			return payload, err
		}
	}
	return payload, nil
}

// e2eHealth 让渠道解析全部可用。
type e2eHealth struct{}

func (e2eHealth) Check(context.Context, string, string) (contracts.Availability, error) {
	return contracts.Availability{ManualEnabled: true, HealthStatus: "available", EffectiveAvailable: true}, nil
}
func (e2eHealth) RecordSuccess(context.Context, string, string) error { return nil }
func (e2eHealth) RecordFailure(context.Context, contracts.RouteFailure) (string, error) {
	return "", nil
}
func (e2eHealth) SetChannelEnabled(context.Context, string, bool) error { return nil }
func (e2eHealth) SetModelEnabled(context.Context, string, string, bool) error {
	return nil
}
func (e2eHealth) SetModelsEnabled(context.Context, string, []string, bool) error { return nil }
func (e2eHealth) DeleteModel(context.Context, string, string) error          { return nil }
func (e2eHealth) DeleteModels(context.Context, string, []string) error       { return nil }
func (e2eHealth) RecoverChannel(context.Context, string) error              { return nil }
func (e2eHealth) RecoverModel(context.Context, string, string) error        { return nil }
func (e2eHealth) RecoverModels(context.Context, string, []string) error     { return nil }
func (e2eHealth) RecoverAllModels(context.Context) (int64, error)           { return 0, nil }
func (e2eHealth) RecoverAllModelsByChannel(context.Context, string) (int64, error) {
	return 0, nil
}
func (e2eHealth) RecoverAllChannels(context.Context) (int64, error) { return 0, nil }
func (e2eHealth) List(context.Context) ([]contracts.ChannelStatus, error) {
	return nil, nil
}
func (e2eHealth) CheckNow(context.Context, bool) error { return nil }
func (e2eHealth) PurgeChannelStates(context.Context, string, []string) error { return nil }

// newE2EGateway 构造完整链路：model-gateway + 真实 vision hook + 真实 route-log。
// 返回 gateway、route-log、事件上下文。
// database 由调用方传入并复用：测试主体往该库写入渠道/能力路由，网关/route-log
// 也绑定同一库——旧实现内部 db.OpenMemory() 另建空库，导致主链路 routing 读不到
// 渠道（TestVisionE2EFlushOnSuccess 稳定 502 no_available_channel）。
func newE2EGateway(t *testing.T, visSvc *Service, database *sql.DB) (*modelgateway.Service, *routelog.Service, *e2eCtx) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	ectx := newE2ECtx()
	mgw := modelgateway.NewService(st, slog.Default(), ectx)

	rl := routelog.NewService(database, slog.Default())
	mgw.SetRoutingServices(database, e2eHealth{}, rl)
	visSvc.SetRouteLog(rl)

	ectx.On(modelgateway.ProxyBeforeUpstream, visSvc.HandleProxyBeforeUpstream)
	return mgw, rl, ectx
}

// doProxy 打到 HandleProxy 的简化 helper（request_id 走中间件逻辑之外，网关自行兜底）。
func doProxyE2E(t *testing.T, mgw *modelgateway.Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	mgw.HandleProxy(rr, req)
	return rr
}

// TestVisionE2EFlushOnSuccess 端到端：含图请求 → 真实 vision 插件识别成功 →
// model-gateway flush 视觉 attempt（step_no=-1）→ 主链路 attempt（step_no=1）。
func TestVisionE2EFlushOnSuccess(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetResponse(`{"choices":[{"message":{"role":"assistant","content":"这是一只猫"}}]}`)

	visSvc, _ := newTestService(t)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	// vision Service 用同一 repo（渠道/路由与主网关同源）。
	visSvc = NewService(visSvc.st, repo, slog.Default())
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "vision", Name: "视觉", BaseURL: url + "/v1", ManualEnabled: true, Models: []db.ChannelModel{{Model: "qwen-vl-max", Enabled: true}}},
		{ID: "main", Name: "主模型", BaseURL: url + "/v1", ManualEnabled: true, Models: []db.ChannelModel{{Model: "deepseek-chat", Enabled: true}}},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	if err := repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	mgw, rl, _ := newE2EGateway(t, visSvc, database)
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	rr := doProxyE2E(t, mgw, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("应转发成功，实际 %d: %s", rr.Code, rr.Body.String())
	}

	page, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("应有 1 条请求日志，实际 %d", len(page.Items))
	}
	detail, err := rl.Detail(context.Background(), page.Items[0].RequestID)
	if err != nil {
		t.Fatalf("查询详情失败: %v", err)
	}
	var visionSeen, mainSeen bool
	for _, a := range detail.Attempts {
		switch a.StepNo {
		case "1":
			visionSeen = true
			if a.Action != "视觉识别" || a.Model != "qwen-vl-max" || a.ChannelID != "vision" || a.Result != "success" {
				t.Fatalf("视觉 attempt 内容不符: %+v", a)
			}
		case "2":
			mainSeen = true
			if a.Action != "首次尝试" || a.Model != "deepseek-chat" {
				t.Fatalf("主链路 attempt 不符: %+v", a)
			}
		}
	}
	if !visionSeen || !mainSeen {
		t.Fatalf("缺少 attempt（vision=%v main=%v）: %+v", visionSeen, mainSeen, detail.Attempts)
	}
}

// TestVisionE2EFlushOnFail 端到端：视觉模型全部失败 → hook 拒绝请求 →
// proxyRejectedLog 仍 flush 失败的视觉 attempt，请求最终标记 failed。
func TestVisionE2EFlushOnFail(t *testing.T) {
	fake, url := fakellm.New()
	defer fake.Close()
	fake.SetError(http.StatusBadGateway, `{"error":{"message":"down","type":"upstream_error"}}`)

	visSvc, _ := newTestService(t)
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("db.OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("db.NewRepository: %v", err)
	}
	visSvc = NewService(visSvc.st, repo, slog.Default())
	if err := repo.ReplaceChannels(context.Background(), []db.Channel{
		{ID: "vision", Name: "视觉", BaseURL: url + "/v1", ManualEnabled: true, Models: []db.ChannelModel{{Model: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	if err := repo.ReplaceCapabilityRoutes(context.Background(), []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
	}); err != nil {
		t.Fatalf("写能力路由失败: %v", err)
	}

	mgw, rl, _ := newE2EGateway(t, visSvc, database)
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":[{"type":"text","text":"看"},{"type":"image_url","image_url":{"url":"http://img/a.png"}}]}]}`
	rr := doProxyE2E(t, mgw, body)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("视觉失败应拒绝（400），实际 %d", rr.Code)
	}

	page, err := rl.List(context.Background(), contracts.RouteLogFilter{})
	if err != nil {
		t.Fatalf("查询 route log 失败: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].Result != "failed" {
		t.Fatalf("应有 1 条 failed 请求日志，实际 %+v", page.Items)
	}
	detail, err := rl.Detail(context.Background(), page.Items[0].RequestID)
	if err != nil {
		t.Fatalf("查询详情失败: %v", err)
	}
	var visionSeen bool
	for _, a := range detail.Attempts {
		if a.StepNo == "1" {
			visionSeen = true
			if a.Action != "视觉识别" || a.Model != "qwen-vl-max" || a.Result != "failed" || a.ErrorMessage == "" {
				t.Fatalf("视觉失败 attempt 内容不符: %+v", a)
			}
		}
	}
	if !visionSeen {
		t.Fatalf("缺少视觉失败 attempt: %+v", detail.Attempts)
	}
}
