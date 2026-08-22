package aggregate

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loadout/core/db"
	"loadout/core/store"
	modelgateway "loadout/plugins/model-gateway"
	"loadout/plugins/types"
)

func TestFindAggregate(t *testing.T) {
	// 创建临时目录
	tmpDir := t.TempDir()
	st, err := store.New(tmpDir)
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	lg := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))

	svc := NewService(st, lg, nil)

	// 写入测试配置
	aggs := []types.AggregateModel{
		{
			Name: "auto",
			Targets: []types.AggregateTarget{
				{Model: "gpt-4", ChannelID: "ch-openai"},
				{Model: "claude-3", ChannelID: "ch-anthropic"},
			},
		},
		{
			Name: "fast",
			Targets: []types.AggregateTarget{
				{Model: "gpt-3.5-turbo", ChannelID: "ch-openai"},
			},
		},
	}
	if err := st.Write(types.FileAggregates, aggs); err != nil {
		t.Fatalf("写入测试配置失败: %v", err)
	}

	// 测试：命中 auto
	agg, err := svc.findAggregate("auto")
	if err != nil {
		t.Fatalf("查找 auto 失败: %v", err)
	}
	if agg == nil {
		t.Fatal("auto 应该存在但返回 nil")
	}
	if agg.Name != "auto" || len(agg.Targets) != 2 {
		t.Errorf("auto 配置不符: got %+v", agg)
	}

	// 测试：命中 fast
	agg, err = svc.findAggregate("fast")
	if err != nil {
		t.Fatalf("查找 fast 失败: %v", err)
	}
	if agg == nil {
		t.Fatal("fast 应该存在但返回 nil")
	}

	// 测试：未命中
	agg, err = svc.findAggregate("nonexist")
	if err != nil {
		t.Fatalf("查找 nonexist 失败: %v", err)
	}
	if agg != nil {
		t.Errorf("nonexist 不应该存在但返回: %+v", agg)
	}

	// 测试：文件不存在
	os.Remove(filepath.Join(tmpDir, types.FileAggregates))
	agg, err = svc.findAggregate("auto")
	if err != nil {
		t.Fatalf("文件不存在时应返回 nil 而非错误: %v", err)
	}
	if agg != nil {
		t.Errorf("文件不存在时应返回 nil: %+v", agg)
	}
}

// TestHandleUpstreamSucceeded 将聚合目标的成功转发标记为可用。
func TestHandleUpstreamSucceeded(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	key := "deepseek-v4-pro-ga-260813@volcengine"
	svc.healthMap[key] = &types.ModelHealth{
		Model:     key,
		Status:    "cooling",
		FailCount: 2,
		LastError: "上游返回 503",
	}

	payload := &modelgateway.SuccessPayload{
		Pipe: &modelgateway.Pipeline{Metadata: map[string]any{"__virtual_model": "auto-demo"}},
		Model:     "deepseek-v4-pro-ga-260813",
		ChannelID: "volcengine",
	}
	if _, err := svc.HandleUpstreamSucceeded(payload); err != nil {
		t.Fatalf("处理成功事件失败: %v", err)
	}

	health := svc.healthMap[key]
	if health == nil || health.Status != "available" || health.FailCount != 0 || health.LastError != "" || health.DisabledUntil != nil {
		t.Fatalf("成功后健康状态不正确: %+v", health)
	}
}

