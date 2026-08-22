package routelog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/plugins/contracts"
)

func logDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestRouteLogReconstructsTimeline(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	started := time.Now().Add(-time.Second)
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r1", RequestedModel: "auto", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r1", StepNo: "1", Model: "m", ChannelID: "c", StartedAt: started, FinishedAt: pointer(time.Now()), Result: "failed", ErrorMessage: "balance", Metadata: map[string]any{"safe": "value"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r1", FinishedAt: time.Now(), Result: "failed", ErrorMessage: "done"}); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].StepNo != "1" {
		t.Fatalf("timeline not reconstructed: %+v", detail)
	}
}

func TestRouteLogRejectsSensitiveMetadata(t *testing.T) {
	service := NewService(logDB(t), nil)
	if err := service.Start(context.Background(), contracts.RouteRequest{RequestID: "r2", RequestedModel: "m", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(context.Background(), contracts.RouteAttempt{RequestID: "r2", StepNo: "1", Model: "m", StartedAt: time.Now(), Metadata: map[string]any{"Authorization": "secret"}}); err == nil {
		t.Fatal("sensitive metadata was accepted")
	}
}

// TestRouteLogRetryMergesSameRequestID 回归：客户端重试复用同一 X-Request-Id 时，
// 同 request_id 的 Start/Attempt 必须合并（UPSERT），不能报主键冲突或产生多行。
func TestRouteLogRetryMergesSameRequestID(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	started := time.Now().Add(-time.Second)

	// 第一次请求
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-retry", RequestedModel: "auto-demo", VirtualModel: "auto-demo", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: "1", Model: "model-x", ChannelID: "ch-x", StartedAt: started, Result: "skipped", FailureClass: "no_available"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-retry", FinishedAt: time.Now(), Result: "failed", ErrorMessage: "第一次失败"}); err != nil {
		t.Fatal(err)
	}

	// 客户端重试：同一 request_id 再次请求，attempts 按 step 覆盖
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-retry", RequestedModel: "auto-demo", VirtualModel: "auto-demo", StartedAt: time.Now()}); err != nil {
		t.Fatalf("重试 Start 不应报主键冲突: %v", err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: "1", Model: "model-x", ChannelID: "ch-x", StartedAt: time.Now(), Result: "skipped", FailureClass: "no_available"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: "2", Model: "model-y", ChannelID: "ch-y", StartedAt: time.Now(), Result: "skipped", FailureClass: "no_available"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-retry", FinishedAt: time.Now(), Result: "failed", ErrorMessage: "重试后仍失败"}); err != nil {
		t.Fatal(err)
	}

	// 仍应是同一条日志
	list, err := service.List(ctx, contracts.RouteLogFilter{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, item := range list.Items {
		if item.RequestID == "r-retry" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("同 request_id 重试后应只有 1 条日志，实际 %d 条", count)
	}

	detail, err := service.Detail(ctx, "r-retry")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 2 {
		t.Fatalf("attempts 应合并为 2 条（step1/step2），实际 %d 条: %+v", len(detail.Attempts), detail.Attempts)
	}
	if detail.Attempts[1].Model != "model-y" {
		t.Fatalf("step2 应覆盖为 model-y，实际 %+v", detail.Attempts[1])
	}
	if !strings.Contains(detail.ErrorMessage, "重试后仍失败") {
		t.Fatalf("error_message 应更新为最后一次，实际 %q", detail.ErrorMessage)
	}
}

func pointer(value time.Time) *time.Time { return &value }

// TestAttemptFirstByteAt：running 占位写 first_byte_at，success UPSERT 不传时保留旧值。
func TestAttemptFirstByteAt(t *testing.T) {
	ctx := context.Background()
	svc := NewService(logDB(t), nil)
	now := time.Now()
	fb := now.Add(-3 * time.Second)
	if err := svc.Start(ctx, contracts.RouteRequest{RequestID: "r-fb", RequestedModel: "m", StartedAt: now}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// 1) 首次不传 FirstByteAt → 读回应为 nil（写 NULL 而非空串）。
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: "1", Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, Result: "running", Stream: true,
	}); err != nil {
		t.Fatalf("Attempt(running no-fb): %v", err)
	}
	if detail, err := svc.Detail(ctx, "r-fb"); err != nil {
		t.Fatalf("Detail(no-fb): %v", err)
	} else if detail.Attempts[0].FirstByteAt != nil {
		t.Fatalf("first_byte_at 未传时应为 nil: %+v", detail.Attempts[0].FirstByteAt)
	}
	// 2) running 占位带 first_byte_at。
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: "1", Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, Result: "running", Stream: true, FirstByteAt: &fb,
	}); err != nil {
		t.Fatalf("Attempt(running): %v", err)
	}
	// 3) success UPSERT 不传 FirstByteAt → 旧值保留（COALESCE 生效）。
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: "1", Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, FinishedAt: pointer(now.Add(10 * time.Second)), Result: "success", Stream: true,
	}); err != nil {
		t.Fatalf("Attempt(success): %v", err)
	}
	detail, err := svc.Detail(ctx, "r-fb")
	if err != nil {
		t.Fatalf("Detail: %v", err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].FirstByteAt == nil {
		t.Fatalf("first_byte_at 应保留: %+v", detail.Attempts)
	}
	// 纳秒级比较（RFC3339Nano 往返无损）。
	if !detail.Attempts[0].FirstByteAt.Equal(fb) {
		t.Fatalf("first_byte_at = %v, want %v", detail.Attempts[0].FirstByteAt, fb)
	}
	// 4) success UPSERT 显式传新 FirstByteAt → 覆盖旧值。
	newFB := now.Add(-1 * time.Second)
	if _, err := svc.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-fb", StepNo: "1", Action: "首次尝试", Model: "m", ChannelID: "c",
		StartedAt: now, FinishedAt: pointer(now.Add(10 * time.Second)), Result: "success", Stream: true,
		FirstByteAt: &newFB,
	}); err != nil {
		t.Fatalf("Attempt(success new-fb): %v", err)
	}
	detail2, err := svc.Detail(ctx, "r-fb")
	if err != nil {
		t.Fatalf("Detail(2): %v", err)
	}
	if detail2.Attempts[0].FirstByteAt == nil || !detail2.Attempts[0].FirstByteAt.Equal(newFB) {
		t.Fatalf("first_byte_at 应被新值覆盖: %+v", detail2.Attempts[0].FirstByteAt)
	}
}

