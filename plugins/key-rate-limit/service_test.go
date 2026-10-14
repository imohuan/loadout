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
	if svc.windowHasRoom("a", rules, now) {
		t.Fatal("刚用满的 Key 应判定为不可用")
	}
	if !svc.windowHasRoom("a", rules, now.Add(61*time.Second)) {
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

// pinnedPipe 构造一个「测试锁定」管线：带上 model-gateway 写入的 pin 标记。
func pinnedPipe(model, channelID string) *modelgateway.ProxyPipeline {
	p := testPipe(model)
	p.Metadata[modelgateway.MetadataPinChannel] = channelID
	return p
}

// TestPinnedBypassesLimitWhenExhausted 测试锁定请求在限流已用满时仍必须放行到目标 Key。
//
// 场景：次数配额已被日常流量用满，用户点某个 Key 的「测试」按钮想知道它还能不能用。
// 若限流照常生效，目标 Key 会被剔除 → 测的根本不是它；配置了 reject 时还会直接 429
// → 用户永远得不到答案。这正是「测试按钮要能探测被限流跳过的 Key」所要求的语义。
func TestPinnedBypassesLimitWhenExhausted(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(1, 60, types.RateLimitReject)})

	// 先用日常流量把 a 的配额用满。
	normal := testPipe("gpt-4o")
	normal.Metadata["__current_channel"] = "a"
	if _, err := svc.HandleBeforeAttempt(normal); err != nil {
		t.Fatalf("日常流量消费失败: %v", err)
	}

	// 日常流量此刻应被限流挡住（回归保护：确认前置条件成立）。
	if _, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", channels("a")); err == nil {
		t.Fatal("日常流量在配额用满时应被拒绝（前置条件不成立，测试无意义）")
	}

	// 测试锁定请求：必须原样放行，且候选不被裁剪。
	pinned := pinnedPipe("gpt-4o", "a")
	got, err := resolve(t, svc, pinned, "gpt-4o", channels("a"))
	if err != nil {
		t.Fatalf("测试锁定请求不应被限流拒绝: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("测试锁定请求应保留目标 Key a，实际 %v", idsOf(got))
	}

	// 测试锁定请求不占并发名额：并发配 1 也不该因为它而挤掉日常流量。
	pipeWithConc := pinnedPipe("gpt-4o", "a")
	if _, err := svc.HandleBeforeAttempt(pipeWithConc); err != nil {
		t.Fatalf("测试锁定请求消费不应报错: %v", err)
	}
	if n := svc.conc.size(); n != 0 {
		t.Fatalf("测试锁定请求不应占并发名额，实际残留 %d 个键", n)
	}
}

// TestPinnedDoesNotConsumeQuota 测试锁定请求不消耗配额：
// 反复探测同一个 Key，不应把它的窗口配额吃光而影响真实流量。
func TestPinnedDoesNotConsumeQuota(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{rateLimitRoute(2, 60, types.RateLimitReject)})

	// 连点 10 次「测试」按钮。
	for i := 0; i < 10; i++ {
		p := pinnedPipe("gpt-4o", "a")
		if _, err := svc.HandleBeforeAttempt(p); err != nil {
			t.Fatalf("第 %d 次探测不应报错: %v", i+1, err)
		}
	}
	// 探测若计入配额，日常流量的这两个名额就没了；这里应仍有余量。
	key := bucketKey("a", scopeKey([]string{"*"}, 0))
	if used := svc.counter.used(key, 2, time.Minute, time.Now()); used != 0 {
		t.Fatalf("测试探测不应计入窗口配额，实际已用 %d 次", used)
	}
}

// concRoute 构造一条「只限并发、不限次数」的 key_rate_limit 路由。
func concRoute(maxConcurrency int, onExhausted string) types.CapabilityRoute {
	return types.CapabilityRoute{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit:  &types.RateLimitRule{MaxConcurrency: maxConcurrency, OnExhausted: onExhausted},
	}
}

// TestConcurrencyOnlyDoesNotLimitCounts 只配并发、不配次数时，请求次数不受限制
// （次数维度 MaxRequests=0 应被忽略，而不是当成「最多 0 次」把请求全拒）。
func TestConcurrencyOnlyDoesNotLimitCounts(t *testing.T) {
	svc := newTestService(t)
	// 并发上限设为 1：若「占」与「放」没有严格配对，第 2 轮就会开始被拒。
	writeRoutes(t, svc, []types.CapabilityRoute{concRoute(1, types.RateLimitReject)})

	pipe := testPipe("gpt-4o")
	const rounds = 20
	for i := 0; i < rounds; i++ {
		// 每次都完整走一遍「占用 → 消费 → 释放」，模拟 20 个串行请求。
		got, err := resolve(t, svc, pipe, "gpt-4o", channels("a"))
		if err != nil {
			t.Fatalf("第 %d 次 resolve 不应报错（不限次数）: %v", i+1, err)
		}
		if len(got) != 1 {
			t.Fatalf("第 %d 次应保留候选，实际 %v", i+1, idsOf(got))
		}
		pipe.Metadata["__current_channel"] = "a"
		if _, err := svc.HandleBeforeAttempt(pipe); err != nil {
			t.Fatalf("第 %d 次消费失败: %v", i+1, err)
		}
		_, _ = svc.HandleAttemptFinished(&modelgateway.ProxyAttemptFinishedPayload{Pipe: pipe, Model: "gpt-4o", ChannelID: "a", Result: "success"})
	}
	// 名额必须全部归还：并发计数器里不应残留任何键（否则就是泄漏，长跑后会把渠道卡死）。
	if n := svc.conc.size(); n != 0 {
		t.Fatalf("%d 轮结束后并发占位应清零，实际残留 %d 个键", rounds, n)
	}
}