// TestSelectAvailableTargetSkipsUnavailable 回归：不可用（冷却/禁用）的目标不应被选中
// 发起真实请求——不可用时直接记录日志，不打上游。
func TestSelectAvailableTargetSkipsUnavailable(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)

	targets := []types.AggregateTarget{
		{Model: "m1", ChannelID: "c1"},
		{Model: "m2", ChannelID: "c2"},
	}
	// 第一个目标冷却中（未到期）→ 不可选
	until := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	svc.healthMap["m1@c1"] = &types.ModelHealth{
		Model:         "m1@c1",
		Status:        "cooling",
		FailCount:     3,
		DisabledUntil: &until,
	}
	// 第二个目标已禁用 → 不可选
	svc.healthMap["m2@c2"] = &types.ModelHealth{
		Model:     "m2@c2",
		Status:    "disabled",
		FailCount: 5,
	}

	target, candidates, err := svc.selectAvailableTarget(targets, nil)
	if err != nil {
		t.Fatalf("selectAvailableTarget 报错: %v", err)
	}
	if target != nil {
		t.Fatalf("不可用目标不应被选中，实际 %+v", target)
	}

	// 冷却已过期的目标应视为可用
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	svc.healthMap["m1@c1"].DisabledUntil = &expired
	target, candidates, err = svc.selectAvailableTarget(targets, nil)
	if err != nil {
		t.Fatalf("selectAvailableTarget 报错: %v", err)
	}
	if target == nil || target.Model != "m1" {
		t.Fatalf("冷却过期的目标应被选中，实际 %+v", target)
	}
	if target.ChannelID != "c1" {
		t.Fatalf("Key 级目标应具体化为 c1，实际 %+v", target)
	}
	if len(candidates) != 0 {
		t.Fatalf("Key 级目标不应有 candidates，实际 %v", candidates)
	}
}

// TestSelectAvailableTargetSkipsModelLevelFailed 回归：candidates=0 早退时
// tryProxyAggregateFailover 的 channelID 为空，failedKeys 记成 "model@"（无渠道 ID）。
// selectAvailableTarget 必须跳过该模型的所有 Key 选下一个目标，否则死循环永远选中同一个。
func TestSelectAvailableTargetSkipsModelLevelFailed(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)

	targets := []types.AggregateTarget{
		{Model: "m1", ChannelID: "c1"},
		{Model: "m2", ChannelID: "c2"},
	}
	// m1 无可用渠道失败（candidates=0 早退），failedKeys 记 "m1@"（channelID 为空）
	failed := []string{"m1@"}

	target, _, err := svc.selectAvailableTarget(targets, failed)
	if err != nil {
		t.Fatalf("selectAvailableTarget 报错: %v", err)
	}
	if target == nil || target.Model != "m2" {
		t.Fatalf("应跳过 m1 选中 m2，实际 %+v", target)
	}
}

// TestSelectAvailableTargetChannelLevel 渠道级目标：组内 Key 逐一健康检查，
// 返回渠道级 target + 可用 Key 列表；组内全部不可用则跳过该 target。
func TestSelectAvailableTargetChannelLevel(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	// JSON 模式渠道表：同一 base_url 两个 Key（k1 冷却中、k2 可用）。
	if err := st.Write(types.FileChannels, []map[string]any{
		{"id": "k1", "name": "Key1", "channel_name": "volc", "base_url": "https://volc.example/v1", "api_key_cipher": "x", "enabled": true},
		{"id": "k2", "name": "Key2", "channel_name": "volc", "base_url": "https://volc.example/v1", "api_key_cipher": "x", "enabled": true},
		{"id": "k3", "name": "Key3", "base_url": "https://volc.example/v1", "enabled": false},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)

	targets := []types.AggregateTarget{
		{Model: "m1", ChannelBaseURL: "https://volc.example/v1"},
		{Model: "m2", ChannelID: "k2"},
	}
	// k1 冷却中（未到期）→ 不可选；k2 无健康记录 → 可用。
	until := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	svc.healthMap["m1@k1"] = &types.ModelHealth{
		Model: "m1@k1", Status: "cooling", FailCount: 2, DisabledUntil: &until,
	}

	target, candidates, err := svc.selectAvailableTarget(targets, nil)
	if err != nil {
		t.Fatalf("selectAvailableTarget 报错: %v", err)
	}
	if target == nil || target.Model != "m1" {
		t.Fatalf("渠道级目标应被选中，实际 %+v", target)
	}
	if target.ChannelBaseURL == "" {
		t.Fatalf("渠道级 target 应保留 ChannelBaseURL，实际 %+v", target)
	}
	if len(candidates) != 1 || candidates[0] != "k2" {
		t.Fatalf("candidates = %v, want [k2]（k1 冷却中应被跳过）", candidates)
	}
}

// TestSelectAvailableTargetChannelLevelAllDown 渠道级组内全部不可用 → 跳过该 target。
func TestSelectAvailableTargetChannelLevelAllDown(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	if err := st.Write(types.FileChannels, []map[string]any{
		{"id": "k1", "name": "Key1", "channel_name": "volc", "base_url": "https://volc.example/v1", "enabled": true},
		{"id": "k2", "name": "Key2", "channel_name": "volc", "base_url": "https://volc.example/v1", "enabled": true},
	}); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)

	targets := []types.AggregateTarget{
		{Model: "m1", ChannelBaseURL: "https://volc.example/v1"}, // 组内两个 Key 都禁用
		{Model: "m2", ChannelID: "c-ok"},                          // 无健康记录 → 可用
	}
	disabled := time.Now().Add(10 * time.Minute).Format(time.RFC3339)
	svc.healthMap["m1@k1"] = &types.ModelHealth{Model: "m1@k1", Status: "disabled", DisabledUntil: &disabled}
	svc.healthMap["m1@k2"] = &types.ModelHealth{Model: "m1@k2", Status: "disabled", DisabledUntil: &disabled}

	target, _, err := svc.selectAvailableTarget(targets, nil)
	if err != nil {
		t.Fatalf("selectAvailableTarget 报错: %v", err)
	}
	if target == nil || target.Model != "m2" {
		t.Fatalf("应跳过渠道级目标选 m2，实际 %+v", target)
	}
}