// ---- 活跃登记表 + SelfHeal 自愈 ----

func TestIsActiveLifecycle(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()

	// Start 前：不存在 → 不活跃
	if service.IsActive("r-active", time.Minute) {
		t.Fatal("未 Start 的 id 不应活跃")
	}
	// Start 后：活跃
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-active", RequestedModel: "m", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if !service.IsActive("r-active", time.Minute) {
		t.Fatal("Start 后应活跃")
	}
	// 空 id / 未知 id → 不活跃
	if service.IsActive("", time.Minute) {
		t.Fatal("空 id 不应活跃")
	}
	if service.IsActive("r-unknown", time.Minute) {
		t.Fatal("未知 id 不应活跃")
	}
	// Finish 后：注销
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-active", FinishedAt: time.Now(), Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if service.IsActive("r-active", time.Minute) {
		t.Fatal("Finish 后应注销")
	}
}

func TestIsActiveMaxAge(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-old", RequestedModel: "m", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	// 登记时刻往前拨 11 分钟（> config.RouteLogSelfHealMaxAlive=10min）→ 视为死
	service.mu.Lock()
	service.activeAt["r-old"] = time.Now().Add(-11 * time.Minute)
	service.mu.Unlock()
	if service.IsActive("r-old", 10*time.Minute) {
		t.Fatal("超过 maxAge 应视为不活跃（死锁兜底）")
	}
}

func TestSelfHealSkipsActiveRequest(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	// 刚 Start：登记表活跃 → SelfHeal 应 no-op，即使 started_at 很老
	old := time.Now().Add(-5 * time.Minute)
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-live", RequestedModel: "m", StartedAt: old}); err != nil {
		t.Fatal(err)
	}
	if err := service.SelfHeal(ctx, "r-live", time.Minute); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r-live")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Result != "running" {
		t.Fatalf("活跃请求不应被自愈，result=%q", detail.Result)
	}
	if detail.FinishedAt != nil {
		t.Fatal("活跃请求不应有 finished_at")
	}
}

