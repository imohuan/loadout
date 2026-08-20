// Package unifyai 实现 UnifyAI 配置同步 CLI 的桥接服务：
// 把 Loadout 管理后台的「UnifyAI 配置同步」页面连接到本地 unifyai CLI，
// 提供平台能力列表（--list-platforms --json）与指令执行（含实时日志流）。
package unifyai

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"loadout/core/config"
)

// Platform 对应 `unifyai --list-platforms --json` 输出的单个平台（附录 B.1）。
type Platform struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	SupportsModels bool  `json:"supportsModels"`
	ModelStatus   string `json:"modelStatus"`
	SupportsMcp   bool   `json:"supportsMcp"`
	McpStatus     string `json:"mcpStatus"`
	ConfigPath    string `json:"configPath"`
	ConfigFormat  string `json:"configFormat"`
}

// ListPlatformsResult 对应 --list-platforms --json 的完整输出。
type ListPlatformsResult struct {
	Platforms []Platform `json:"platforms"`
}

// Service 是 UnifyAI CLI 桥接服务。
type Service struct {
	lg     *slog.Logger
	runner *RunRunner
	mu     sync.Mutex
	// pendingArgs 保存「启动任务前」由 handler 设置的 CLI 参数，
	// runner 启动任务时取出（takePendingArgs 一次性消费）。
	pendingArgs []string
}

// NewService 创建服务。
func NewService(lg *slog.Logger) *Service {
	svc := &Service{lg: lg}
	svc.runner = newRunRunner(svc)
	return svc
}

// SetArgs 设置下一次任务的 CLI 参数（启动前调用）。
func (s *Service) SetArgs(args []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingArgs = append([]string(nil), args...)
}

// takePendingArgs 取出并清空待执行参数。
func (s *Service) takePendingArgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	args := s.pendingArgs
	s.pendingArgs = nil
	return args
}

// Subscribe 订阅任务日志流（SSE 用）；无任务在跑时自动启动一个。
func (s *Service) Subscribe() (<-chan RunEvent, error) {
	return s.runner.Subscribe()
}

// PlatformInfo 解析 `unifyai --list-platforms --json`，返回平台能力列表。
// CLI 不可用时回落到内置默认平台（与 UI 静态数据一致），保证页面可用。
func (s *Service) PlatformInfo() (ListPlatformsResult, error) {
	cmd, base, err := resolveCmd()
	if err != nil {
		s.lg.Warn("unifyai: 获取平台列表回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	args := append(append([]string{}, base...), "--list-platforms", "--json")
	out, err := exec.Command(cmd, args...).Output()
	if err != nil {
		s.lg.Warn("unifyai: --list-platforms 失败，回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	var res ListPlatformsResult
	if err := json.Unmarshal(out, &res); err != nil {
		s.lg.Warn("unifyai: 解析平台列表 JSON 失败，回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	if len(res.Platforms) == 0 {
		return defaultPlatforms(), nil
	}
	return res, nil
}

// Run 执行一次 unifyai 指令（args 为 CLI 参数，如 ["--all", "--dry-run"]），
// 实时把 stdout/stderr 逐行回传给 onLog。返回进程退出错误（nil=成功）。
func (s *Service) Run(args []string, onLog func(string)) error {
	if onLog == nil {
		onLog = func(string) {}
	}
	cmd, base, err := resolveCmd()
	if err != nil {
		return err
	}
	onLog(fmt.Sprintf("执行: %s %s", displayCmd(cmd, base), strings.Join(args, " ")))
	full := append(append([]string{}, base...), args...)
	if err := runCommandStream(cmd, full, onLog); err != nil {
		return fmt.Errorf("unifyai: %w", err)
	}
	return nil
}

// resolveCmd 解析 unifyai 可执行入口：
//  1. 显式配置 UnifyaiDir（src/cli.mjs 存在）→ node + 该路径；
//  2. PATH 中的 unifyai → 直接执行；
//  3. 常见仓库位置（D:/Code/Git/unifyai 等）→ node + cli.mjs。
func resolveCmd() (string, []string, error) {
	if dir := config.UnifyaiDir; dir != "" {
		if cli := filepath.Join(dir, "src", "cli.mjs"); fileExists(cli) {
			return nodeCmd(cli)
		}
	}
	if p, err := exec.LookPath("unifyai"); err == nil {
		return p, nil, nil
	}
	for _, dir := range []string{"D:/Code/Git/unifyai", "C:/Code/unifyai", "~/unifyai"} {
		cli := filepath.Join(dir, "src", "cli.mjs")
		if fileExists(cli) {
			return nodeCmd(cli)
		}
	}
	return "", nil, fmt.Errorf("未找到 unifyai 命令：请安装 unifyai（npm i -g）或设置 LOADOUT_UNIFYAI_DIR")
}

// nodeCmd 返回 node 执行器 + cli.mjs 路径（Windows 下扩展名补全）。
func nodeCmd(cli string) (string, []string, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		// Windows 常见安装位置兜底。
		if runtime.GOOS == "windows" {
			for _, p := range []string{
				"C:/Program Files/nodejs/node.exe",
				filepath.Join(os.Getenv("APPDATA"), "npm", "node.exe"),
			} {
				if fileExists(p) {
					return p, []string{cli}, nil
				}
			}
		}
		return "", nil, fmt.Errorf("未找到 node：无法运行 unifyai CLI")
	}
	return node, []string{cli}, nil
}

// fileExists 判断路径存在且是普通文件。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// displayCmd 生成人类可读的命令展示（长路径缩短为 unifyai）。
func displayCmd(cmd string, base []string) string {
	if len(base) > 0 {
		return "node " + strings.Join(base, " ")
	}
	return "unifyai"
}

// defaultPlatforms 内置默认平台（CLI 不可用时的 UI 兜底，与附录 B.1 一致）。
func defaultPlatforms() ListPlatformsResult {
	return ListPlatformsResult{Platforms: []Platform{
		{ID: "opencode", Name: "OpenCode", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.config/opencode/opencode.json", ConfigFormat: "jsonc"},
		{ID: "codex", Name: "Codex", SupportsModels: false, ModelStatus: "not_supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.codex/config.toml", ConfigFormat: "toml"},
		{ID: "claudecode", Name: "Claude Code", SupportsModels: false, ModelStatus: "not_supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.claude.json", ConfigFormat: "json"},
		{ID: "reasonix", Name: "Reasonix", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "not_implemented", ConfigPath: "~/AppData/Roaming/reasonix/config.toml", ConfigFormat: "toml"},
		{ID: "penguin", Name: "PenguinHarness", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.penguin/data/default_project/.project_config.toml", ConfigFormat: "toml"},
	}}
}
