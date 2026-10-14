package keyratelimit

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"loadout/core/plugin"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// mockCtx 最小 plugin.Context：支持 On/Waterfall（与 force-stream 集成测试同构）。
type mockCtx struct {
	handlers map[string][]plugin.Handler
}

func newMockCtx() *mockCtx { return &mockCtx{handlers: map[string][]plugin.Handler{}} }

func (m *mockCtx) Get(name string) any                      { return nil }
func (m *mockCtx) Set(name string, svc any) plugin.Disposer { return func() {} }
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
func (m *mockCtx) Effect(fn func()) plugin.Disposer                    { return func() {} }
func (m *mockCtx) Logger() *slog.Logger                                { return slog.Default() }
func (m *mockCtx) RegisterCheck(name string, fn func() []plugin.Issue) {}
func (m *mockCtx) RegisterRoute(spec plugin.RouteSpec) plugin.Disposer { return func() {} }
func (m *mockCtx) SetRouteLogPresenceHook(install func(fn func(ctx context.Context, ids []string) (map[string]bool, error))) {
}
func (m *mockCtx) InstallRouteLogPresence(fn func(ctx context.Context, ids []string) (map[string]bool, error)) bool {
	return false
}

// respBody 假上游返回的非流式 JSON 响应体。
const respBody = "{\"id\":\"x\",\"object\":\"chat.completion\",\"choices\":[{\"message\":{\"content\":\"ok\"}}]}"

// chatBody 客户端请求体。
const chatBody = "{\"model\":\"gpt-4o\",\"stream\":false,\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"

// hitRecorder 记录每次上游命中的账号密钥（按 Authorization 区分账号）。
type hitRecorder struct {
	mu   sync.Mutex
	hits []string
}

func (h *hitRecorder) add(key string) {
	h.mu.Lock()
	h.hits = append(h.hits, key)
	h.mu.Unlock()
}

func (h *hitRecorder) list() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.hits...)
}

// newTwoAccountUpstream 起一个假上游：按 Authorization 区分两个账号并记录命中顺序。
func newTwoAccountUpstream(t *testing.T, rec *hitRecorder) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		rec.add(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// newGatewayWithLimit 组装「同平台两个账号 + 一条限流规则」的网关。
// max=0 表示规则未配参数（等同不限流）。
func newGatewayWithLimit(t *testing.T, max, windowSeconds int, onExhausted string, rec *hitRecorder) *modelgateway.Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	upstream := newTwoAccountUpstream(t, rec)

	// 同一 base_url 下两个账号（渠道 Key），密钥各不同 —— 走真实加密与解密链路。
	cipherA, err := st.Encrypt("key-account-a")
	if err != nil {
		t.Fatal(err)
	}
	cipherB, err := st.Encrypt("key-account-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "keyA", Name: "账号A", BaseURL: upstream.URL, APIKeyCipher: cipherA, Enabled: true, ManualEnabled: true},
		{ID: "keyB", Name: "账号B", BaseURL: upstream.URL, APIKeyCipher: cipherB, Enabled: true, ManualEnabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit: &types.RateLimitRule{
			MaxRequests:   max,
			WindowSeconds: windowSeconds,
			OnExhausted:   onExhausted,
		},
	}}); err != nil {
		t.Fatal(err)
	}

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	// 与 plugin.go 一致的订阅。
	ctx.On(modelgateway.ProxyChannelsResolved, svc.HandleChannelsResolved)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleBeforeAttempt)
	return modelgateway.NewService(st, slog.Default(), ctx)
}

// doChat 发一次 chat/completions 请求，返回（状态码, 响应体）。
func doChat(t *testing.T, gw *modelgateway.Service) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(chatBody))
	rr := httptest.NewRecorder()
	gw.HandleProxy(rr, req)
	return rr.Code, rr.Body.String()
}