// TestConcurrencyRejectsWhenInFlightFull 并发占满时按 reject 策略返回 429。
// 关键：占用发生在候选筛选阶段，因此第二次请求（前一次尚未结束）就必须被拒。
func TestConcurrencyRejectsWhenInFlightFull(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{concRoute(1, types.RateLimitReject)})

	pipe1 := testPipe("gpt-4o")
	// 第 1 个请求：占住唯一名额，且**不释放**（模拟请求还在进行中）。
	got, err := resolve(t, svc, pipe1, "gpt-4o", channels("a"))
	if err != nil {
		t.Fatalf("第 1 个请求 resolve 报错: %v", err)
	}
	if len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("第 1 个请求应保留候选 a，实际 %v", idsOf(got))
	}
	pipe1.Metadata["__current_channel"] = "a"
	if _, err := svc.HandleBeforeAttempt(pipe1); err != nil {
		t.Fatalf("第 1 个请求消费失败: %v", err)
	}

	// 第 2 个请求（第 1 个仍在途）：并发已满 → 429。
	pipe2 := testPipe("gpt-4o")
	_, err = resolve(t, svc, pipe2, "gpt-4o", channels("a"))
	if err == nil {
		t.Fatal("在途并发已满时应拒绝第 2 个请求")
	}
	gw, ok := err.(*modelgateway.GatewayError)
	if !ok || gw.Status != http.StatusTooManyRequests {
		t.Fatalf("应返回 429 GatewayError，实际 %T %v", err, err)
	}

	// 第 1 个请求结束后名额释放，第 3 个请求可以正常通过。
	_, _ = svc.HandleAttemptFinished(&modelgateway.ProxyAttemptFinishedPayload{Pipe: pipe1, Model: "gpt-4o", ChannelID: "a", Result: "success"})
	got3, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", channels("a"))
	if err != nil {
		t.Fatalf("前一个请求结束后应能再次放行: %v", err)
	}
	if len(got3) != 1 {
		t.Fatalf("结束的请求应已释放名额，实际候选 %v", idsOf(got3))
	}
}

// TestConcurrencyRotatesToOtherKey 并发占满时自动换到另一个账号（而不是直接拒绝）。
func TestConcurrencyRotatesToOtherKey(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{concRoute(1, types.RateLimitReject)})

	pipe1 := testPipe("gpt-4o")
	got, _ := resolve(t, svc, pipe1, "gpt-4o", channels("a", "b"))
	if len(got) == 0 || got[0].ID != "a" {
		t.Fatalf("第 1 个请求应占用第一个候选 a（b 保留作 failover），实际 %v", idsOf(got))
	}
	pipe1.Metadata["__current_channel"] = "a"
	_, _ = svc.HandleBeforeAttempt(pipe1)

	// a 的并发已满（1 个在途）：第 2 个请求应落到 b。
	got2, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("a 满 b 空时不应拒绝: %v", err)
	}
	if len(got2) == 0 || got2[0].ID != "b" {
		t.Fatalf("a 并发占满后应把 b 排到首位（自动换账号），实际 %v", idsOf(got2))
	}
}

// TestConcurrencyReuseKeepsServing reuse 策略下并发占满仍放行。
func TestConcurrencyReuseKeepsServing(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{concRoute(1, types.RateLimitReuse)})

	pipe1 := testPipe("gpt-4o")
	_, _ = resolve(t, svc, pipe1, "gpt-4o", channels("a"))
	pipe1.Metadata["__current_channel"] = "a"
	_, _ = svc.HandleBeforeAttempt(pipe1)

	got, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", channels("a"))
	if err != nil {
		t.Fatalf("reuse 策略不应拒绝: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reuse 策略应原样返回候选，实际 %v", idsOf(got))
	}
}

// TestBothDimensionsBothMustAllow 两个维度都配时，任一维度达上限即换 Key。
func TestBothDimensionsBothMustAllow(t *testing.T) {
	svc := newTestService(t)
	writeRoutes(t, svc, []types.CapabilityRoute{{
		Models:     []string{"*"},
		Capability: capabilityName,
		Route:      types.RouteProxy,
		RateLimit:  &types.RateLimitRule{MaxRequests: 5, WindowSeconds: 60, MaxConcurrency: 1, OnExhausted: types.RateLimitReject},
	}})

	// a 的次数还剩很多，但并发已满 → 换 b。
	pipe1 := testPipe("gpt-4o")
	_, _ = resolve(t, svc, pipe1, "gpt-4o", channels("a", "b"))
	pipe1.Metadata["__current_channel"] = "a"
	_, _ = svc.HandleBeforeAttempt(pipe1)

	got, err := resolve(t, svc, testPipe("gpt-4o"), "gpt-4o", channels("a", "b"))
	if err != nil {
		t.Fatalf("a 并发满 b 空时不应拒绝: %v", err)
	}
	if len(got) == 0 || got[0].ID != "b" {
		t.Fatalf("应因并发维度把 b 排到首位，实际 %v", idsOf(got))
	}
}

