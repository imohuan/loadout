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
// scopeToChannel 为 true 时把限流规则绑到该渠道（channel_base_urls，界面上「渠道」列选中的值）：
// 覆盖「规则只对某个平台生效」这一形态。
func newGatewayWithLimit(t *testing.T, max, windowSeconds int, onExhausted string, rec *hitRecorder, scopeToChannel ...bool) *modelgateway.Service {
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
	route := types.CapabilityRoute{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit: &types.RateLimitRule{
			MaxRequests:   max,
			WindowSeconds: windowSeconds,
			OnExhausted:   onExhausted,
		},
	}
	// 渠道级约束：把规则绑到上游所在的这个渠道组（界面上的「渠道」列）。
	if len(scopeToChannel) > 0 && scopeToChannel[0] {
		route.ChannelBaseURLs = []string{upstream.URL}
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{route}); err != nil {
		t.Fatal(err)
	}

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	// 与 plugin.go 一致的订阅。
	ctx.On(modelgateway.ProxyChannelsResolved, svc.HandleChannelsResolved)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleBeforeAttempt)
	ctx.On(modelgateway.ProxyAttemptFinished, svc.HandleAttemptFinished)
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

// TestE2EChannelScopedRuleRotatesAccounts 规则绑在渠道上（界面「渠道」列 = channel_base_urls）时
// 账号轮换同样生效。
//
// 回归：候选解析阶段 metadata 里还没有 __current_channel，只用 metadata 解析渠道作用域时
// 这条路由匹配不到，表现为用户看到的「配了限流完全没效果」。
func TestE2EChannelScopedRuleRotatesAccounts(t *testing.T) {
	rec := &hitRecorder{}
	// scopeToChannel=true：规则带 channel_base_urls（界面「渠道」列），走渠道级匹配。
	gw := newGatewayWithLimit(t, 1, 60, types.RateLimitReject, rec, true)

	for i := 0; i < 2; i++ {
		if code, body := doChat(t, gw); code != http.StatusOK {
			t.Fatalf("第 %d 次状态码 = %d，期望 200：%s", i+1, code, body)
		}
	}
	hits := rec.list()
	if len(hits) != 2 || hits[1] != "key-account-b" {
		t.Fatalf("渠道级约束下 A 用满应换到 B，实际 hits=%v", hits)
	}
}

