// Package unifyai 实现 UnifyAI 配置同步 CLI 的桥接服务：
// 把 Loadout 管理后台的「UnifyAI 配置同步」页面连接到 unifyai CLI
// （统一通过 `npx unifyai@latest -y` 运行，npx 自动安装，无需本地脚本），
// 提供平台能力列表（--list platforms --json）与指令执行（含实时日志流）。
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
	"time"

	"loadout/core/cmdutil"
	"loadout/core/procreg"
	"loadout/core/config"
)

// OpenRouterMeta 对应 openrouter-models.json 中单个模型的元数据条目
// （unifyai --update-metadata 从 https://openrouter.ai/api/v1/models 拉取后缓存）。
type OpenRouterMeta struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Context   int64  `json:"context"`
	Output    int64  `json:"output"`
	Vision    bool   `json:"vision"`
	Reasoning bool   `json:"reasoning"`
}

// ModelSourceStatus 对应 UI「模型来源」卡片：OpenRouter 连接信息 + 元数据缓存状态。
type ModelSourceStatus struct {
	Kind           string `json:"kind"` // openrouter | none
	BaseURL        string `json:"baseUrl"`
	APIKeyMasked   string `json:"apiKeyMasked"` // 未配置为空串
	ModelCount     int    `json:"modelCount"`
	VisionCount    int    `json:"visionCount"`
	ReasoningCount int    `json:"reasoningCount"`
	CachedAt       string `json:"cachedAt"` // 缓存文件修改时间（RFC3339），无缓存为空
	Degraded       string `json:"degraded,omitempty"`
}

// openrouterBaseURL 是 unifyai --update-metadata 拉取模型数据的公开端点。
const openrouterBaseURL = "https://openrouter.ai/api/v1"

// metadataCachePath 返回 OpenRouter 元数据缓存文件路径（~/.unifyai/cache/openrouter-models.json），
// 与 unifyai 源码 metadata-fetcher.mjs 的 CACHE_FILE 保持一致。
func metadataCachePath() string {
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".unifyai", "cache", "openrouter-models.json")
	}
	return "openrouter-models.json"
}

// ModelSource 读取 OpenRouter 元数据缓存（不经 CLI，快、离线可用）。
// 缓存缺失/损坏时返回 Kind=none（不报错），UI 据此提示先执行刷新。
func (s *Service) ModelSource() ModelSourceStatus {
	res := ModelSourceStatus{Kind: "openrouter", BaseURL: openrouterBaseURL}
	// API Key：OpenRouter /models 是公开端点，仅当配置了环境变量时回显掩码。
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		res.APIKeyMasked = maskSecret(key)
	}
	data, err := os.ReadFile(metadataCachePath())
	if err != nil {
		res.Kind = "none"
		res.Degraded = "元数据缓存不存在，请先点击「更新元数据」"
		return res
	}
	var metas []OpenRouterMeta
	if err := json.Unmarshal(data, &metas); err != nil {
		res.Kind = "none"
		res.Degraded = "元数据缓存解析失败，请重新「更新元数据」"
		return res
	}
	res.ModelCount = len(metas)
	for _, m := range metas {
		if m.Vision {
			res.VisionCount++
		}
		if m.Reasoning {
			res.ReasoningCount++
		}
	}
	if fi, err := os.Stat(metadataCachePath()); err == nil {
		res.CachedAt = fi.ModTime().Format(time.RFC3339)
	}
	return res
}

// maskSecret 把密钥掩码成 sk-xxxx****xxxx 形式（前后各留 4 位）。
func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

// OpenCodexModel 对应 unifyai --list models --json 输出的单个模型。
type OpenCodexModel struct {
	Provider          string `json:"provider"`
	ModelID           string `json:"modelId"`
	DisplayName       string `json:"displayName"`
	ContextWindow     int64  `json:"contextWindow"`
	MaxOutputTokens   int64  `json:"maxOutputTokens"`
	SupportsVision    bool   `json:"supportsVision"`
	SupportsThinking  bool   `json:"supportsThinking"`
}

// OpenCodexModelsResult 对应 unifyai --list models --json 的完整输出。
type OpenCodexModelsResult struct {
	Source               string           `json:"source"`
	ProxyURL             string           `json:"proxyUrl"`
	Port                 int              `json:"port"`
	HasAPIKey            bool             `json:"hasApiKey"`
	APIKeyPreview        string           `json:"apiKeyPreview"`
	ProviderCount        int              `json:"providerCount"`
	EnabledProviderCount int              `json:"enabledProviderCount"`
	RawCount             int              `json:"rawCount"`
	Degraded             bool             `json:"degraded"`
	DegradedReason       string           `json:"degradedReason"`
	ORMatchedCount       int              `json:"orMatchedCount"`
	ORTotal              int              `json:"orTotal"`
	Models               []OpenCodexModel `json:"models"`
	Count                int              `json:"count"`
	Error                string           `json:"error,omitempty"`
}

