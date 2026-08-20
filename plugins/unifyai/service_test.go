package unifyai

import (
	"log/slog"
	"strings"
	"testing"
)

// TestPlatformInfoFallback 验证 CLI 不可用时回落内置默认平台（页面仍可用）。
func TestPlatformInfoFallback(t *testing.T) {
	old := runCommandStream
	defer func() { runCommandStream = old }()
	// 模拟 CLI 执行失败（resolveCmd 找不到命令 → 回落默认）。
	svc := NewService(slog.Default())
	res, err := svc.PlatformInfo()
	if err != nil {
		t.Fatalf("PlatformInfo 不应返回错误（回落默认）: %v", err)
	}
	if len(res.Platforms) != 5 {
		t.Fatalf("默认平台数 = %d, want 5", len(res.Platforms))
	}
	byID := map[string]Platform{}
	for _, p := range res.Platforms {
		byID[p.ID] = p
	}
	if byID["codex"].ModelStatus != "not_supported" {
		t.Errorf("codex.modelStatus = %q, want not_supported", byID["codex"].ModelStatus)
	}
	if byID["reasonix"].McpStatus != "not_implemented" {
		t.Errorf("reasonix.mcpStatus = %q, want not_implemented", byID["reasonix"].McpStatus)
	}
	if byID["opencode"].ConfigPath == "" {
		t.Error("opencode.configPath 不应为空")
	}
}

// TestRunStreamsLogs 验证 Run 逐行回调 CLI 输出（stdout/stderr 合并）。
func TestRunStreamsLogs(t *testing.T) {
	old := runCommandStream
	defer func() { runCommandStream = old }()
	runCommandStream = func(name string, args []string, onLine func(string)) error {
		if name == "" || len(args) == 0 {
			t.Errorf("runCommandStream 收到空命令: %q %v", name, args)
		}
		onLine("line-a")
		onLine("line-b")
		return nil
	}
	svc := NewService(slog.Default())
	var got []string
	if err := svc.Run([]string{"--dry-run"}, func(line string) { got = append(got, line) }); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 3 { // 执行: 前缀 + 2 行
		t.Fatalf("日志行数 = %d, want 3; got %v", len(got), got)
	}
	if !strings.Contains(got[0], "执行:") {
		t.Errorf("首行应为执行命令展示, got %q", got[0])
	}
	if got[1] != "line-a" || got[2] != "line-b" {
		t.Errorf("日志顺序错误: %v", got[1:])
	}
}

// TestRunErrorPropagates 验证 Run 返回命令错误（非零退出）。
func TestRunErrorPropagates(t *testing.T) {
	old := runCommandStream
	defer func() { runCommandStream = old }()
	runCommandStream = func(name string, args []string, onLine func(string)) error {
		onLine("boom")
		return errFake
	}
	svc := NewService(slog.Default())
	err := svc.Run(nil, nil)
	if err == nil {
		t.Fatal("Run 应返回错误")
	}
	if !strings.Contains(err.Error(), "boom") && !strings.Contains(err.Error(), errFake.Error()) {
		t.Errorf("错误应包含原因, got %v", err)
	}
}

var errFake = errFakeType{}

type errFakeType struct{}

func (errFakeType) Error() string { return "fake command failed" }

// TestRunnerSingleInstanceAndBroadcast 验证单实例 + 多订阅者广播 + 终态关闭。
func TestRunnerSingleInstanceAndBroadcast(t *testing.T) {
	old := runCommandStream
	defer func() { runCommandStream = old }()
	runCommandStream = func(name string, args []string, onLine func(string)) error {
		onLine("log-1")
		return nil
	}
	svc := NewService(slog.Default())
	svc.SetArgs([]string{"--all"})

	ch1, err := svc.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	ch2, err := svc.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe 2: %v", err)
	}
	var ev1, ev2 []RunEvent
	for ev := range ch1 {
		ev1 = append(ev1, ev)
	}
	for ev := range ch2 {
		ev2 = append(ev2, ev)
	}
	if len(ev1) != 4 { // log 开始 + 执行: 命令 + log-1 + done
		t.Fatalf("订阅者1 事件数 = %d, want 4; got %v", len(ev1), ev1)
	}
	if ev1[len(ev1)-1].Type != "done" {
		t.Errorf("终态应为 done, got %+v", ev1[len(ev1)-1])
	}
	if len(ev2) != len(ev1) {
		t.Errorf("订阅者2 事件数 = %d, want %d（广播一致性）", len(ev2), len(ev1))
	}
}
