// Package unifyai 实现 UnifyAI 配置同步 CLI 的桥接服务：
// 把 Loadout 管理后台的「UnifyAI 配置同步」页面连接到 unifyai CLI
// （统一通过 `npx unifyai@latest -y` 运行，npx 自动安装，无需本地脚本），
// 提供平台能力列表（--list-platforms --json）与指令执行（含实时日志流）。
package unifyai

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
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

// resolveCmd 返回 unifyai 执行入口：统一走 `npx -y unifyai@latest`。
// npx 会自动拉取/复用 unifyai 包，无需本地仓库或全局安装，只要机器有 Node.js 环境。
// 注意：`-y`（跳过 npx 的 "Ok to proceed?" 安装确认）必须放在包名【前面】，
// 放在包名后会作为 unifyai 的参数传入，导致 `error: unknown option '-y'`。
func resolveCmd() (string, []string, error) {
	npx, err := exec.LookPath("npx")
	if err != nil {
		// Windows 常见安装位置兜底。
		if runtime.GOOS == "windows" {
			for _, p := range []string{
				filepath.Join(os.Getenv("APPDATA"), "npm", "npx.cmd"),
				"C:/Program Files/nodejs/npx.cmd",
			} {
				if fileExists(p) {
					return p, []string{"-y", "unifyai@latest"}, nil
				}
			}
		}
		return "", nil, fmt.Errorf("未找到 npx：请先安装 Node.js（unifyai 通过 npx 自动运行，无需额外安装）")
	}
	return npx, []string{"-y", "unifyai@latest"}, nil
}

// fileExists 判断路径存在且是普通文件。
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// displayCmd 生成人类可读的命令展示。
func displayCmd(cmd string, base []string) string {
	return "npx -y unifyai@latest"
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

// ============ MCP 服务器列表（直接读写 mcp.json，不经 CLI）============

// McpServer 单个 MCP 服务器（mcp.json 条目）。
// 写回时只写 disabled 字段：unifyai 同步时 loadMcpConfig 以
// `if (!config.disabled)` 过滤服务器（enabled 仅 normalizeMcp 兜底，不再双写）。
type McpServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`    // local | remote
	Enabled bool              `json:"enabled"`
	Command []string          `json:"command,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

// mcpRawEntry mcp.json 中 mcpServers 的单条原始结构（兼容 string/数组 command）。
type mcpRawEntry struct {
	Type     string            `json:"type"`
	Enabled  *bool             `json:"enabled"`
	Disabled *bool             `json:"disabled"`
	Command  json.RawMessage   `json:"command"`
	URL      string            `json:"url"`
	Headers  map[string]string `json:"headers"`
	Env      map[string]string `json:"env"`
}

// McpServers 读取 mcp.json（优先级 cwd/mcp.json > ~/.unifyai/mcp.json），
// 返回 UI 使用的服务器列表（含 disabled，由 UI 决定参与同步）。
// 文件不存在返回空列表（不报错）。
func (s *Service) McpServers() ([]McpServer, error) {
	path := mcpConfigPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []McpServer{}, nil
		}
		return nil, fmt.Errorf("unifyai: 读取 mcp.json 失败: %w", err)
	}
	var raw struct {
		McpServers map[string]mcpRawEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("unifyai: 解析 mcp.json 失败: %w", err)
	}
	servers := make([]McpServer, 0, len(raw.McpServers))
	for name, entry := range raw.McpServers {
		enabled := true
		if entry.Enabled != nil && !*entry.Enabled {
			enabled = false
		}
		if entry.Disabled != nil && *entry.Disabled {
			enabled = false
		}
		servers = append(servers, McpServer{
			Name:    name,
			Type:    entry.Type,
			Enabled: enabled,
			Command: parseCommandField(entry.Command),
			URL:     entry.URL,
			Headers: entry.Headers,
			Env:     entry.Env,
		})
	}
	// 稳定排序（按名称），UI 顺序一致。
	sortMcpServers(servers)
	return servers, nil
}

// SaveMcpServers 把服务器列表写回 mcp.json（优先写已存在的 cwd/mcp.json，
// 否则写 ~/.unifyai/mcp.json，目录不存在自动创建）。
func (s *Service) SaveMcpServers(servers []McpServer) error {
	path := mcpConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("unifyai: 创建配置目录失败: %w", err)
	}
	m := make(map[string]any, len(servers))
	for _, srv := range servers {
		// 只写 disabled：unifyai 同步时以 `if (!config.disabled)` 过滤（loadMcpConfig），
		// enabled 不写（normalizeMcp 的 enabled 判断只是兜底，避免双字段冗余）。
		entry := map[string]any{
			"type":     srv.Type,
			"disabled": !srv.Enabled,
		}
		if len(srv.Command) > 0 {
			entry["command"] = srv.Command
		}
		if srv.URL != "" {
			entry["url"] = srv.URL
		}
		if len(srv.Headers) > 0 {
			entry["headers"] = srv.Headers
		}
		if len(srv.Env) > 0 {
			entry["env"] = srv.Env
		}
		m[srv.Name] = entry
	}
	doc := map[string]any{"mcpServers": m}
	buf := new(bytes.Buffer)
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return fmt.Errorf("unifyai: 编码 mcp.json 失败: %w", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("unifyai: 写入 mcp.json 失败: %w", err)
	}
	s.lg.Info("unifyai: 已保存 mcp.json", "path", path, "count", len(servers))
	return nil
}

// mcpConfigPath 解析 mcp.json 路径：cwd 优先，回退 ~/.unifyai/mcp.json（写入时创建）。
// 包级变量，测试可替换。
var mcpConfigPath = func() string {
	if p, err := os.Getwd(); err == nil {
		if c := filepath.Join(p, "mcp.json"); fileExists(c) {
			return c
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".unifyai", "mcp.json")
	}
	return "mcp.json"
}

// parseCommandField 兼容 command 为数组或字符串两种写法。
func parseCommandField(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil && strings.TrimSpace(str) != "" {
		return []string{str}
	}
	return nil
}

// sortMcpServers 按名称排序（稳定展示顺序）。
func sortMcpServers(servers []McpServer) {
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
}
