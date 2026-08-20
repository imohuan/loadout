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
