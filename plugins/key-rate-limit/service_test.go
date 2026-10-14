package keyratelimit

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

// newTestService 用临时目录建 Store 的 Service。
func newTestService(t *testing.T) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	return NewService(st, slog.Default())
}

// writeRoutes 把能力路由写入 JSON 数据源（无 SQLite 时的回退路径）。
func writeRoutes(t *testing.T, svc *Service, routes []types.CapabilityRoute) {
	t.Helper()
	if err := svc.st.Write(types.FileCapabilityRoutes, routes); err != nil {
		t.Fatalf("写入能力路由失败: %v", err)
	}
}

// testPipe 构造一个最小代理管线（Model + Metadata）。
func testPipe(model string) *modelgateway.ProxyPipeline {
	return &modelgateway.ProxyPipeline{
		RequestID: "req-test",
		Request:   &modelgateway.ProxyRequest{Model: model, Path: "chat/completions"},
		Metadata:  map[string]any{},
	}
}

// channels 构造候选渠道列表。
func channels(ids ...string) []modelgateway.ResolvedChannel {
	out := make([]modelgateway.ResolvedChannel, 0, len(ids))
	for _, id := range ids {
		out = append(out, modelgateway.ResolvedChannel{ID: id, Name: "key-" + id, BaseURL: "https://api.example.com/v1"})
	}
	return out
}

// rateLimitRoute 构造一条 key_rate_limit proxy 路由。
func rateLimitRoute(max, windowSeconds int, onExhausted string, models ...string) types.CapabilityRoute {
	if len(models) == 0 {
		models = []string{"*"}
	}
	return types.CapabilityRoute{
		Models:     models,
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit: &types.RateLimitRule{
			MaxRequests:   max,
			WindowSeconds: windowSeconds,
			OnExhausted:   onExhausted,
		},
	}
}

// resolve 跑一次候选筛选事件，返回（改写后的候选, err）。
func resolve(t *testing.T, svc *Service, pipe *modelgateway.ProxyPipeline, model string, in []modelgateway.ResolvedChannel) ([]modelgateway.ResolvedChannel, error) {
	t.Helper()
	out, err := svc.HandleChannelsResolved(&modelgateway.ChannelCandidatesPayload{Pipe: pipe, Model: model, Candidates: in})
	if err != nil {
		return nil, err
	}
	p, ok := out.(*modelgateway.ChannelCandidatesPayload)
	if !ok || p == nil {
		return nil, fmt.Errorf("返回载荷类型异常: %T", out)
	}
	return p.Candidates, nil
}

// TestNoRouteNoLimit 未配任何规则时原样放行。
func TestNoRouteNoLimit(t *testing.T) {
	svc := newTestService(t)
	in := channels("a", "b")
	got, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", in)
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("未配规则应原样返回 2 个候选，实际 %d", len(got))
	}
}

// TestNativeRouteExempts 命中 native 路由时豁免（不限流）。
func TestNativeRouteExempts(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteNative,
		RateLimit:  &types.RateLimitRule{MaxRequests: 1, WindowSeconds: 60},
	}})
	pipe := testPipe("gpt-4o")
	// 先消费若干次，再验证仍不受限。
	for i := 0; i < 5; i++ {
		_, _ = svc.HandleBeforeAttempt(pipe)
	}
	got, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("native 豁免不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("native 豁免时不应剔除候选，实际剩 %d", len(got))
	}
}

// TestRotateToNextKey 单个 Key 用满后自动换下一个账号。
func TestRotateToNextKey(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(2, 60, types.RateLimitReject)})

	pipe := testPipe("gpt-4o")
	// 第 1 次：两个候选都可用，取第一个（a）并消费。
	got, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("第 1 次 resolve 报错: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" {
		t.Fatalf("第 1 次应保留 [a b]，实际 %v", idsOf(got))
	}
	pipe.Metadata["__current_channel"] = "a"
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatalf("消费 a 失败: %v", err)
	}

	// 第 2 次：a 还剩 1 次，仍可用。
	got, err = resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("第 2 次 resolve 报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("a 未用满时应保留两个候选，实际 %v", idsOf(got))
	}
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatalf("第 2 次消费 a 失败: %v", err)
	}

	// 第 3 次：a 已用满 2 次 → 被剔除，只剩 b（自动换账号）。
	got, err = resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("a 用满后 resolve 不应报错: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("a 用满后应只剩 b，实际 %v", idsOf(got))
	}
}

