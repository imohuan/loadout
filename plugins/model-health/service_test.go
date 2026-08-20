package modelhealth

import (
	"context"
	"database/sql"
	"strings"
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

// TestListExcludesDisabledCatalog 验证：channel_models 中 enabled=0 的模型
// 不应出现在模型状态里——只有"模型渠道"对外暴露的模型才进入模型状态。
// 历史 model_states 记录（catalog 里没有这一行的"幽灵"）仍然要展示。
func TestListExcludesDisabledCatalog(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 渠道目录：m1 启用，m2 禁用。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES
		('c','m1','probe',1,'now','now'),
		('c','m2','probe',0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	// 两个模型都曾在 model_states 留过历史（用户在编辑前都用过）。
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m1',1,'available','now'),
		('c','m2',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 个渠道，实际 %d", len(items))
	}
	got := map[string]bool{}
	for _, m := range items[0].Models {
		got[m.Model] = true
	}
	if !got["m1"] {
		t.Fatal("m1（启用）应在模型状态中")
	}
	if got["m2"] {
		t.Fatalf("m2（已被渠道禁用）不应在模型状态中，实际 models=%+v", items[0].Models)
	}
}

// TestListDropsCatalogExternalStates 验证：渠道已有模型目录（已探测/已编辑）时，
// model_states 里目录外的"幽灵"记录不再展示——模型状态严格以「模型渠道」为数据源。
func TestListDropsCatalogExternalStates(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 渠道目录：只有 m1 启用。
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES
		('c','m1','probe',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	// m2 是目录外"幽灵"：catalog 没有它，但 model_states 里有健康状态。
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m1',1,'available','now'),
		('c','m2',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 个渠道，实际 %d", len(items))
	}
	got := map[string]bool{}
	for _, m := range items[0].Models {
		got[m.Model] = true
	}
	if !got["m1"] {
		t.Fatalf("m1（渠道目录）应展示，实际 models=%+v", items[0].Models)
	}
	if got["m2"] {
		t.Fatalf("m2（目录外幽灵）不应展示，实际 models=%+v", items[0].Models)
	}
}

// TestListKeepsUnprobedChannelStates 验证：渠道从未探测过（channel_models 整个为空）时，
// model_states 的历史状态仍然展示（无目录可依的兜底），不被本次收紧误伤。
func TestListKeepsUnprobedChannelStates(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 渠道目录为空（未探测），model_states 有历史记录。
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m-hist',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("期望 1 个渠道，实际 %d", len(items))
	}
	got := map[string]bool{}
	for _, m := range items[0].Models {
		got[m.Model] = true
	}
	if !got["m-hist"] {
		t.Fatalf("未探测渠道的历史状态应兜底展示，实际 models=%+v", items[0].Models)
	}
}

// TestPurgeChannelStates 验证：编辑渠道全量替换模型清单后，被删除模型的
// 历史状态（幽灵）被清理，keep 中的保留，keep 为空时清空该渠道全部状态。
func TestPurgeChannelStates(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m1',1,'available','now'),
		('c','m2',1,'cooling','now'),
		('c','m3',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	if err := service.PurgeChannelStates(ctx, "c", []string{"m1"}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("应只保留 m1 的状态，实际 %d 行", count)
	}
	var remaining string
	if err := database.QueryRow(`SELECT model FROM model_states WHERE channel_id='c'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != "m1" {
		t.Fatalf("保留的应是 m1，实际 %q", remaining)
	}

	// keep 为空：清空全部。
	if err := service.PurgeChannelStates(ctx, "c", nil); err != nil {
		t.Fatalf("purge all: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("keep 为空应清空该渠道全部状态，实际 %d 行", count)
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
	// m3 只存在于 model_states（不在渠道目录）——已探测渠道的目录外状态
	// 不应混进模型状态，模型状态严格以「模型渠道」为数据源。
	if len(item.Models) != 2 {
		t.Fatalf("期望 2 个模型，实际 %d: %+v", len(item.Models), item.Models)
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
	if _, ok := byModel["m3"]; ok {
		t.Fatalf("m3 不在渠道目录，不应出现在模型状态: %+v", byModel["m3"])
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

func TestDeleteModelManualOnly(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES ('c','m-manual','manual',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES ('c','m-manual',1,'available','now')`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteModel(ctx, "c", "m-manual"); err != nil {
		t.Fatalf("manual delete: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM channel_models WHERE channel_id='c' AND model='m-manual'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("manual model should be deleted from catalog, got %d rows", count)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c' AND model='m-manual'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("model_states should be cleaned, got %d rows", count)
	}

	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES ('c','m-probe','probe',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteModel(ctx, "c", "m-probe"); err == nil || !strings.Contains(err.Error(), "only manual models can be deleted") {
		t.Fatalf("probe delete should fail with 'only manual', got: %v", err)
	}
	if err := service.DeleteModel(ctx, "c", "missing"); err == nil || !strings.Contains(err.Error(), "not in channel") {
		t.Fatalf("missing delete should fail, got: %v", err)
	}
}

func TestSetModelsEnabledBatch(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if err := service.SetModelsEnabled(ctx, "c", []string{"m1", "m2"}, true); err != nil {
		t.Fatal(err)
	}
	if err := service.SetModelsEnabled(ctx, "c", []string{"m1", "m3"}, false); err != nil {
		t.Fatal(err)
	}
	var manual bool
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='c' AND model='m1'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		t.Fatal("m1 应被批量关闭")
	}
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='c' AND model='m2'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if !manual {
		t.Fatal("m2 应保持开启")
	}
	if err := database.QueryRow(`SELECT manual_enabled FROM model_states WHERE channel_id='c' AND model='m3'`).Scan(&manual); err != nil {
		t.Fatal(err)
	}
	if manual {
		t.Fatal("m3 应被批量关闭")
	}
}

func TestRecoverModelsBatch(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, fail_count, last_error, disabled_until, updated_at) VALUES
		('c','m1',0,'cooling',3,'上游 503',?, 'now'),
		('c','m2',1,'disabled',1,'auth',NULL,'now')`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := service.RecoverModels(ctx, "c", []string{"m1", "m2"}); err != nil {
		t.Fatal(err)
	}
	// 恢复语义与单个 RecoverModel 一致：清自动熔断（status/fail_count），不强制改动手动开关。
	for _, m := range []string{"m1", "m2"} {
		var status string
		var failCount int
		var manual bool
		if err := database.QueryRow(`SELECT status, fail_count, manual_enabled FROM model_states WHERE channel_id='c' AND model='m1'`).Scan(&status, &failCount, &manual); err != nil {
			t.Fatal(err)
		}
		_ = m
	}
	var status string
	var failCount int
	var manual bool
	if err := database.QueryRow(`SELECT status, fail_count, manual_enabled FROM model_states WHERE channel_id='c' AND model='m1'`).Scan(&status, &failCount, &manual); err != nil {
		t.Fatal(err)
	}
	if status != statusAvailable || failCount != 0 {
		t.Fatalf("m1 自动熔断应清除, got status=%q fail=%d", status, failCount)
	}
	if manual {
		t.Fatal("m1 手动关闭不应被恢复操作强制打开")
	}
}

func TestDeleteModelsBatch(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, source, enabled, first_seen_at, last_seen_at) VALUES
		('c','m-manual','manual',1,'now','now'),
		('c','m-probe','probe',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m-manual',1,'available','now'),
		('c','m-probe',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	// 全 manual → 成功删除。
	if err := service.DeleteModels(ctx, "c", []string{"m-manual"}); err != nil {
		t.Fatalf("批量删除 manual: %v", err)
	}
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM channel_models WHERE channel_id='c' AND model='m-manual'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("manual 目录行应删除, got %d", count)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM model_states WHERE channel_id='c' AND model='m-manual'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("manual 状态行应删除, got %d", count)
	}

	// 含 probe → 整体回滚，probe 与已存在的 manual 都不删。
	if err := service.DeleteModels(ctx, "c", []string{"m-probe"}); err == nil || !strings.Contains(err.Error(), "only manual models can be deleted") {
		t.Fatalf("含 probe 批量删除应失败, got: %v", err)
	}
	if err := database.QueryRow(`SELECT COUNT(*) FROM channel_models WHERE channel_id='c' AND model='m-probe'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("probe 目录行应保留, got %d", count)
	}
}

// TestRecordFailureAuthDisablesChannel 多 key 语义：401/403(auth) 说明该 key 无效，
// 除模型级禁用外，必须把整条 key 记录（channel_states）置 disabled，否则路由仍会选它。
// 429/402(model_quota) 不得触发渠道级禁用（key 级冷却/禁模型，不连坐）。
func TestRecordFailureAuthDisablesChannel(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// auth：模型级 + 渠道级（key 记录）都应禁用。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m", StatusCode: 401, Error: "invalid api key"}); err != nil || class != "auth" {
		t.Fatalf("auth classification: %q %v", class, err)
	}
	var channelStatus string
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatalf("auth 应写入 channel_states: %v", err)
	}
	if channelStatus != statusDisabled {
		t.Fatalf("auth 应禁用整条 key 记录, got status=%q", channelStatus)
	}
	availability, err := service.Check(ctx, "c", "m")
	if err != nil {
		t.Fatal(err)
	}
	if availability.EffectiveAvailable || !strings.Contains(availability.Reason, "disabled") {
		t.Fatalf("auth 后渠道应不可用: %+v", availability)
	}

	// 429：只冷却模型，不碰渠道状态（另一个 key 不受影响）。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m2", StatusCode: 429, Error: "rate limit"}); err != nil || class != "rate_limit" {
		t.Fatalf("rate limit classification: %q %v", class, err)
	}
	var channelStatusAfter string
	if err := database.QueryRow(`SELECT COALESCE((SELECT status FROM channel_states WHERE channel_id='c'), 'available')`).Scan(&channelStatusAfter); err != nil {
		t.Fatal(err)
	}
	// 渠道已被 auth 禁用过（status=disabled），429 不应把它变成 cooling，也不应覆盖为 available。
	if channelStatusAfter != statusDisabled {
		t.Fatalf("rate limit 不得覆盖渠道状态, got %q", channelStatusAfter)
	}

	// 新渠道上 402 model_quota：不产生渠道级禁用。
	if _, err := database.Exec(`INSERT INTO channels(id, name, base_url, manual_enabled, sync_billing, created_at, updated_at) VALUES ('c2','C2','http://c2',1,0,'now','now')`); err != nil {
		t.Fatal(err)
	}
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c2", Model: "m", StatusCode: 402, Error: "quota exceeded"}); err != nil || class != "model_quota" {
		t.Fatalf("model quota classification: %q %v", class, err)
	}
	var c2Status sql.NullString
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c2'`).Scan(&c2Status); err != sql.ErrNoRows {
		t.Fatalf("model_quota 不应写入 channel_states: %v", err)
	}
	availability, err = service.Check(ctx, "c2", "m")
	if err != nil {
		t.Fatal(err)
	}
	if availability.EffectiveAvailable {
		t.Fatalf("model_quota 后该模型应不可用: %+v", availability)
	}
}