func TestSelfHealFinalizesStuckRequest(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	started := time.Now().Add(-5 * time.Minute)
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-stuck", RequestedModel: "m", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	// 模拟转发结束但 Finish 未落库：从登记表移除（等同 Finish 已被调用/进程状态丢失）
	service.mu.Lock()
	delete(service.activeAt, "r-stuck")
	service.mu.Unlock()

	if err := service.SelfHeal(ctx, "r-stuck", time.Minute); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r-stuck")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Result != "stream_interrupted" {
		t.Fatalf("卡死请求应收尾为 stream_interrupted，实际 %q", detail.Result)
	}
	if detail.FinishedAt == nil {
		t.Fatal("卡死请求应写 finished_at")
	}
	if detail.Duration.Milliseconds() < 4*60*1000 {
		t.Fatalf("duration 应接近 started_at 到 now（>=4min），实际 %dms", detail.Duration.Milliseconds())
	}
	if detail.ErrorMessage == "" {
		t.Fatal("卡死请求应补 error_message")
	}
}

func TestSelfHealNoopForFinishedOrMissing(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	// 已完结：不修
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-done", RequestedModel: "m", StartedAt: time.Now().Add(-5 * time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-done", FinishedAt: time.Now(), Result: "success"}); err != nil {
		t.Fatal(err)
	}
	if err := service.SelfHeal(ctx, "r-done", time.Minute); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r-done")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Result != "success" {
		t.Fatalf("已完结记录不应被改动，result=%q", detail.Result)
	}
	// 不存在的记录：no-op 不报错
	if err := service.SelfHeal(ctx, "r-missing", time.Minute); err != nil {
		t.Fatalf("不存在记录应 no-op，err=%v", err)
	}
	// threshold<=0：禁用
	if err := service.SelfHeal(ctx, "r-still-running", 0); err != nil {
		t.Fatal(err)
	}
}

// TestSelfHealPromotesSuccessfulAttempt：最后一次非视觉 attempt 是 success 时，
// 整条请求应升级为 success 并复用 attempt 的 final_model / channel / tokens /
// duration 等数据（而非粗暴标 stream_interrupted）。视觉 attempt 不算数。
func TestSelfHealPromotesSuccessfulAttempt(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	started := time.Now().Add(-10 * time.Minute)
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-promote", RequestedModel: "auto", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	// 视觉 attempt（应被排除，不算数）
	visionEnd := started.Add(3 * time.Second)
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-promote", StepNo: "1", Action: "视觉识别", Model: "qwen-vl",
		ChannelID: "ch-v", StartedAt: started, FinishedAt: &visionEnd, Result: "success",
		Duration: contracts.DurationMS(3 * time.Second),
		Metadata: map[string]any{"capability": "vision", "image_count": 1},
	}); err != nil {
		t.Fatal(err)
	}
	// 主链路：第一次失败
	failStart := started.Add(3 * time.Second)
	failEnd := failStart.Add(2 * time.Second)
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-promote", StepNo: "2", Action: "首次尝试", Model: "deepseek-x", ChannelID: "ch-x",
		StartedAt: failStart, FinishedAt: &failEnd, Result: "failed",
		FailureClass: "upstream_5xx", StatusCode: 502,
		Duration: contracts.DurationMS(2 * time.Second), ErrorMessage: "bad gateway",
	}); err != nil {
		t.Fatal(err)
	}
	// 主链路：第二次成功（应是最后一次非视觉 attempt）
	okStart := failEnd.Add(time.Millisecond)
	okEnd := okStart.Add(3*time.Second + 110*time.Millisecond)
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-promote", StepNo: "3", Action: "切换渠道", Model: "glm-5", ChannelID: "ch-y",
		StartedAt: okStart, FinishedAt: &okEnd, Result: "success",
		StatusCode: 200, Stream: true,
		PromptTokens: 1500, CompletionTokens: 320, CachedTokens: 80,
		Duration: contracts.DurationMS(3*time.Second + 110*time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	// 模拟崩溃：登记表移除（等同进程死）
	service.mu.Lock()
	delete(service.activeAt, "r-promote")
	service.mu.Unlock()
	// 自愈
	if err := service.SelfHeal(ctx, "r-promote", time.Minute); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r-promote")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Result != "success" {
		t.Fatalf("最后一次成功 attempt 应触发升级为 success，实际 %q", detail.Result)
	}
	if detail.FinalModel != "glm-5" {
		t.Errorf("final_model 应取最后成功 attempt 的 model = glm-5，实际 %q", detail.FinalModel)
	}
	if detail.FinalChannelID != "ch-y" {
		t.Errorf("final_channel_id 应取最后成功 attempt 的 channel = ch-y，实际 %q", detail.FinalChannelID)
	}
	if detail.HTTPStatus != 200 {
		t.Errorf("http_status 应取最后成功 attempt 的 status_code = 200，实际 %d", detail.HTTPStatus)
	}
	if detail.PromptTokens != 1500 || detail.CompletionTokens != 320 || detail.CachedTokens != 80 {
		t.Errorf("tokens 应复用 last attempt 的，实际 p=%d c=%d cache=%d",
			detail.PromptTokens, detail.CompletionTokens, detail.CachedTokens)
	}
	if !detail.Stream {
		t.Errorf("stream 应复用 last attempt 的 stream=true")
	}
	if detail.ErrorMessage != "" {
		t.Errorf("升级为 success 应清空 error_message，实际 %q", detail.ErrorMessage)
	}
	if detail.FinishedAt == nil {
		t.Fatal("应写 finished_at")
	}
	// duration 应为最后一次成功 attempt 结束到请求 started 的总时长
	wantDur := okEnd.Sub(started).Milliseconds()
	if got := detail.Duration.Milliseconds(); got < wantDur-100 || got > wantDur+100 {
		t.Errorf("duration 应约 %dms（含失败重试时间），实际 %dms", wantDur, got)
	}
}

