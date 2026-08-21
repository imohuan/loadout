package modelgateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"loadout/core/plugin"
	"loadout/core/store"
	"loadout/plugins/contracts"
	gatewaykeys "loadout/plugins/gateway-keys"
	"loadout/plugins/types"
)

// mockCtx 测试用 plugin.Context：仅实现事件总线，其余方法为占位。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

// newMockCtx 创建空事件总线的 mock 上下文。
func newMockCtx() *mockCtx {
	return &mockCtx{handlers: map[string][]plugin.Handler{}}
}

func (m *mockCtx) Get(name string) any { return nil }

func (m *mockCtx) Set(name string, svc any) plugin.Disposer { return func() {} }

func (m *mockCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}

func (m *mockCtx) Emit(event string, payload any) {}

func (m *mockCtx) Waterfall(event string, payload any) (any, error) {
	for _, h := range m.handlers[event] {
		var err error
		payload, err = h(payload)
		if err != nil {
			return payload, err
		}
	}
	return payload, nil
}

func (m *mockCtx) Effect(fn func()) plugin.Disposer { return func() {} }

func (m *mockCtx) Logger() *slog.Logger { return slog.Default() }

func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}

func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }

// newTestService 用临时目录建 Store 与 Service，供测试复用。
func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default(), newMockCtx()), st
}

// writeTestChannel 写入一条指向 fake-llm 的渠道记录。
func writeTestChannel(t *testing.T, st *store.Store, baseURL string) {
	t.Helper()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "test", Name: "测试渠道", BaseURL: baseURL, Enabled: true},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
}

func TestRouteCapability(t *testing.T) {
	svc, st := newTestService(t)
	routes := []types.CapabilityRoute{
		{Models: []string{"deepseek-chat"}, Capability: "vision", Route: types.RouteProxy, ViaOptions: []types.ViaOption{{ViaModel: "qwen-vl-max"}}},
		{Models: []string{"gpt-4o-mini"}, Capability: "vision", Route: types.RouteNative},
		{Models: []string{"deepseek-*"}, Capability: "vision", Route: types.RouteError},
		// 渠道约束：仅 ch-b 上的 gpt-4o 命中 native；ch-a 上的 gpt-4o 不命中（全渠道无约束路由时）。
		{Models: []string{"gpt-4o"}, ChannelIDs: []string{"ch-b"}, Capability: "vision", Route: types.RouteNative},
		// 通用全匹配：* 渠道对任何渠道（含未知渠道）都命中。
		{Models: []string{"claude-3"}, ChannelIDs: []string{"*"}, Capability: "vision", Route: types.RouteProxy},
	}
	if err := st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写能力路由表失败: %v", err)
	}

	hit, err := svc.RouteCapability("deepseek-chat", "vision", "")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if hit == nil || hit.Route != types.RouteProxy || len(hit.ViaOptions) == 0 || hit.ViaOptions[0].ViaModel != "qwen-vl-max" {
		t.Fatalf("应命中 proxy 路由: %+v", hit)
	}

	// 通配符前缀匹配
	wild, err := svc.RouteCapability("deepseek-v4-flash", "vision", "")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if wild == nil || wild.Route != types.RouteError {
		t.Fatalf("deepseek-v4-flash 应命中 deepseek-* 通配: %+v", wild)
	}

	miss, err := svc.RouteCapability("deepseek-chat", "tts", "")
	if err != nil {
		t.Fatalf("未命中不应报错: %v", err)
	}
	if miss != nil {
		t.Fatalf("未命中应返回 nil，实际 %+v", miss)
	}

	// 渠道约束：命中渠道命中，非命中渠道不命中。
	chHit, err := svc.RouteCapability("gpt-4o", "vision", "ch-b")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if chHit == nil || chHit.Route != types.RouteNative {
		t.Fatalf("ch-b 上的 gpt-4o 应命中渠道约束路由: %+v", chHit)
	}
	chMiss, err := svc.RouteCapability("gpt-4o", "vision", "ch-a")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if chMiss != nil {
		t.Fatalf("ch-a 上的 gpt-4o 不应命中 ch-b 约束路由，实际 %+v", chMiss)
	}
	// 渠道未知（空）时约束路由不命中，避免误伤。
	unknownMiss, err := svc.RouteCapability("gpt-4o", "vision", "")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if unknownMiss != nil {
		t.Fatalf("渠道未知不应命中约束路由，实际 %+v", unknownMiss)
	}

	// 通用全匹配：任何渠道（含未知）都命中。
	starHit, err := svc.RouteCapability("claude-3", "vision", "")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if starHit == nil || starHit.Route != types.RouteProxy {
		t.Fatalf("claude-3 应命中 * 全匹配渠道路由: %+v", starHit)
	}
	starHitCh, err := svc.RouteCapability("claude-3", "vision", "ch-z")
	if err != nil {
		t.Fatalf("RouteCapability 出错: %v", err)
	}
	if starHitCh == nil {
		t.Fatalf("claude-3 在任意渠道应命中 * 路由")
	}
}