// TestE2ERotatesAccounts Key 用满后请求自动落到另一个账号。
// max=1：第 1 次打账号 A（渠道表顺序第一个），A 用满；第 2 次应落到账号 B。
func TestE2ERotatesAccounts(t *testing.T) {
	rec := &hitRecorder{}
	gw := newGatewayWithLimit(t, 1, 60, types.RateLimitReject, rec)

	for i := 0; i < 2; i++ {
		if code, body := doChat(t, gw); code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 = %d，期望 200：%s", i+1, code, body)
		}
	}
	hits := rec.list()
	if len(hits) != 2 {
		t.Fatalf("应命中上游 2 次，实际 %d（hits=%v）", len(hits), hits)
	}
	if hits[0] != "key-account-a" {
		t.Fatalf("第 1 次应打账号 A，实际 %q", hits[0])
	}
	if hits[1] != "key-account-b" {
		t.Fatalf("A 用满后第 2 次应自动换账号 B，实际 %q", hits[1])
	}
}

// TestE2ERejectsWhenAllExhausted 全部账号用满 → 429，且不再访问上游。
func TestE2ERejectsWhenAllExhausted(t *testing.T) {
	rec := &hitRecorder{}
	gw := newGatewayWithLimit(t, 1, 60, types.RateLimitReject, rec)

	for i := 0; i < 2; i++ {
		if code, body := doChat(t, gw); code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 = %d，期望 200：%s", i+1, code, body)
		}
	}
	before := len(rec.list())
	code, body := doChat(t, gw)
	if code != http.StatusTooManyRequests {
		t.Fatalf("全部用满后状态码 = %d，期望 429：%s", code, body)
	}
	if !strings.Contains(body, "key_rate_limited") {
		t.Fatalf("429 响应体应包含 key_rate_limited 类型，实际：%s", body)
	}
	if got := len(rec.list()); got != before {
		t.Fatalf("429 时不应再访问上游，命中数从 %d 变成 %d", before, got)
	}
}

// TestE2EReuseKeepsServing reuse 策略下全部用满仍继续服务。
func TestE2EReuseKeepsServing(t *testing.T) {
	rec := &hitRecorder{}
	gw := newGatewayWithLimit(t, 1, 60, types.RateLimitReuse, rec)

	for i := 0; i < 4; i++ {
		if code, body := doChat(t, gw); code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 = %d，期望 200（reuse 不应拒绝）：%s", i+1, code, body)
		}
	}
	if got := len(rec.list()); got != 4 {
		t.Fatalf("reuse 策略应每次都访问上游，实际命中 %d 次", got)
	}
}

// TestE2ENoRuleUnaffected 未配参数（max=0）时行为不变：始终用渠道表第一个账号。
func TestE2ENoRuleUnaffected(t *testing.T) {
	rec := &hitRecorder{}
	gw := newGatewayWithLimit(t, 0, 60, types.RateLimitReject, rec)

	for i := 0; i < 3; i++ {
		if code, body := doChat(t, gw); code != http.StatusOK {
			t.Fatalf("第 %d 次请求状态码 = %d，期望 200：%s", i+1, code, body)
		}
	}
	hits := rec.list()
	if len(hits) != 3 {
		t.Fatalf("应命中上游 3 次，实际 %d", len(hits))
	}
	for i, h := range hits {
		if h != "key-account-a" {
			t.Fatalf("未配限流时应始终打第一个账号，第 %d 次打到 %q", i+1, h)
		}
	}
}

// TestE2EWindowExpiryRotatesBack 窗口滑出后第一个账号重新可用。
func TestE2EWindowExpiryRotatesBack(t *testing.T) {
	rec := &hitRecorder{}
	// 窗口 1 秒：短暂等待即可让它滑出，无需注入时钟。
	gw := newGatewayWithLimit(t, 1, 1, types.RateLimitReject, rec)

	if code, body := doChat(t, gw); code != http.StatusOK {
		t.Fatalf("第 1 次状态码 = %d：%s", code, body)
	}
	if code, body := doChat(t, gw); code != http.StatusOK {
		t.Fatalf("第 2 次状态码 = %d：%s", code, body)
	}
	// 等窗口滑出（1 秒窗口 + 余量）。
	time.Sleep(time.Second + 300*time.Millisecond)
	if code, body := doChat(t, gw); code != http.StatusOK {
		t.Fatalf("窗口滑出后状态码 = %d，期望 200（账号重新可用）：%s", code, body)
	}
	hits := rec.list()
	if len(hits) != 3 || hits[2] != "key-account-a" {
		t.Fatalf("窗口滑出后应重新从账号 A 开始，实际 hits=%v", hits)
	}
}

