package modelhealth

import (
	"context"
	"testing"
	"time"

	"loadout/plugins/contracts"
)

// TestListModelInheritsKeyDisable 用户报告：同一个模型在 17 个账号上，
// 其中 12 个 Key 已因 401/403 自动熔断、5 个 429 冷却。模型行却显示 available，
// 页面看起来「除了一个账号都正常」，实际这些账号根本不会参与路由。
//
// 模型行的有效可用性必须叠加账号级状态：Key 被禁用时，它名下的模型一律不可用，
// 并把账号级成因带到模型行，避免用户误判。
func TestListModelInheritsKeyDisable(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(
		`INSERT INTO channel_states(channel_id, status, fail_count, last_failure_class, last_error, updated_at)
		 VALUES ('c','disabled',1,'auth','invalid api key','now')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at)
		 VALUES ('c','m1',1,'now','now')`,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at)
		 VALUES ('c','m1',1,'available','now')`,
	); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || len(items[0].Models) != 1 {
		t.Fatalf("unexpected items: %+v", items)
	}
	m := items[0].Models[0]
	if m.Health.EffectiveAvailable {
		t.Fatalf("账号已禁用时模型不能显示为可用: %+v", m)
	}
	if m.FailureClass != "auth" || m.LastError != "invalid api key" {
		t.Fatalf("应把账号级成因带到模型行: %+v", m)
	}
	if items[0].LastError != "invalid api key" {
		t.Fatalf("渠道行应带账号级最近错误: %+v", items[0])
	}
}

// TestListModelInheritsKeyCooldown 账号被 429 冷却（有截止时间）时，模型行
// 不仅要不可用，还要带恢复时间；否则 UI 会显示「需手动恢复」，实际会自愈。
func TestListModelInheritsKeyCooldown(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)

	if _, err := database.Exec(
		`INSERT INTO channel_states(channel_id, status, disabled_until, last_failure_class, updated_at)
		 VALUES ('c','cooling',?,'rule_disable_key_daily','now')`,
		until.Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(
		`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at)
		 VALUES ('c','m1',1,'now','now')`,
	); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m := items[0].Models[0]
	if m.Health.EffectiveAvailable {
		t.Fatalf("账号冷却时模型不能显示为可用: %+v", m)
	}
	if m.DisabledUntil == nil || !m.DisabledUntil.Equal(until) {
		t.Fatalf("应继承账号恢复时间 %v，实际 %+v", until, m.DisabledUntil)
	}
}

// TestCheckModelRejectsWhenKeyDisabled Check 是路由的真正闸门，同样必须返回
// 账号级原因（已有实现），这里锁住 reason 供 UI/日志复用。
func TestCheckModelRejectsWhenKeyDisabled(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()
	if _, err := database.Exec(
		`INSERT INTO channel_states(channel_id, status, disabled_until, updated_at)
		 VALUES ('c','cooling',?,'now')`,
		time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano),
	); err != nil {
		t.Fatal(err)
	}
	a, err := service.Check(ctx, "c", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if a.EffectiveAvailable {
		t.Fatalf("账号冷却时模型不应可用: %+v", a)
	}
	if a.Reason != "channel cooling" {
		t.Fatalf("reason = %q, want channel cooling", a.Reason)
	}
}

// TestCheckModelKeepsOtherModelsWhenModelDisabled 用户核心担心：
// 一个账号里某个模型额度/故障被停用，访问同账号其他模型必须仍可路由。
//
// 模型级 disable_model 只写该模型；Check 先查账号、再查模型，账号正常时
// 其他模型不能因为兄弟模型的状态被连坐。
func TestCheckModelKeepsOtherModelsWhenModelDisabled(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, updated_at) VALUES
		('c','m1',1,'disabled','now'),
		('c','m2',1,'available','now')`); err != nil {
		t.Fatal(err)
	}

	bad, err := service.Check(ctx, "c", "m1")
	if err != nil {
		t.Fatal(err)
	}
	if bad.EffectiveAvailable {
		t.Fatalf("m1 已被模型级禁用，不应可用: %+v", bad)
	}

	ok, err := service.Check(ctx, "c", "m2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok.EffectiveAvailable {
		t.Fatalf("m2 未故障，账号正常时必须可用（不能因 m1 连坐）: %+v", ok)
	}
}

// TestListMarksFailureScope 页面必须能区分账号级与模型级，避免用户把
// 「账号停了」误读成「模型坏了」，或反过来。
func TestListMarksFailureScope(t *testing.T) {
	database := healthDB(t)
	service := NewService(database, nil)
	ctx := context.Background()

	if _, err := database.Exec(`INSERT INTO channel_models(channel_id, model, enabled, first_seen_at, last_seen_at) VALUES
		('c','m1',1,'now','now'),('c','m2',1,'now','now')`); err != nil {
		t.Fatal(err)
	}
	// m1 模型级禁用；账号本身正常。
	if _, err := database.Exec(`INSERT INTO model_states(channel_id, model, manual_enabled, status, last_failure_class, updated_at) VALUES
		('c','m1',1,'disabled','rule_disable_model_never','now')`); err != nil {
		t.Fatal(err)
	}

	items, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]contracts.ModelStatus{}
	for _, m := range items[0].Models {
		byModel[m.Model] = m
	}
	if byModel["m1"].Health.EffectiveAvailable {
		t.Fatalf("m1 应不可用: %+v", byModel["m1"])
	}
	if !byModel["m2"].Health.EffectiveAvailable {
		t.Fatalf("m2 必须可用（模型级故障不连坐）: %+v", byModel["m2"])
	}
}