func TestRouteCapabilityNoFile(t *testing.T) {
	svc, _ := newTestService(t)
	got, err := svc.RouteCapability("m", "c", "")
	if err != nil {
		t.Fatalf("无路由表不应报错: %v", err)
	}
	if got != nil {
		t.Fatalf("无路由表应返回 nil，实际 %+v", got)
	}
}

func TestResolveChannelsForModel(t *testing.T) {
	_, st := newTestService(t)
	cipher, err := st.Encrypt("sk-secret-123")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "未知渠道", BaseURL: "http://u/v1", Enabled: true},
		{ID: "c2", Name: "已知渠道", BaseURL: "http://k/v1", APIKeyCipher: cipher, Enabled: true, Models: []string{"gpt-4o", "deepseek-chat"}},
		{ID: "c3", Name: "无关渠道", BaseURL: "http://o/v1", Enabled: true, Models: []string{"claude-x"}},
		{ID: "c4", Name: "禁用渠道", BaseURL: "http://d/v1", Enabled: false, Models: []string{"deepseek-chat"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}

	got, err := ResolveChannelsForModel(st, "deepseek-chat")
	if err != nil {
		t.Fatalf("ResolveChannelsForModel: %v", err)
	}
	// 已知支持（c2）在前，未知兜底（c1）在后；c3（不支持）、c4（禁用）排除。
	if len(got) != 2 {
		t.Fatalf("应返回 2 个候选，实际 %d: %+v", len(got), got)
	}
	if got[0].ID != "c2" || got[1].ID != "c1" {
		t.Fatalf("顺序应为 c2,c1，实际 %q,%q", got[0].ID, got[1].ID)
	}
	if got[0].APIKey != "sk-secret-123" {
		t.Fatalf("应解密出明文 key，实际 %q", got[0].APIKey)
	}

	// 没有任何渠道支持时返回空。
	none, err := ResolveChannelsForModel(st, "unknown-model")
	if err != nil {
		t.Fatalf("ResolveChannelsForModel: %v", err)
	}
	if len(none) != 1 || none[0].ID != "c1" {
		t.Fatalf("未知模型应只有未知渠道兜底，实际 %+v", none)
	}
}

func TestHandleModelsWithAggregate(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", BaseURL: "http://u/v1", Enabled: true, Models: []string{"model-a", "model-b"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	if err := st.Write(types.FileAggregates, []types.Aggregate{
		{Name: "auto", Models: []string{"model-a", "model-b"}},
	}); err != nil {
		t.Fatalf("写聚合表失败: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	svc.HandleModels(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码应为 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("解析响应失败: %v", err)
	}
	ids := map[string]bool{}
	for _, d := range parsed.Data {
		ids[d.ID] = true
	}
	if !ids["model-a"] || !ids["model-b"] || !ids["auto"] {
		t.Fatalf("模型列表应包含 model-a、model-b、auto，实际 %+v", ids)
	}
}

