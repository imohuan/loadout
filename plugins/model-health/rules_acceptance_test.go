package modelhealth

import (
	"context"
	"database/sql"
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
	// 关键语义：额度用尽是账号级的，必须同时禁用整条 Key（channel_states），
	// 否则同一账号会在下一个模型上继续被选中，反复失败。
	var channelStatus, channelClass, channelUntil string
	if err := database.QueryRow(
		`SELECT status, COALESCE(last_failure_class,''), COALESCE(disabled_until,'') FROM channel_states WHERE channel_id='c'`,
	).Scan(&channelStatus, &channelClass, &channelUntil); err != nil {
		t.Fatalf("quota must disable the whole Key (channel_states): %v", err)
	}
	if channelStatus != statusCooling || channelClass != "rule_disable_key_daily" {
		t.Fatalf("channel state = %q/%q, want cooling/rule_disable_key_daily", channelStatus, channelClass)
	}
	// 次日恢复：恢复点必须落在「明天」，不能是今天稍后的刷新点（否则当天就会复活）。
	channelUntilTime, err := time.Parse(time.RFC3339Nano, channelUntil)
	if err != nil {
		t.Fatalf("channel until parse: %v", err)
	}
	beijing := channelUntilTime.In(time.FixedZone("Asia/Shanghai", 8*3600))
	nowBeijing := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600))
	if !beijing.After(nowBeijing) {
		t.Fatalf("channel until %v 必须晚于当前时间", channelUntil)
	}
	if beijing.Format("2006-01-02") == nowBeijing.Format("2006-01-02") {
		t.Fatalf("每日额度必须次日恢复，实际恢复点=%v（北京时间当天）", beijing)
	}
	// 规则归属：状态要能告诉 UI 是哪条规则判的，用户才能去改那条规则。
	var ruleID, ruleName string
	if err := database.QueryRow(`SELECT COALESCE(last_rule_id,''), COALESCE(last_rule_name,'') FROM model_states WHERE channel_id='c' AND model='m'`).Scan(&ruleID, &ruleName); err != nil {
		t.Fatal(err)
	}
	if ruleID != "seed-002" || ruleName == "" {
		t.Fatalf("rule attribution = %q/%q, want seed-002 + name", ruleID, ruleName)
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

// TestAcceptanceKeyCooldownExpires 回归：Key 级的定时禁用必须能到期自动恢复。
//
// 规则引擎的 disable_key 会写 channel_states，而 CheckNow 原先只清理 model_states，
// 于是被按日/定时禁用的 Key 会永远停在 cooling，路由再也选不到它。
// 这里模拟「已到期」的渠道冷却，CheckNow 必须把它恢复成 available。
func TestAcceptanceKeyCooldownExpires(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, disabled_until, fail_count, last_failure_class, updated_at)
		VALUES ('c', 'cooling', ?, 3, 'rule_disable_key_daily', ?)`, past, past); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckNow(ctx, false); err != nil {
		t.Fatal(err)
	}
	var status string
	var until sql.NullString
	if err := database.QueryRow(`SELECT status, disabled_until FROM channel_states WHERE channel_id='c'`).Scan(&status, &until); err != nil {
		t.Fatal(err)
	}
	if status != statusAvailable || until.Valid {
		t.Fatalf("到期的 Key 冷却应自动恢复, got status=%q until=%q", status, until.String)
	}
}