// TestSelfHealIgnoresVisionOnlyAttempts：所有非视觉 attempt 都失败了（或没有），）
// 即使视觉 attempt 成功，也走原 stream_interrupted 收尾。
func TestSelfHealIgnoresVisionOnlyAttempts(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	started := time.Now().Add(-5 * time.Minute)
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-visonly", RequestedModel: "m", StartedAt: started}); err != nil {
		t.Fatal(err)
	}
	// 视觉成功
	visionEnd := started.Add(time.Second)
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-visonly", StepNo: "1", Action: "视觉识别", Model: "qwen-vl",
		ChannelID: "ch-v", StartedAt: started, FinishedAt: &visionEnd, Result: "success",
		Duration: contracts.DurationMS(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	// 主链路失败
	mainStart := visionEnd.Add(time.Millisecond)
	mainEnd := mainStart.Add(time.Second)
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{
		RequestID: "r-visonly", StepNo: "2", Action: "首次尝试", Model: "x", ChannelID: "ch-x",
		StartedAt: mainStart, FinishedAt: &mainEnd, Result: "failed", StatusCode: 500,
		Duration: contracts.DurationMS(time.Second), ErrorMessage: "boom",
	}); err != nil {
		t.Fatal(err)
	}
	service.mu.Lock()
	delete(service.activeAt, "r-visonly")
	service.mu.Unlock()
	if err := service.SelfHeal(ctx, "r-visonly", time.Minute); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r-visonly")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Result != "stream_interrupted" {
		t.Fatalf("视觉成功但主链路失败时不应被升级，仍走 stream_interrupted，实际 %q", detail.Result)
	}
}

// TestCompareStepNo：点分 step 数值比较语义（符号即顺序）。
func TestCompareStepNo(t *testing.T) {
	less := []struct{ a, b string }{
		{"1", "1.1"},
		{"1.1", "1.2"},
		{"1.2", "2"},
		{"2", "2.1"},
		{"1", "2"},
		{"1.1", "1.10"},
		{"1.2", "1.10"}, // 点分段数值比较：2 < 10（非字典序）
	}
	for _, c := range less {
		if got := compareStepNo(c.a, c.b); got >= 0 {
			t.Errorf("compareStepNo(%q, %q) = %d, want < 0", c.a, c.b, got)
		}
		if got := compareStepNo(c.b, c.a); got <= 0 {
			t.Errorf("compareStepNo(%q, %q) = %d, want > 0", c.b, c.a, got)
		}
	}
	equal := []struct{ a, b string }{
		{"1", "1"},
		{"1.2", "1.2"},
	}
	for _, c := range equal {
		if got := compareStepNo(c.a, c.b); got != 0 {
			t.Errorf("compareStepNo(%q, %q) = %d, want 0（同层相等）", c.a, c.b, got)
		}
	}
}