// newGatewayWithConcurrency 组装「两个账号 + 只限并发」的网关。
//
// gatedKeys 里的账号在上游会被阻塞（直到调用方释放），其他账号立即返回——这样才能分别制造
// 「某个账号有一个请求还在途」的状态。返回的 release 用于放行全部被阻塞的请求。
// scopeToChannel 为 true 时把规则绑到该渠道（channel_base_urls = 界面上的「渠道」列）。
func newGatewayWithConcurrency(t *testing.T, maxConcurrency int, onExhausted string, rec *hitRecorder, gatedKeys []string, scopeToChannel ...bool) (gw *modelgateway.Service, release func()) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gates := map[string]chan struct{}{}
	gateMu := sync.Mutex{}
	for _, k := range gatedKeys {
		gates[k] = make(chan struct{})
	}
	// 假上游：被 gate 的账号先阻塞（模拟请求仍在进行中），其余账号立即返回。
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		mu.Lock()
		rec.add(key)
		mu.Unlock()
		gateMu.Lock()
		gate := gates[key]
		gateMu.Unlock()
		if gate != nil {
			<-gate
		}
		rw.Header().Set("Content-Type", "application/json")
		_, _ = rw.Write([]byte(respBody))
	}))
	t.Cleanup(srv.Close)
	var releaseOnce sync.Once
	release = func() {
		releaseOnce.Do(func() {
			gateMu.Lock()
			defer gateMu.Unlock()
			for _, g := range gates {
				close(g)
			}
		})
	}

	cipherA, err := st.Encrypt("key-account-a")
	if err != nil {
		t.Fatal(err)
	}
	cipherB, err := st.Encrypt("key-account-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Write(types.FileChannels, []types.Channel{
		{ID: "keyA", Name: "账号A", BaseURL: srv.URL, APIKeyCipher: cipherA, Enabled: true, ManualEnabled: true},
		{ID: "keyB", Name: "账号B", BaseURL: srv.URL, APIKeyCipher: cipherB, Enabled: true, ManualEnabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	route := types.CapabilityRoute{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit:  &types.RateLimitRule{MaxConcurrency: maxConcurrency, OnExhausted: onExhausted},
	}
	if len(scopeToChannel) > 0 && scopeToChannel[0] {
		route.ChannelBaseURLs = []string{srv.URL}
	}
	if err := st.Write(types.FileCapabilityRoutes, []types.CapabilityRoute{route}); err != nil {
		t.Fatal(err)
	}

	ctx := newMockCtx()
	svc := NewService(st, slog.Default())
	ctx.On(modelgateway.ProxyChannelsResolved, svc.HandleChannelsResolved)
	ctx.On(modelgateway.ProxyBeforeAttempt, svc.HandleBeforeAttempt)
	ctx.On(modelgateway.ProxyAttemptFinished, svc.HandleAttemptFinished)
	return modelgateway.NewService(st, slog.Default(), ctx), release
}

// TestE2EConcurrencyRotatesAccounts 在途并发占满时自动换到另一个账号。
//
// 上游故意阻塞住第一个请求，使账号 A 的并发名额保持在「已占」状态；此时第二个请求
// 必须落到账号 B，而不是排队或 429。
func TestE2EConcurrencyRotatesAccounts(t *testing.T) {
	rec := &hitRecorder{}
	// 只让账号 A 的请求阻塞：A 的名额被占住后，第二个请求应落到 B（B 立即可用）。
	gw, release := newGatewayWithConcurrency(t, 1, types.RateLimitReject, rec, []string{"key-account-a"})

	// 第一个请求：会卡在上游（占用 A 的并发名额）。
	done := make(chan int, 1)
	go func() {
		code, _ := doChat(t, gw)
		done <- code
	}()
	// 等上游确实收到第一个请求（A 的名额已被占住）。
	waitForHits(t, rec, 1)

	// 第二个请求：A 并发已满 → 应落到 B（B 也有请求在途，但 B 名额尚空）。
	code2, body2 := doChat(t, gw)
	if code2 != http.StatusOK {
		t.Fatalf("第二个请求状态码 = %d，期望 200（自动换账号）：%s", code2, body2)
	}
	waitForHits(t, rec, 2)
	hits := rec.list()
	if hits[0] != "key-account-a" || hits[1] != "key-account-b" {
		t.Fatalf("并发占满后应自动换到账号 B，实际 hits=%v", hits)
	}

	release() // 放行上游，让卡住的请求收尾
	<-done
}

// TestE2EConcurrencyRejectsWhenAllBusy 两个账号的并发都占满 → 429，且不再访问上游。
func TestE2EConcurrencyRejectsWhenAllBusy(t *testing.T) {
	rec := &hitRecorder{}
	// 两个账号都阻塞：两个请求各占住一个名额后，第三个请求应被 429 拒绝。
	gw, release := newGatewayWithConcurrency(t, 1, types.RateLimitReject, rec, []string{"key-account-a", "key-account-b"})

	// 两个请求分别占住 A、B 的名额（都卡在上游）。
	doneA := make(chan int, 1)
	go func() { code, _ := doChat(t, gw); doneA <- code }()
	waitForHits(t, rec, 1)
	doneB := make(chan int, 1)
	go func() { code, _ := doChat(t, gw); doneB <- code }()
	waitForHits(t, rec, 2)

	// 第三个请求：两个账号的并发都满 → 429，且不应触达上游。
	code, body := doChat(t, gw)
	if code != http.StatusTooManyRequests {
		t.Fatalf("并发全满时状态码 = %d，期望 429：%s", code, body)
	}
	if !strings.Contains(body, "key_rate_limited") {
		t.Fatalf("429 响应体应包含 key_rate_limited，实际：%s", body)
	}
	if got := len(rec.list()); got != 2 {
		t.Fatalf("429 时不应再访问上游，命中数应为 2，实际 %d", got)
	}

	release()
	<-doneA
	<-doneB
}

// TestE2EChannelScopedConcurrencyRotatesAccounts 规则绑在渠道上时，并发维度的账号轮换同样生效。
//
// 回归：这一形态（界面「渠道」列 + 并发上限）正是用户实测「并发控制完全没效果、
// 全是同一个账号」的场景。渠道级路由在候选阶段匹配不到时，A 的并发名额根本不会被占，
// 第二个请求照样落到 A。
func TestE2EChannelScopedConcurrencyRotatesAccounts(t *testing.T) {
	rec := &hitRecorder{}
	// 只阻塞账号 A：A 的在途名额被占住后，第二个请求应换到 B。
	gw, release := newGatewayWithConcurrency(t, 1, types.RateLimitReject, rec, []string{"key-account-a"}, true)

	done := make(chan int, 1)
	go func() {
		code, _ := doChat(t, gw)
		done <- code
	}()
	waitForHits(t, rec, 1)

	// 第二个请求要有界：若换号没生效，它会落到仍被阻塞的账号 A 上、永久等待。
	// 所以放 goroutine 里等结果，超时即判定「没换号」，而不是把测试挂死。
	second := make(chan int, 1)
	go func() {
		code, _ := doChat(t, gw)
		second <- code
	}()
	select {
	case code := <-second:
		if code != http.StatusOK {
			t.Fatalf("第二个请求状态码 = %d，期望 200（换账号）", code)
		}
	case <-time.After(3 * time.Second):
		release()
		<-done
		t.Fatal("第二个请求仍落到被阻塞的账号 A：渠道级约束的并发限流没有生效（没有换号）")
	}
	waitForHits(t, rec, 2)
	hits := rec.list()
	if hits[0] != "key-account-a" || hits[1] != "key-account-b" {
		t.Fatalf("渠道级约束下并发占满应换到 B，实际 hits=%v", hits)
	}

	release()
	<-done
}

// waitForHits 等待上游命中数达到 want（带超时，避免测试挂死）。
func waitForHits(t *testing.T, rec *hitRecorder, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if len(rec.list()) >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待上游命中 %d 次超时，实际 %d", want, len(rec.list()))
}