// TestAllExhaustedRejects 全部账号用满时按默认策略返回 429。
func TestAllExhaustedRejects(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(1, 60, types.RateLimitReject)})

	pipe := testPipe("gpt-4o")
	for _, id := range []string{"a", "b"} {
		pipe.Metadata["__current_channel"] = id
		if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
			t.Fatalf("消费 %s 失败: %v", id, err)
		}
	}
	_, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err == nil {
		t.Fatal("全部用满且 strategy=reject 时应返回错误")
	}
	gw, ok := err.(*modelgateway.GatewayError)
	if !ok {
		t.Fatalf("错误应为 *GatewayError，实际 %T", err)
	}
	if gw.Status != http.StatusTooManyRequests {
		t.Fatalf("状态码应为 429，实际 %d", gw.Status)
	}
	if gw.Type != "key_rate_limited" {
		t.Fatalf("错误类型应为 key_rate_limited，实际 %q", gw.Type)
	}
}

// TestAllExhaustedReuse 全部账号用满时按 reuse 策略继续使用。
func TestAllExhaustedReuse(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(1, 60, types.RateLimitReuse)})

	pipe := testPipe("gpt-4o")
	for _, id := range []string{"a", "b"} {
		pipe.Metadata["__current_channel"] = id
		if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
			t.Fatalf("消费 %s 失败: %v", id, err)
		}
	}
	got, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("reuse 策略不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("reuse 策略应原样返回全部候选，实际 %v", idsOf(got))
	}
}

// TestModelScopeIsolation 单模型规则不影响其他模型。
func TestModelScopeIsolation(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(1, 60, types.RateLimitReject, "gpt-4o")})

	pipe := testPipe("gpt-4o")
	pipe.Metadata["__current_channel"] = "a"
	if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
		t.Fatalf("消费失败: %v", err)
	}

	// 目标模型：a 已用满 → 只剩 b。
	got, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("resolve 报错: %v", err)
	}
	if len(got) != 1 || got[0].ID != "b" {
		t.Fatalf("gpt-4o 下 a 应用满被剔除，实际 %v", idsOf(got))
	}

	// 其他模型：不受该规则约束。
	other := testPipe("claude-3")
	got, err = resolve(t, svc, other, "claude-3", channels("a", "b"))
	if err != nil {
		t.Fatalf("其他模型 resolve 报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("其他模型不应受限，实际 %v", idsOf(got))
	}
}

// TestMissingParamsNotLimited 配了 proxy 路由但没填参数 → 不限流。
func TestMissingParamsNotLimited(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit:  &types.RateLimitRule{MaxRequests: 0},
	}})
	pipe := testPipe("gpt-4o")
	pipe.Metadata["__current_channel"] = "a"
	for i := 0; i < 50; i++ {
		if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
			t.Fatalf("消费失败: %v", err)
		}
	}
	got, err := resolve(t, svc, pipe, "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("未配参数不应报错: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("未配参数不应剔除候选，实际 %v", idsOf(got))
	}
}

// TestWindowExpiryRestoresKey 窗口滑出后 Key 自动重新可用。
func TestWindowExpiryRestoresKey(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(1, 60, types.RateLimitReject)})

	// 直接面向计数器验证「窗口滑出即恢复」（避免测试里真的睡 60 秒）。
	now := time.Now()
	rules, exempt := svc.activeRules([]*types.CapabilityRoute{{
		Models: []string{"*"}, Capability: capabilityName, Route: types.RouteProxy,
		RateLimit: &types.RateLimitRule{MaxRequests: 1, WindowSeconds: 60},
	}})
	if exempt || len(rules) != 1 {
		t.Fatalf("应解析出 1 条生效规则，实际 %d（exempt=%v）", len(rules), exempt)
	}
	r := rules[0]
	key := bucketKey("a", r.scope)
	svc.counter.record(key, r.rule.MaxRequests, r.window, now)
	if svc.channelAvailable("a", rules, now) {
		t.Fatal("刚用满的 Key 应判定为不可用")
	}
	if !svc.channelAvailable("a", rules, now.Add(61*time.Second)) {
		t.Fatal("窗口滑出后 Key 应恢复可用")
	}
}

// TestScopeKeySharedBucket 多模型规则共用一个桶（配额跟着规则走）。
func TestScopeKeySharedBucket(t *testing.T) {
	a := scopeKey([]string{"m1", "m2"}, 0)
	b := scopeKey([]string{"m2", "m1"}, 0)
	if a != b {
		t.Fatalf("同规则不同模型顺序应共用桶：%q vs %q", a, b)
	}
	if scopeKey([]string{"*"}, 0) == scopeKey([]string{"m1"}, 0) {
		t.Fatal("全通配与单模型应是不同的桶")
	}
}

// idsOf 取候选 ID 列表（断言用）。
func idsOf(chs []modelgateway.ResolvedChannel) []string {
	out := make([]string, 0, len(chs))
	for _, ch := range chs {
		out = append(out, ch.ID)
	}
	return out
}

