package aggregate

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

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

	target := svc.selectAvailableTarget(targets, nil)
	if target != nil {
		t.Fatalf("不可用目标不应被选中，实际 %+v", target)
	}

	// 冷却已过期的目标应视为可用
	expired := time.Now().Add(-time.Minute).Format(time.RFC3339)
	svc.healthMap["m1@c1"].DisabledUntil = &expired
	target = svc.selectAvailableTarget(targets, nil)
	if target == nil || target.Model != "m1" {
		t.Fatalf("冷却过期的目标应被选中，实际 %+v", target)
	}
}