// OpenCodexModels 获取 OpenCodex 代理的模型列表（--list models --json）。
// enableVision=true 时追加 --enable-vision（强制所有模型标记为支持视觉）。
// CLI 不可用/代理不可达时返回 Degraded=true + 原因（不报错），保证页面可用。
func (s *Service) OpenCodexModels(enableVision bool) OpenCodexModelsResult {
	cmd, base, err := resolveCmd()
	if err != nil {
		return OpenCodexModelsResult{Degraded: true, DegradedReason: err.Error()}
	}
	// 新版 CLI 用 `--list models --json`（旧 `--list-models` 已移除），输出 {models: {...}}。
	args := append(append([]string{}, base...), "--list", "models", "--json")
	if enableVision {
		args = append(args, "--enable-vision")
	}
	proc := exec.Command(cmd, args...)
	cmdutil.HideWindow(proc)
	proc.Env = envWithBinDir(cmd)
	out, err := proc.Output()
	if err != nil {
		s.lg.Warn("unifyai: --list models 执行失败", "err", err)
		return OpenCodexModelsResult{Degraded: true, DegradedReason: err.Error()}
	}
	var wrapped struct {
		Models OpenCodexModelsResult `json:"models"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		s.lg.Warn("unifyai: 解析 --list models JSON 失败", "err", err)
		return OpenCodexModelsResult{Degraded: true, DegradedReason: "解析 --list models 输出失败"}
	}
	res := wrapped.Models
	if res.Error != "" {
		res.Degraded = true
		res.DegradedReason = res.Error
	}
	return res
}

// Platform 对应 `unifyai --list platforms --json` 输出的单个平台（附录 B.1）。
type Platform struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	SupportsModels bool   `json:"supportsModels"`
	ModelStatus    string `json:"modelStatus"`
	SupportsMcp    bool   `json:"supportsMcp"`
	McpStatus      string `json:"mcpStatus"`
	ConfigPath     string `json:"configPath"`
	ConfigFormat   string `json:"configFormat"`
}

// ListPlatformsResult 对应 --list platforms --json 的完整输出。
type ListPlatformsResult struct {
	Platforms []Platform `json:"platforms"`
}

// McpMatrixServer 对应 --list mcp --json 中单个服务器的条目（name/enabled/config 原始配置）。
type McpMatrixServer struct {
	Name    string          `json:"name"`
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config,omitempty"`
}

// McpSourceState 对应 --list mcp --json 的 source 字段（源 mcp.json）。
type McpSourceState struct {
	Path    string            `json:"path"`
	Servers []McpMatrixServer `json:"servers"`
}

// McpPlatformState 对应 --list mcp --json 中单个平台的状态（可读性 + 服务器开关列表）。
type McpPlatformState struct {
	Platform   string            `json:"platform"`
	Name       string            `json:"name"`
	ConfigPath string            `json:"configPath"`
	Readable   bool              `json:"readable"`
	Servers    []McpMatrixServer `json:"servers"`
}

// McpMatrixResult 对应 --list mcp --json 的完整输出（源 + 各平台），供前端渲染同步矩阵。
type McpMatrixResult struct {
	Source    *McpSourceState    `json:"source"`
	Platforms []McpPlatformState `json:"platforms"`
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

// PlatformInfo 解析 `unifyai --list platforms --json`，返回平台能力列表。
// CLI 不可用时回落到内置默认平台（与 UI 静态数据一致），保证页面可用。
func (s *Service) PlatformInfo() (ListPlatformsResult, error) {
	cmd, base, err := resolveCmd()
	if err != nil {
		s.lg.Warn("unifyai: 获取平台列表回落到内置默认", "err", err)
		return defaultPlatforms(), nil
	}
	args := append(append([]string{}, base...), "--list", "platforms", "--json")
	proc := exec.Command(cmd, args...)
	cmdutil.HideWindow(proc) // 桌面 exe 下不弹黑色终端框
	// 后台服务 PATH 可能不完整，把 npx 所在目录补到最前（含 node）。
	proc.Env = envWithBinDir(cmd)
	out, err := proc.Output()
	if err != nil {
		s.lg.Warn("unifyai: --list platforms 失败，回落到内置默认", "err", err)
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

// AllConfigResult 对应 `unifyai --list all --json` 的完整输出（前端一次获取全部配置）。
// Models / Metadata 结构复杂且随 CLI 演进，用 RawMessage 透传，前端直接消费。
type AllConfigResult struct {
	Platforms []Platform      `json:"platforms"`
	Models    json.RawMessage `json:"models"`
	Mcp       McpMatrixResult `json:"mcp"`
	Metadata  json.RawMessage `json:"metadata"`
}

// ListAll 解析 `unifyai --list all --json`，返回平台 + 模型 + MCP 矩阵 + 元数据缓存状态，
// 前端初始化一次拉全（替代分别调 platforms / opencodex-models / mcp-matrix）。
// CLI 不可用时返回空结构（前端回落内置默认），不报错。
func (s *Service) ListAll() (AllConfigResult, error) {
	var empty AllConfigResult
	cmd, base, err := resolveCmd()
	if err != nil {
		s.lg.Warn("unifyai: --list all 获取失败（未找到 npx）", "err", err)
		return empty, nil
	}
	args := append(append([]string{}, base...), "--list", "all", "--json")
	proc := exec.Command(cmd, args...)
	cmdutil.HideWindow(proc)
	proc.Env = envWithBinDir(cmd)
	out, err := proc.Output()
	if err != nil {
		s.lg.Warn("unifyai: --list all 执行失败", "err", err)
		return empty, nil
	}
	var res AllConfigResult
	if err := json.Unmarshal(out, &res); err != nil {
		s.lg.Warn("unifyai: 解析 --list all JSON 失败", "err", err)
		return empty, nil
	}
	return res, nil
}

// syncConfigPath 返回同步配置文件路径（前端把当前 UI 状态落盘后以 --config 引用）。
func syncConfigPath() string {
	return filepath.Join(osConfigHome(), "sync.json")
}

// osConfigHome 返回 unifyai 配置目录（~/.unifyai），与 CLI 的 resolveSourceMcp 一致。
func osConfigHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".unifyai"
	}
	return filepath.Join(home, ".unifyai")
}

// SaveSyncConfig 把同步配置 JSON 写入 ~/.unifyai/sync.json（前端保存当前 UI 状态用）。
func (s *Service) SaveSyncConfig(cfg []byte) (string, error) {
	p := syncConfigPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, cfg, 0o644); err != nil {
		return "", err
	}
	return p, nil
}

// SyncConfigPath 返回同步配置文件路径（前端命令预览展示用）。
func (s *Service) SyncConfigPath() string {
	return syncConfigPath()
}

// ListMcpMatrix 解析 `unifyai --list mcp --json`，返回源 mcp.json + 各平台 MCP 开关状态，
// 供前端「MCP 同步矩阵」渲染（行=去重服务器，列=平台，勾选=该平台开启）。
// CLI 不可用 / 执行失败时返回空结果（前端回落内置默认），不报错。
func (s *Service) ListMcpMatrix() (McpMatrixResult, error) {
	var empty McpMatrixResult
	cmd, base, err := resolveCmd()
	if err != nil {
		s.lg.Warn("unifyai: --list mcp 获取失败（未找到 npx）", "err", err)
		return empty, nil
	}
	args := append(append([]string{}, base...), "--list", "mcp", "--json")
	proc := exec.Command(cmd, args...)
	cmdutil.HideWindow(proc) // 桌面 exe 下不弹黑色终端框
	proc.Env = envWithBinDir(cmd)
	out, err := proc.Output()
	if err != nil {
		s.lg.Warn("unifyai: --list mcp 执行失败", "err", err)
		return empty, nil
	}
	// 新版 CLI 输出 {mcp: {source, platforms}}，多包一层 mcp。
	var wrapped struct {
		Mcp McpMatrixResult `json:"mcp"`
	}
	if err := json.Unmarshal(out, &wrapped); err != nil {
		s.lg.Warn("unifyai: 解析 --list mcp JSON 失败", "err", err)
		return empty, nil
	}
	return wrapped.Mcp, nil
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
	h, err := procreg.Run(procreg.Options{
		Name:  "UnifyAI 同步",
		Kind:  "unifyai",
		Cmd:   cmd,
		Args:  full,
		OnLog: onLog,
	})
	if err != nil {
		return fmt.Errorf("unifyai: %w", err)
	}
	if err := h.Wait(); err != nil {
		return fmt.Errorf("unifyai: %w", err)
	}
	return nil
}

// resolveCmd 返回 unifyai 执行入口（cmd + 固定前缀参数）。
// 优先级：
//  1. LOADOUT_UNIFYAI_CMD（config.UnifyaiCmd）配置的命令行，按 shell 风格分词
//     （双引号可包含空格的路径），例如 `node "D:/Code/Git/unifyai/src/cli.mjs"`；
//  2. 默认统一走 `npx -y unifyai@latest`。
//
// npx 会自动拉取/复用 unifyai 包，无需本地仓库或全局安装，只要机器有 Node.js 环境。
// 注意：`-y`（跳过 npx 的 "Ok to proceed?" 安装确认）必须放在包名【前面】，
// 放在包名后会作为 unifyai 的参数传入，导致 `error: unknown option '-y'`。
func resolveCmd() (string, []string, error) {
	if cfg := config.UnifyaiCmd; cfg != "" {
		parts := splitCommandLine(cfg)
		if len(parts) == 0 {
			return "", nil, fmt.Errorf("LOADOUT_UNIFYAI_CMD 无法解析: %q", cfg)
		}
		return parts[0], parts[1:], nil
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		// PATH 中找不到 npx（后台服务/systemd 启动时环境不完整），
		// 按常见安装位置兜底，覆盖 Windows 与 Linux。
		for _, p := range npxCandidates() {
			if fileExists(p) {
				return p, []string{"-y", "unifyai@latest"}, nil
			}
		}
		return "", nil, fmt.Errorf("未找到 npx：请先安装 Node.js（unifyai 通过 npx 自动运行，无需额外安装）")
	}
	return npx, []string{"-y", "unifyai@latest"}, nil
}

// splitCommandLine 按空白分词，支持双引号包裹的路径/含空格参数（如 "C:/Program Files/..."）。
func splitCommandLine(s string) []string {
	var parts []string
	var cur strings.Builder
	inQuote := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				parts = append(parts, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		parts = append(parts, cur.String())
	}
	return parts
}

// npxCandidates 按常见安装位置枚举 npx 完整路径（含 Windows 与 Linux）。
// 包级变量，测试可替换。
var npxCandidates = func() []string {
	if runtime.GOOS == "windows" {
		return []string{
			filepath.Join(os.Getenv("APPDATA"), "npm", "npx.cmd"),
			"C:/Program Files/nodejs/npx.cmd",
		}
	}
	// Linux: nvm、fnm 版本目录全部枚举，另加 volta/asdf/系统位置。
	var cands []string
	if home, err := os.UserHomeDir(); err == nil {
		// nvm: ~/.nvm/versions/node/<ver>/bin/npx。
		versionsDir := filepath.Join(home, ".nvm", "versions", "node")
		if entries, err := os.ReadDir(versionsDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					cands = append(cands, filepath.Join(versionsDir, e.Name(), "bin", "npx"))
				}
			}
		}
		cands = append(cands,
			filepath.Join(home, ".nvm", "current", "bin", "npx"),
			filepath.Join(home, ".local", "bin", "npx"),
			filepath.Join(home, ".volta", "bin", "npx"),
			filepath.Join(home, ".asdf", "shims", "npx"),
		)
		// fnm: ~/.local/share/fnm/<ver>/installation/bin/npx。
		fnmDir := filepath.Join(home, ".local", "share", "fnm")
		if entries, err := os.ReadDir(fnmDir); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					cands = append(cands, filepath.Join(fnmDir, e.Name(), "installation", "bin", "npx"))
				}
			}
		}
	}
	return append(cands,
		"/usr/local/bin/npx",
		"/usr/bin/npx",
		"/opt/node/bin/npx",
		"/usr/local/node/bin/npx",
	)
}

