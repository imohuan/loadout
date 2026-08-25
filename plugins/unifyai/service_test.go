package unifyai

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loadout/core/config"
)

// TestPlatformInfoFallback 验证 CLI 不可用时回落内置默认平台（页面仍可用）。
// 强制 config.UnifyaiCmd 指向必然失败的命令，确保走 CLI 执行失败→回落默认 6 平台分支，
// 避免受本机真实 CLI 版本影响。
func TestPlatformInfoFallback(t *testing.T) {
	oldCmd := config.UnifyaiCmd
	config.UnifyaiCmd = "nonexistent-command-that-will-fail"
	defer func() { config.UnifyaiCmd = oldCmd }()
	svc := NewService(slog.Default())
	res, err := svc.PlatformInfo()
	if err != nil {
		t.Fatalf("PlatformInfo 不应返回错误（回落默认）: %v", err)
	}
	if len(res.Platforms) != 6 {
		t.Fatalf("默认平台数 = %d, want 6（含 workbuddy）", len(res.Platforms))
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
	if byID["workbuddy"].ID == "" {
		t.Error("默认平台列表应包含 workbuddy")
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

// TestFindNpxFallback 验证 PATH 不完整(LookPath 失败)时,
// 能按常见安装位置兜底找到 npx 完整路径(修复后台服务找不到 npx 的问题)。
func TestFindNpxFallback(t *testing.T) {
	old := npxCandidates
	defer func() { npxCandidates = old }()
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing", "npx")
	good := filepath.Join(dir, "npx")
	if err := os.WriteFile(good, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	npxCandidates = func() []string { return []string{missing, good} }
	if got := findNpxFallback(); got != good {
		t.Fatalf("findNpxFallback = %q, want %q", got, good)
	}
}

// TestEnvWithBinDir 验证 PATH 补充:命令所在目录放到最前,
// 保证 npx 子进程能找到同目录的 node(后台服务 PATH 不完整时)。
func TestEnvWithBinDir(t *testing.T) {
	env := envWithBinDir(filepath.Join(string(filepath.Separator), "home", "u", ".nvm", "versions", "node", "v20.10.0", "bin", "npx"))
	wantPrefix := filepath.Join(string(filepath.Separator), "home", "u", ".nvm", "versions", "node", "v20.10.0", "bin") + string(os.PathListSeparator)
	found := false
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i >= 0 && strings.EqualFold(kv[:i], "PATH") {
			if !strings.HasPrefix(strings.TrimPrefix(kv, kv[:i+1]), wantPrefix) {
				t.Errorf("PATH 应以命令目录开头, got %q", kv)
			}
			found = true
		}
	}
	if !found {
		t.Error("env 中应包含 PATH 条目")
	}
}

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

// TestSaveMcpServersSingleSwitchField 验证写回 mcp.json 只含 disabled 单开关字段
// （unifyai 同步时 loadMcpConfig 以 `if (!config.disabled)` 过滤，enabled 不写）。
func TestSaveMcpServersSingleSwitchField(t *testing.T) {
	old := mcpConfigPath
	defer func() { mcpConfigPath = old }()
	dir := t.TempDir()
	mcpConfigPath = func() string { return filepath.Join(dir, "mcp.json") }

	svc := NewService(slog.Default())
	if err := svc.SaveMcpServers([]McpServer{
		{Name: "a", Type: "local", Enabled: false, Command: []string{"cmd"}},
		{Name: "b", Type: "remote", Enabled: true, URL: "https://x", Headers: map[string]string{"k": "v"}},
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "mcp.json"))
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("json: %v", err)
	}
	ms := doc["mcpServers"].(map[string]any)
	for _, name := range []string{"a", "b"} {
		entry := ms[name].(map[string]any)
		if _, hasEnabled := entry["enabled"]; hasEnabled {
			t.Errorf("%s: 不应写出 enabled 字段", name)
		}
	}
	if ms["a"].(map[string]any)["disabled"] != true {
		t.Errorf("a.disabled = %v, want true", ms["a"].(map[string]any)["disabled"])
	}
	if ms["b"].(map[string]any)["disabled"] != false {
		t.Errorf("b.disabled = %v, want false", ms["b"].(map[string]any)["disabled"])
	}
}

// TestSplitCommandLine 验证 LOADOUT_UNIFYAI_CMD 的命令行分词（空格 + 双引号路径）。
func TestSplitCommandLine(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`node D:/Code/Git/unifyai/src/cli.mjs`, []string{"node", "D:/Code/Git/unifyai/src/cli.mjs"}},
		{`node "C:/Program Files/node/node.exe" --flag`, []string{"node", "C:/Program Files/node/node.exe", "--flag"}},
		{`npx -y unifyai@latest`, []string{"npx", "-y", "unifyai@latest"}},
		{``, nil},
	}
	for _, c := range cases {
		got := splitCommandLine(c.in)
		if len(got) != len(c.want) {
			t.Errorf("splitCommandLine(%q) = %v, want %v", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("splitCommandLine(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}

// TestResolveCmdConfigured 验证配置了 LOADOUT_UNIFYAI_CMD 时优先使用配置命令。
func TestResolveCmdConfigured(t *testing.T) {
	old := config.UnifyaiCmd
	config.UnifyaiCmd = `node D:/Code/Git/unifyai/src/cli.mjs`
	defer func() { config.UnifyaiCmd = old }()

	cmd, base, err := resolveCmd()
	if err != nil {
		t.Fatalf("resolveCmd 不应报错: %v", err)
	}
	if cmd != "node" {
		t.Errorf("cmd = %q, want node", cmd)
	}
	if strings.Join(base, " ") != "D:/Code/Git/unifyai/src/cli.mjs" {
		t.Errorf("base = %v, want [D:/Code/Git/unifyai/src/cli.mjs]", base)
	}
}
