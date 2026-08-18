package modelhealth

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/plugins/contracts"
)

func healthDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(t.TempDir() + "/loadout.db")
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES ('c', 'C', 'http://c', 1, 1, 'now', 'now')`)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestAvailabilitySeparatesManualAndHealth(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if err := service.SetModelEnabled(ctx, "c", "m", false); err != nil {
		t.Fatal(err)
	}
	availability, err := service.Check(ctx, "c", "m")
	if err != nil {
		t.Fatal(err)
	}
	if availability.EffectiveAvailable || availability.ManualEnabled {
		t.Fatalf("manual state lost: %+v", availability)
	}
	if _, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m", StatusCode: 429, Error: "rate limit"}); err != nil {
		t.Fatal(err)
	}
	availability, err = service.Check(ctx, "c", "m")
	if err != nil {
		t.Fatal(err)
	}
	if availability.HealthStatus != statusCooling || availability.EffectiveAvailable {
		t.Fatalf("cooling state incorrect: %+v", availability)
	}
	if err := service.RecordSuccess(ctx, "c", "m"); err != nil {
		t.Fatal(err)
	}
	availability, err = service.Check(ctx, "c", "m")
	if err != nil {
		t.Fatal(err)
	}
	if availability.EffectiveAvailable {
		t.Fatal("success must not reopen a manually disabled model")
	}
}

func TestBillingPropagationRequiresExplicitClassification(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m", StatusCode: 402, Error: "quota exceeded"}); err != nil || class != "model_quota" {
		t.Fatalf("model quota classification: %q %v", class, err)
	}
	var channelStatus sql.NullString
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != sql.ErrNoRows {
		t.Fatalf("model quota created unexpected channel state: %v", err)
	}
	if channelStatus.Valid {
		t.Fatalf("model quota unexpectedly disabled channel: %q", channelStatus.String)
	}
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m2", StatusCode: 402, Error: "account balance is empty"}); err != nil || class != "channel_billing" {
		t.Fatalf("channel billing classification: %q %v", class, err)
	}
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus.String != statusDisabled {
		t.Fatalf("channel billing did not propagate: %q", channelStatus.String)
	}
}

func TestListAssemblesStatus(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 渠道 'c' 进入 cooling（未过期）。
	if _, err := database.Exec(`INSERT INTO channel_states(channel_id, status, disabled_until, updated_at) VALUES ('c','cooling', ?, 'now')`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	// channel_models 有两个模型；m3 只存在于 model_states（不在目录）。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at) VALUES ('c','m1',1,'now','now'),('c','m2',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	lastSuccess := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	coolUntil := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, disabled_until, fail_count, last_error, last_success_at, updated_at) VALUES
		('c','m1',0,'available',NULL,0,'','now','now'),
		('c','m2',1,'cooling',?,3,'上游 503',?,'now'),
		('c','m3',1,'available',NULL,0,'',NULL,'now')`, coolUntil, lastSuccess); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 个渠道，实际 %d", len(items))
	}
	item := items[0]
	if item.Health.HealthStatus != statusCooling || item.Health.EffectiveAvailable {
		t.Fatalf("渠道冷却状态不符: %+v", item.Health)
	}
	if len(item.Models) != 3 {
		t.Fatalf("期望 3 个模型，实际 %d: %+v", len(item.Models), item.Models)
	}
	byModel := map[string]contracts.ModelStatus{}
	for _, m := range item.Models {
		byModel[m.Model] = m
	}
	if byModel["m1"].ManualEnabled || byModel["m1"].Health.EffectiveAvailable {
		t.Fatalf("m1 手动关闭状态不符: %+v", byModel["m1"])
	}
	m2 := byModel["m2"]
	if m2.Health.HealthStatus != statusCooling || m2.Health.EffectiveAvailable {
		t.Fatalf("m2 冷却状态不符: %+v", m2)
	}
	if m2.FailCount != 3 || m2.LastError != "上游 503" {
		t.Fatalf("m2 失败信息不符: %+v", m2)
	}
	if m2.LastSuccessAt == nil || m2.DisabledUntil == nil {
		t.Fatalf("m2 时间字段缺失: %+v", m2)
	}
	if !byModel["m3"].ManualEnabled || !byModel["m3"].Health.EffectiveAvailable {
		t.Fatalf("m3 应默认可用: %+v", byModel["m3"])
	}
}

func TestCheckNowExpiresCooling(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	until := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, status, disabled_until, updated_at) VALUES ('c', 'm', 'cooling', ?, 'now')`, until); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckNow(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := database.QueryRow(`SELECT status FROM model_states WHERE channel_id='c' AND model='m'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != statusAvailable {
		t.Fatalf("cooling state not expired: %q", status)
	}
}

func TestRecordSkipsModelsOutsideCatalog(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 渠道未探测（目录为空）：放行，保持历史行为。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m-probe", StatusCode: 429, Error: "rate limit"}); err != nil || class != "rate_limit" {
		t.Fatalf("unprobed channel must still record, class=%q err=%v", class, err)
	}

	// 渠道探测出目录 m1；m2 是目录外模型（如聚合目标里拼错的模型名）。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at) VALUES ('c','m1',1,'now','now')`); err != nil {
		t.Fatal(err)
	}

	// 目录外模型：失败、成功都不允许写入 model_states。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m2", StatusCode: 404, Error: "model not found"}); err != nil || class != "" {
		t.Fatalf("catalog-external failure should be skipped, got class=%q err=%v", class, err)
	}
	if err := service.RecordSuccess(ctx, "c", "m2"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c' AND model='m2'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("catalog-external model must not be recorded, got %d rows", count)
	}

	// 目录内模型照常记录。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m1", StatusCode: 429, Error: "rate limit"}); err != nil || class != "rate_limit" {
		t.Fatalf("catalog model must be recorded, class=%q err=%v", class, err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c' AND model='m1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("catalog model must be recorded, got %d rows", count)
	}
}

func TestCheckNowPurgesCatalogExternalStates(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 目录: m1；model_states 里同时有 m1 与幽灵 m2。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at) VALUES ('c','m1',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES ('c','m1',1,'available','now'),('c','m2',1,'cooling','now')`); err != nil {
		t.Fatal(err)
	}
	if err := service.CheckNow(ctx, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("CheckNow should purge catalog-external states, got %d rows", count)
	}
	var remaining string
	if err := database.QueryRow(`SELECT model FROM model_states WHERE channel_id='c'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != "m1" {
		t.Fatalf("remaining model should be m1, got %q", remaining)
	}
}