func TestHandleModelsKeyRestriction(t *testing.T) {
	svc, st := newTestService(t)
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "c1", Name: "渠道1", BaseURL: "http://u/v1", Enabled: true, Models: []string{"model-a", "model-b", "model-c"}},
	}); err != nil {
		t.Fatalf("写渠道表失败: %v", err)
	}
	if err := st.Write(types.FileAggregates, []types.Aggregate{
		{Name: "auto", Models: []string{"model-a", "model-b"}},
	}); err != nil {
		t.Fatalf("写聚合表失败: %v", err)
	}

	list := func(keyModels []string) map[string]bool {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req = req.WithContext(gatewaykeys.ContextWithAPIKey(req.Context(), types.APIKey{Models: keyModels}))
		rec := httptest.NewRecorder()
		svc.HandleModels(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("状态码应为 200，实际 %d: %s", rec.Code, rec.Body.String())
		}
		var parsed struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &parsed); err != nil {
			t.Fatalf("解析响应失败: %v", err)
		}
		ids := map[string]bool{}
		for _, d := range parsed.Data {
			ids[d.ID] = true
		}
		return ids
	}

	// key 只允许 model-a → 虚拟模型 auto 也需被允许，这里不应出现。
	ids := list([]string{"model-a"})
	if !ids["model-a"] || ids["model-b"] || ids["model-c"] || ids["auto"] {
		t.Fatalf("白名单 [model-a] 应只返回 model-a，实际 %+v", ids)
	}

	// key 同时允许真实模型与虚拟模型 → 都返回。
	ids = list([]string{"model-a", "auto"})
	if !ids["model-a"] || !ids["auto"] || ids["model-b"] || ids["model-c"] {
		t.Fatalf("白名单 [model-a, auto] 应返回 model-a 与 auto，实际 %+v", ids)
	}

	// 无限制（["*"]）→ 全部返回。
	ids = list([]string{"*"})
	if len(ids) != 4 || !ids["model-a"] || !ids["model-b"] || !ids["model-c"] || !ids["auto"] {
		t.Fatalf("无限制 key 应返回 4 个模型，实际 %+v", ids)
	}
}

func TestMessageContentUnmarshal(t *testing.T) {
	var req struct {
		Messages []ChatMessage `json:"messages"`
	}
	body := `{"messages":[{"role":"user","content":"纯文本"},{"role":"user","content":[{"type":"text","text":"分段文字"},{"type":"image_url","image_url":{"url":"http://img/x.png"}}]}]}`
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if req.Messages[0].Content.Text != "纯文本" {
		t.Fatalf("字符串 content 未解析: %+v", req.Messages[0].Content)
	}
	c := req.Messages[1].Content
	if len(c.Parts) != 2 {
		t.Fatalf("数组 content 应解析出 2 段: %+v", c)
	}
	if c.Parts[1].Type != "image_url" || c.Parts[1].ImageURL != "http://img/x.png" {
		t.Fatalf("image_url 段解析错误: %+v", c.Parts[1])
	}
}

// mockRouteLog 记录 Start/Attempt/Finish 调用，验证前置拒绝路径是否写入路由日志。
type mockRouteLog struct {
	starts   []contracts.RouteRequest
	attempts []contracts.RouteAttempt
	finishs  []contracts.RouteFinish
}

func (m *mockRouteLog) Start(ctx context.Context, r contracts.RouteRequest) error {
	m.starts = append(m.starts, r)
	return nil
}
func (m *mockRouteLog) Attempt(ctx context.Context, a contracts.RouteAttempt) (int64, error) {
	m.attempts = append(m.attempts, a)
	return int64(len(m.attempts)), nil
}
func (m *mockRouteLog) Finish(ctx context.Context, f contracts.RouteFinish) error {
	m.finishs = append(m.finishs, f)
	return nil
}
func (m *mockRouteLog) List(ctx context.Context, f contracts.RouteLogFilter) ([]contracts.RouteRequestView, error) {
	return nil, nil
}
func (m *mockRouteLog) Detail(ctx context.Context, id string) (contracts.RouteRequestView, error) {
	return contracts.RouteRequestView{}, nil
}
func (m *mockRouteLog) Clear(ctx context.Context, t time.Time) error { return nil }
func (m *mockRouteLog) SelfHeal(ctx context.Context, requestID string, threshold time.Duration) error {
	return nil
}