// TestRecordFailureAuthScope 收紧语义：纯 403（权限/封禁，无 invalid api key 文案）
// 只禁模型不禁渠道；401 才禁渠道。手动启用（SetChannelEnabled(true)）必须同时
// 清掉渠道自动熔断，否则被 auth 禁的 key 永远无法通过 UI 恢复。
func TestRecordFailureAuthScope(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	// 纯 403：不禁渠道。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m", StatusCode: 403, Error: "permission denied"}); err != nil || class != "auth" {
		t.Fatalf("403 classification: %q %v", class, err)
	}
	var channelStatus sql.NullString
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != sql.ErrNoRows {
		t.Fatalf("纯 403 不应写 channel_states: %v", err)
	}

	// 401：禁渠道。
	if class, err := service.RecordFailure(ctx, contracts.RouteFailure{ChannelID: "c", Model: "m2", StatusCode: 401, Error: "invalid api key"}); err != nil || class != "auth" {
		t.Fatalf("401 classification: %q %v", class, err)
	}
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus.String != statusDisabled {
		t.Fatalf("401 应禁渠道, got %q", channelStatus.String)
	}

	// 手动启用：清渠道自动熔断 + 该 key 下自动禁用的模型，路由恢复。
	if err := service.SetChannelEnabled(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`SELECT status FROM channel_states WHERE channel_id='c'`).Scan(&channelStatus); err != nil {
		t.Fatal(err)
	}
	if channelStatus.String != statusAvailable {
		t.Fatalf("手动启用后渠道应恢复 available, got %q", channelStatus.String)
	}
	availability, err := service.Check(ctx, "c", "m2")
	if err != nil {
		t.Fatal(err)
	}
	if !availability.EffectiveAvailable {
		t.Fatalf("手动启用后渠道应可用: %+v", availability)
	}

	// 用户手动禁用的模型（manual_enabled=0）不得被启用操作强制打开。
	if err := service.SetModelEnabled(ctx, "c", "manual-off", false); err != nil {
		t.Fatal(err)
	}
	if err := service.SetChannelEnabled(ctx, "c", true); err != nil {
		t.Fatal(err)
	}
	availability, err = service.Check(ctx, "c", "manual-off")
	if err != nil {
		t.Fatal(err)
	}
	if availability.ManualEnabled {
		t.Fatalf("手动禁用的模型不应被启用操作打开: %+v", availability)
	}
}
