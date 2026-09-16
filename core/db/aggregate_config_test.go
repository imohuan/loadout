package db

import (
	"context"
	"path/filepath"
	"testing"
)

// TestAggregateConfigRoundTrip 聚合模型的「模型配置」（对外 /v1/models 那一行的属性）
// 随聚合模型一起持久化：写入后读回，数值与能力标记必须原样保留。
// 这是配置虚拟模型上下文/能力的数据底座，读回丢字段就等于配置没生效。
func TestAggregateConfigRoundTrip(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	repo, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.Exec("INSERT INTO channels(id, name, base_url, created_at, updated_at) VALUES ('alpha', 'Alpha', 'https://alpha.example', 'now', 'now')"); err != nil {
		t.Fatal(err)
	}

	want := &AggregateConfig{
		ContextLength:   1048576,
		MaxOutputTokens: 384000,
		Capabilities:    []string{CapabilityVision, CapabilityReasoning},
	}
	if err := repo.ReplaceAggregates(ctx, []Aggregate{{
		Name:    "auto",
		Enabled: true,
		Config:  want,
		Targets: []AggregateTarget{{Model: "gpt-test", ChannelID: "alpha"}},
	}}); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListAggregates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("聚合模型数 = %d, want 1", len(got))
	}
	if got[0].Config == nil {
		t.Fatal("Config 未持久化：读回为 nil")
	}
	if got[0].Config.ContextLength != want.ContextLength {
		t.Errorf("上下文长度 = %d, want %d", got[0].Config.ContextLength, want.ContextLength)
	}
	if got[0].Config.MaxOutputTokens != want.MaxOutputTokens {
		t.Errorf("最大输出 = %d, want %d", got[0].Config.MaxOutputTokens, want.MaxOutputTokens)
	}
	if !got[0].Config.HasCapability(CapabilityVision) || !got[0].Config.HasCapability(CapabilityReasoning) {
		t.Errorf("能力标记丢失: %v", got[0].Config.Capabilities)
	}
	if got[0].Config.HasCapability(CapabilityToolUse) {
		t.Errorf("未勾选的能力不应出现: %v", got[0].Config.Capabilities)
	}
}

// TestAggregateConfigOmittedWhenEmpty 没配模型配置的聚合模型读回 Config 必须为 nil，
// 这样 /v1/models 才知道「不额外声明任何字段」，与改造前行为一致。
func TestAggregateConfigOmittedWhenEmpty(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	repo, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.Exec("INSERT INTO channels(id, name, base_url, created_at, updated_at) VALUES ('alpha', 'Alpha', 'https://alpha.example', 'now', 'now')"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceAggregates(ctx, []Aggregate{{
		Name:    "auto",
		Enabled: true,
		Targets: []AggregateTarget{{Model: "gpt-test", ChannelID: "alpha"}},
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListAggregates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("聚合模型数 = %d, want 1", len(got))
	}
	if got[0].Config != nil {
		t.Fatalf("未配置模型配置时应读回 nil, got %+v", got[0].Config)
	}
}

// TestAggregateConfigCorruptJSONDoesNotBreakList 数据库里出现损坏的 config_json 时，
// 列表必须照常返回（该条按「未配置」处理）——不能让一条脏数据把所有虚拟模型
// 从 /v1/models 里抹掉。
func TestAggregateConfigCorruptJSONDoesNotBreakList(t *testing.T) {
	database := mustOpen(t, filepath.Join(t.TempDir(), "loadout.db"))
	defer database.Close()
	repo, err := NewRepository(database)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := database.Exec("INSERT INTO channels(id, name, base_url, created_at, updated_at) VALUES ('alpha', 'Alpha', 'https://alpha.example', 'now', 'now')"); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceAggregates(ctx, []Aggregate{{
		Name:    "good",
		Enabled: true,
		Config:  &AggregateConfig{ContextLength: 1000},
		Targets: []AggregateTarget{{Model: "gpt-test", ChannelID: "alpha"}},
	}}); err != nil {
		t.Fatal(err)
	}
	// 直接写一条坏 JSON 模拟历史脏数据（正常写入路径不会产生）。
	if _, err := database.Exec("UPDATE aggregates SET config_json = '{broken' WHERE name = 'good'"); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListAggregates(ctx)
	if err != nil {
		t.Fatalf("坏配置不应导致列表读取失败: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("列表应照常返回 1 条, got %d", len(got))
	}
	if got[0].Config != nil {
		t.Fatalf("坏配置应按「未配置」处理, got %+v", got[0].Config)
	}
}