// findNpxFallback 在常见安装位置中寻找存在的 npx（兜底 PATH 缺失场景）。
func findNpxFallback() string {
	for _, p := range npxCandidates() {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// envWithBinDir 在子进程环境变量中把命令所在目录放到 PATH 最前，
// 保证 npx 能找到同目录下的 node（后台服务 PATH 不完整时）。
func envWithBinDir(cmdPath string) []string {
	dir := filepath.Dir(cmdPath)
	return osEnvironWithPathPrefix(dir)
}

// osEnvironWithPathPrefix 返回 os.Environ 副本，并把 dir 放到 PATH 最前。
// 后台服务/systemd 启动时 PATH 可能不完整，需显式补全命令所在目录。
func osEnvironWithPathPrefix(dir string) []string {
	env := os.Environ()
	for i, kv := range env {
		if j := strings.IndexByte(kv, '='); j > 0 && strings.EqualFold(kv[:j], "PATH") {
			cur := kv[j+1:]
			if cur == "" {
				env[i] = "PATH=" + dir
			} else if !strings.HasPrefix(cur, dir+string(os.PathListSeparator)) {
				env[i] = "PATH=" + dir + string(os.PathListSeparator) + cur
			}
			return env
		}
	}
	return append(env, "PATH="+dir)
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
		{ID: "workbuddy", Name: "WorkBuddy", SupportsModels: true, ModelStatus: "supported", SupportsMcp: true, McpStatus: "supported", ConfigPath: "~/.workbuddy/models.json", ConfigFormat: "json"},
	}}
}

// ============ MCP 服务器列表（直接读写 mcp.json，不经 CLI）============

// McpServer 单个 MCP 服务器（mcp.json 条目）。
// 写回时只写 disabled 字段：unifyai 同步时 loadMcpConfig 以
// `if (!config.disabled)` 过滤服务器（enabled 仅 normalizeMcp 兜底，不再双写）。
type McpServer struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"` // local | remote
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
