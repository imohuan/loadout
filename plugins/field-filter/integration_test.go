package fieldfilter

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// mockCtx 最小 plugin.Context：支持 On/Waterfall，其余方法空实现。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

func newMockCtx() *mockCtx { return &mockCtx{handlers: map[string][]plugin.Handler{}} }

func (m *mockCtx) Get(name string) any                            { return nil }
func (m *mockCtx) Set(name string, svc any) plugin.Disposer       { return func() {} }
func (m *mockCtx) On(event string, h plugin.Handler) plugin.Disposer {
	m.handlers[event] = append(m.handlers[event], h)
	return func() {}
}
func (m *mockCtx) Emit(event string, payload any) {
	for _, h := range m.handlers[event] {
		_, _ = h(payload)
	}
}
func (m *mockCtx) Waterfall(event string, payload any) (any, error) {
	cur := payload
	for _, h := range m.handlers[event] {
		next, err := h(cur)
		if err != nil {
			return nil, err
		}
		cur = next
	}
	return cur, nil
}
func (m *mockCtx) Effect(fn func()) plugin.Disposer                  { return func() {} }
func (m *mockCtx) Logger() *slog.Logger                              { return slog.Default() }
func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }

// TestFieldFilterE2E 完整链路：echo 上游 + 渠道 + field_filter 路由，
// 验证请求方向剔除与响应方向剔除都经 HandleProxy 生效，且未配置的字段原样透传。
func TestFieldFilterE2E(t *testing.T) {
	// 1) 测试 store + 渠道 + 能力路由
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var gotBody string
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Server-Extra", "secret")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"total_tokens":7}}`))
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{{
		Models:     []string{"gpt-4o"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		FieldRules: &types.FieldRules{
			RequestStrip:  []string{"client_metadata"},
			ResponseStrip: []string{"usage"},
			ResponseHeaderStrip:   []string{"X-Server-Extra"},
		},
	}}); err != nil {
		t.Fatal(err)
	}

	// 2) mock ctx 注册 field-filter hook + modelgateway 服务
	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	gw := modelgateway.NewService(st, slog.Default(), ctx)

	// 3) 请求带 client_metadata → 上游收到的 body 无该字段；响应剔除 usage 与 header
	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"client_metadata":{"app":"codex"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(gotBody, "client_metadata") {
		t.Fatalf("上游收到未剔除的 client_metadata: %s", gotBody)
	}
	if !strings.Contains(gotBody, `"messages"`) {
		t.Fatalf("上游 body 不应丢 messages: %s", gotBody)
	}
	if strings.Contains(rr.Body.String(), "usage") {
		t.Fatalf("响应未剔除 usage: %s", rr.Body.String())
	}
	if rr.Header().Get("X-Server-Extra") != "" {
		t.Fatalf("响应头 X-Server-Extra 未剔除")
	}
	if !strings.Contains(rr.Body.String(), "ok") {
		t.Fatalf("响应内容缺失: %s", rr.Body.String())
	}
}

// TestFieldFilterE2ENoRoute 未配置 field_filter 路由的模型：全程原样透传，
// 不破坏透明代理语义。
func TestFieldFilterE2ENoRoute(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var gotBody string
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer echo.Close()
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "echo", Name: "回显", BaseURL: echo.URL, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	// 不写 field_filter 路由

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyBeforeUpstream, svc.HandleProxyBeforeUpstream)
	ctx.On(modelgateway.ProxyAfterUpstream, svc.HandleProxyAfterUpstream)
	gw := modelgateway.NewService(st, slog.Default(), ctx)

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"client_metadata":{"app":"codex"}}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("状态码 = %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(gotBody, "client_metadata") {
		t.Fatalf("未配置路由应原样透传 client_metadata: %s", gotBody)
	}
}