// TestListPagination：List 支持 Limit/Offset 分页，Total 为满足过滤条件的全量条数。
func TestListPagination(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	// 插入 25 条日志（started_at 递增，保证 ORDER BY 顺序确定）
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		id := fmt.Sprintf("r-pg-%02d", i)
		started := base.Add(time.Duration(i) * time.Minute)
		if err := service.Start(ctx, contracts.RouteRequest{RequestID: id, RequestedModel: "m", StartedAt: started}); err != nil {
			t.Fatalf("Start(%s): %v", id, err)
		}
		if err := service.Finish(ctx, contracts.RouteFinish{RequestID: id, FinishedAt: started.Add(time.Second), Result: "success"}); err != nil {
			t.Fatalf("Finish(%s): %v", id, err)
		}
	}
	// 第 1 页：pageSize=10 → 10 条、total=25
	page1, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if page1.Total != 25 {
		t.Fatalf("total = %d, want 25", page1.Total)
	}
	if len(page1.Items) != 10 {
		t.Fatalf("page1 len = %d, want 10", len(page1.Items))
	}
	// 第 3 页：offset=20 → 5 条（余量）
	page3, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(page3.Items) != 5 {
		t.Fatalf("page3 len = %d, want 5", len(page3.Items))
	}
	// 越界页：offset=30 → 0 条，但 total 仍为 25
	pageOver, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: 30})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageOver.Items) != 0 {
		t.Fatalf("over page len = %d, want 0", len(pageOver.Items))
	}
	if pageOver.Total != 25 {
		t.Fatalf("over page total = %d, want 25", pageOver.Total)
	}
	// 负 offset 钳 0
	pageNeg, err := service.List(ctx, contracts.RouteLogFilter{Limit: 10, Offset: -5})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageNeg.Items) != 10 {
		t.Fatalf("negative offset len = %d, want 10", len(pageNeg.Items))
	}
	// 过滤条件同样影响 total：造 1 条 failed 做对照（r-pg-00 已是 success）
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-fail", RequestedModel: "m", StartedAt: base.Add(-2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-fail", FinishedAt: base.Add(-2 * time.Hour).Add(time.Second), Result: "failed"}); err != nil {
		t.Fatal(err)
	}
	failedPage, err := service.List(ctx, contracts.RouteLogFilter{Result: "failed", Limit: 10, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if failedPage.Total != 1 {
		t.Fatalf("failed total = %d, want 1", failedPage.Total)
	}
}

// TestDetailSortsByStepNo：step_no 为 TEXT 后 SQL ORDER BY 字典序错误（"1.10" < "1.2"），
// Detail 应在 Go 侧按点分数值比较排序。乱序插入 "1"、"1.2"、"1.1"、"2" → 返回 1 < 1.1 < 1.2 < 2。
func TestDetailSortsByStepNo(t *testing.T) {
	service := NewService(logDB(t), nil)
	ctx := context.Background()
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-sort", RequestedModel: "m", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, stepNo := range []string{"1", "1.2", "1.1", "2"} {
		if _, err := service.Attempt(ctx, contracts.RouteAttempt{
			RequestID: "r-sort", StepNo: stepNo, Action: "首次尝试", Model: "m",
			StartedAt: now, FinishedAt: pointer(now.Add(time.Second)), Result: "success",
		}); err != nil {
			t.Fatalf("Attempt(step=%s): %v", stepNo, err)
		}
	}
	detail, err := service.Detail(ctx, "r-sort")
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, a := range detail.Attempts {
		steps = append(steps, a.StepNo)
	}
	want := []string{"1", "1.1", "1.2", "2"}
	if len(steps) != len(want) {
		t.Fatalf("attempts step 序列 = %v, want %v", steps, want)
	}
	for i := range want {
		if steps[i] != want[i] {
			t.Fatalf("attempts step 序列 = %v, want %v", steps, want)
		}
	}
}