// TestHandleProxyRejectedBeforeUpstreamWritesLog 回归：before-upstream 前置拒绝
// （聚合模型"无可用目标"、熔断拦截等）必须写入 failed 路由日志，不能无声消失。
// 当聚合目标的列表存在于 metadata 时，每个目标应作为 skipped attempt 写入，
// 让用户在同一条日志下看到完整的候选清单。
func TestHandleProxyRejectedBeforeUpstreamWritesLog(t *testing.T) {
	svc, _ := newTestService(t)
	log := &mockRouteLog{}
	svc.SetRoutingServices(nil, nil, log)

	ctx := newMockCtx()
	ctx.On(ProxyBeforeUpstream, func(payload any) (any, error) {
		pipe := payload.(*ProxyPipeline)
		// 模拟 aggregate 设置了 metadata 后再返回 error
		pipe.Metadata = map[string]any{
			"__virtual_model": "auto-demo",
			"__aggregate_targets": []types.AggregateTarget{
				{Model: "model-x", ChannelID: "ch-x"},
				{Model: "model-y", ChannelID: "ch-y"},
				{Model: "model-z", ChannelID: "ch-z"},
			},
		}
		return nil, &GatewayError{
			Status: http.StatusServiceUnavailable,
			Type:   "no_available_model",
			Msg:    `聚合模型 "auto-demo" 的所有目标当前不可用`,
		}
	})
	svc.ctx = ctx

	body := `{"model":"auto-demo","input":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	rec := httptest.NewRecorder()
	svc.HandleProxy(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码应为 503，实际 %d: %s", rec.Code, rec.Body.String())
	}
	// Start 两次：hook 之前写占位（running）+ proxyRejectedLog 幂等补全（UPSERT 合并为同一条）。
	if len(log.starts) != 2 {
		t.Fatalf("应写入 2 条 start（占位 + 补全），实际 %d", len(log.starts))
	}
	if len(log.finishs) != 1 {
		t.Fatalf("应写入 1 条 finish，实际 %d", len(log.finishs))
	}
	if log.finishs[0].Result != "failed" {
		t.Fatalf("finish result 应为 failed，实际 %q", log.finishs[0].Result)
	}
	if log.finishs[0].HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("finish http_status 应为 503，实际 %d", log.finishs[0].HTTPStatus)
	}
	if !strings.Contains(log.finishs[0].ErrorMessage, "所有目标当前不可用") {
		t.Fatalf("finish error_message 应包含拒绝原因，实际 %q", log.finishs[0].ErrorMessage)
	}
	// 虚拟模型在 hook 之后的第二次 Start（补全）里带出；占位 start 是原始模型。
	if log.starts[1].RequestedModel != "auto-demo" {
		t.Fatalf("补全 start requested_model 应为 auto-demo，实际 %q", log.starts[1].RequestedModel)
	}
	if log.starts[1].VirtualModel != "auto-demo" {
		t.Fatalf("补全 start virtual_model 应为 auto-demo，实际 %q", log.starts[1].VirtualModel)
	}
	// 不可用时把"最后一个候选目标"写入 final_model/final_channel_id，与"最后真实尝试"语义一致
	if log.finishs[0].FinalModel != "model-z" {
		t.Fatalf("finish final_model 应为最后一个目标 model-z，实际 %q", log.finishs[0].FinalModel)
	}
	if log.finishs[0].FinalChannelID != "ch-z" {
		t.Fatalf("finish final_channel_id 应为最后一个目标 ch-z，实际 %q", log.finishs[0].FinalChannelID)
	}
	// 3 个目标应各写一条 skipped attempt
	if len(log.attempts) != 3 {
		t.Fatalf("应写入 3 条 skipped attempt，实际 %d", len(log.attempts))
	}
	wantModels := []string{"model-x", "model-y", "model-z"}
	for i, attempt := range log.attempts {
		if attempt.Result != "skipped" {
			t.Fatalf("attempt[%d] result 应为 skipped，实际 %q", i, attempt.Result)
		}
		if attempt.FailureClass != "no_available" {
			t.Fatalf("attempt[%d] failure_class 应为 no_available，实际 %q", i, attempt.FailureClass)
		}
		if attempt.Model != wantModels[i] {
			t.Fatalf("attempt[%d] model 应为 %s，实际 %q", i, wantModels[i], attempt.Model)
		}
		if attempt.Action != "首次尝试" && attempt.Action != "切换模型" {
			t.Fatalf("attempt[%d] action 应为首次尝试/切换模型，实际 %q", i, attempt.Action)
		}
	}
	if log.attempts[1].Action != "切换模型" {
		t.Fatalf("第 2 步 action 应为切换模型，实际 %q", log.attempts[1].Action)
	}
	// 响应头应 echo X-Request-Id，客户端重试时复用同一值即可合并日志
	requestID := rec.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatalf("响应头 X-Request-Id 应存在，实际为空")
	}
	if requestID != log.starts[0].RequestID {
		t.Fatalf("响应头 X-Request-Id 与日志 request_id 不一致：header=%q log=%q", requestID, log.starts[0].RequestID)
	}
}
