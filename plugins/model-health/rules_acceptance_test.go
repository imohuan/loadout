package modelhealth

import (
	"context"
	"testing"
	"time"

	"loadout/plugins/contracts"
)

// TestAcceptanceQuotaDailyDisable 验收：429+业务码14018 → key 禁用至次日（cooling+未来时间点），
// 期间 CheckNow 不会提前恢复；context canceled → 不改变状态。
func TestAcceptanceQuotaDailyDisable(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at) VALUES ('c','m',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	class, err := service.RecordFailure(ctx, contracts.RouteFailure{
		ChannelID: "c", Model: "m", StatusCode: 429,
		ErrorBody: `{"error":{"data":{"code":14018,"msg":"额度已用尽"}}}`,
	})
	if err != nil || class != "disable_key" {
		t.Fatalf("quota classification: %q %v", class, err)
	}
	var status string
	var untilStr string
	if err := database.QueryRow(`SELECT status, COALESCE(disabled_until,'') FROM model_states WHERE channel_id='c' AND model='m'`).Scan(&status, &untilStr); err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339Nano, untilStr)
	if err != nil {
		t.Fatalf("until parse: %v", err)
	}
	if status != statusCooling || untilStr == "" || !until.After(time.Now()) {
		t.Fatalf("quota state: status=%q until=%v, want cooling+future", status, untilStr)
	}
	// CheckNow 不应提前恢复
	if err := service.CheckNow(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT status FROM model_states WHERE channel_id='c' AND model='m'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != statusCooling {
		t.Fatalf("CheckNow must not recover daily quota early: %q", status)
	}
	// context canceled：忽略，不追加失败、不改状态
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m", Error: "context canceled"}); err != nil || class != "ignore" {
		t.Fatalf("cancel classification: %q %v", class, err)
	}
	var failCount int
	_ = database.QueryRow(`SELECT fail_count FROM model_states WHERE channel_id='c' AND model='m'`).Scan(&failCount)
	if failCount != 1 {
		t.Fatalf("cancel must not increment fail_count: %d", failCount)
	}
}
