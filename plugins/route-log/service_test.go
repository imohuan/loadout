package routelog

import (
	"context"
	"database/sql"
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
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r1", StepNo: 1, Model: "m", ChannelID: "c", StartedAt: started, FinishedAt: pointer(time.Now()), Result: "failed", ErrorMessage: "balance", Metadata: map[string]any{"safe": "value"}}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r1", FinishedAt: time.Now(), Result: "failed", ErrorMessage: "done"}); err != nil {
		t.Fatal(err)
	}
	detail, err := service.Detail(ctx, "r1")
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Attempts) != 1 || detail.Attempts[0].StepNo != 1 {
		t.Fatalf("timeline not reconstructed: %+v", detail)
	}
}

func TestRouteLogRejectsSensitiveMetadata(t *testing.T) {
	service := NewService(logDB(t), nil)
	if err := service.Start(context.Background(), contracts.RouteRequest{RequestID: "r2", RequestedModel: "m", StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(context.Background(), contracts.RouteAttempt{RequestID: "r2", StepNo: 1, Model: "m", StartedAt: time.Now(), Metadata: map[string]any{"Authorization": "secret"}}); err == nil {
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
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: 1, Model: "model-x", ChannelID: "ch-x", StartedAt: started, Result: "skipped", FailureClass: "no_available"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Finish(ctx, contracts.RouteFinish{RequestID: "r-retry", FinishedAt: time.Now(), Result: "failed", ErrorMessage: "第一次失败"}); err != nil {
		t.Fatal(err)
	}

	// 客户端重试：同一 request_id 再次请求，attempts 按 step 覆盖
	if err := service.Start(ctx, contracts.RouteRequest{RequestID: "r-retry", RequestedModel: "auto-demo", VirtualModel: "auto-demo", StartedAt: time.Now()}); err != nil {
		t.Fatalf("重试 Start 不应报主键冲突: %v", err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: 1, Model: "model-x", ChannelID: "ch-x", StartedAt: time.Now(), Result: "skipped", FailureClass: "no_available"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Attempt(ctx, contracts.RouteAttempt{RequestID: "r-retry", StepNo: 2, Model: "model-y", ChannelID: "ch-y", StartedAt: time.Now(), Result: "skipped", FailureClass: "no_available"}); err != nil {
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
	for _, item := range list {
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