// TestFindAggregateDBChannelLevel DB 模式（SQLite）下渠道级/Key 多选字段必须完整保留，
// 防止 findAggregate 拷贝时丢字段导致生产 503（回归防护）。
func TestFindAggregateDBChannelLevel(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("创建 store 失败: %v", err)
	}
	database, err := db.OpenMemory()
	if err != nil {
		t.Fatalf("打开内存库失败: %v", err)
	}
	defer database.Close()
	repo, err := db.NewRepository(database)
	if err != nil {
		t.Fatalf("创建 repo 失败: %v", err)
	}
	ctx := context.Background()
	channels := []db.Channel{
		{ID: "k1", Name: "Key1", BaseURL: "https://volc.example/v1", ManualEnabled: true},
		{ID: "k2", Name: "Key2", BaseURL: "https://volc.example/v1", ManualEnabled: true},
	}
	if err := repo.ReplaceChannels(ctx, channels); err != nil {
		t.Fatalf("写渠道失败: %v", err)
	}
	if err := repo.ReplaceAggregates(ctx, []db.Aggregate{
		{Name: "auto", Enabled: true, Targets: []db.AggregateTarget{
			{Model: "m1", ChannelBaseURL: "https://volc.example/v1"},
			{Model: "m2", ChannelIDs: []string{"k1", "k2"}},
			{Model: "m3", ChannelID: "k1"},
		}},
	}); err != nil {
		t.Fatalf("写聚合失败: %v", err)
	}
	svc := NewService(st, slog.Default(), nil)
	svc.routing = repo

	agg, err := svc.findAggregate("auto")
	if err != nil {
		t.Fatalf("findAggregate 报错: %v", err)
	}
	if agg == nil || len(agg.Targets) != 3 {
		t.Fatalf("聚合目标数 = %d, want 3", len(agg.Targets))
	}
	if agg.Targets[0].ChannelBaseURL != "https://volc.example/v1" {
		t.Fatalf("target[0] 渠道级字段丢失: %+v", agg.Targets[0])
	}
	if len(agg.Targets[1].ChannelIDs) != 2 || agg.Targets[1].ChannelIDs[0] != "k1" {
		t.Fatalf("target[1] Key 多选字段丢失: %+v", agg.Targets[1])
	}
	if agg.Targets[2].ChannelID != "k1" {
		t.Fatalf("target[2] 单 Key 字段丢失: %+v", agg.Targets[2])
	}
}
