package adminapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"loadout/core/config"
	"loadout/core/procreg"
)

// ============================================================================
// OpenCodex 集成检测与控制（ocx CLI 桥接）
// ============================================================================

// OcxStatus 返回给前端的检测状态。
type OcxStatus struct {
	CLIInstalled    bool     `json:"cliInstalled"`
	ServiceRunning  bool     `json:"serviceRunning"`
	LoadoutProvider bool     `json:"loadoutProvider"`
	CodexInjected   bool     `json:"codexInjected"`
	Port            int      `json:"port"`
	ProxyURL        string   `json:"proxyUrl"`
	LoadoutBaseURL  string   `json:"loadoutBaseUrl"`
	Missing         []string `json:"missing"`

	// ---- 生命周期分层（新）----
	// ConfigInitialized  ~/.opencodex/config.json 存在且能解析出 providers
	ConfigInitialized bool `json:"configInitialized"`
	// ProviderCount / DefaultProvider 用于让用户看到「我已经配了什么」
	ProviderCount   int    `json:"providerCount"`
	DefaultProvider string `json:"defaultProvider"`
	// ModelCount Loadout provider 下的可用模型数（config 已知模型 + 代理实时目录）
	ModelCount int `json:"modelCount"`
	// ServiceInstalled 后台服务已注册（service-state.json 存在）
	ServiceInstalled bool `json:"serviceInstalled"`
	// ServiceBackend 服务管理器：scheduler / launchd / systemd / winsw
	ServiceBackend string `json:"serviceBackend"`
	// ShimInstalled Codex 自启动 shim
	ShimInstalled bool `json:"shimInstalled"`
	// LaunchMode 当前代理的拉起方式
	LaunchMode OcxLaunchMode `json:"launchMode"`
	// Protection 开机/崩溃保护等级：none / shim / service / service+shim
	Protection string `json:"protection"`
	// RebootSafe 重启电脑后代理是否还会自己回来
	RebootSafe bool `json:"rebootSafe"`
	// ServiceLog 服务日志路径（诊断用）
	ServiceLog string `json:"serviceLog"`
	// CLIVersion 已安装 ocx 的版本（读 package.json，避免跑 ocx --version）
	CLIVersion string `json:"cliVersion"`
	// ProxyVersion / ProxyPID 来自 /healthz（0.6s），用于确认「同一个代理」
	ProxyVersion string `json:"proxyVersion"`
	ProxyPID     int    `json:"proxyPid"`
	// Capabilities 生命周期能力清单（前端主视图）
	Capabilities []OcxCapability `json:"capabilities"`
	// Methods 三种启动方式的可用性（前端方式选择卡）
	Methods []OcxMethod `json:"methods"`
	// CheckedAt RFC3339，前端显示「刚刚检测」
	CheckedAt string `json:"checkedAt"`
}

// 默认常量（与 OpenCodex CLI 实际路径一致；端口从 config.json 读取，缺省 10100）。
const (
	ocxDefaultPort        = 10100
	ocxLoadoutDefaultBase = "http://localhost:3000"
	ocxProxyDefaultURL    = "http://127.0.0.1"
)

// ocxConfigPath 返回 ~/.opencodex/config.json 的绝对路径。
// 做成 var 便于测试覆盖到临时目录，与 unifyai 包的 metadataCachePath 同模式。
var ocxConfigPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opencodex", "config.json")
}

// ocxLookPath 是 exec.LookPath 的 seam（测试可模拟「未安装」）。
var ocxLookPath = exec.LookPath

// ocxCodexConfigPath 返回 ~/.codex/config.toml 的绝对路径（Codex 桌面端注入目标）。
var ocxCodexConfigPath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".codex", "config.toml")
}

// OcxStatus 执行一次完整检测。
// 任何一步失败（文件缺失 / JSON 损坏 / 端口未监听）都不会让整个检测报错，
// 而是把对应项标为 false 并列入 missing，保证 UI 永远有东西可显示。
func (s *Service) OcxStatus() OcxStatus {
	res := OcxStatus{
		Port:           ocxDefaultPort,
		ProxyURL:       ocxProxyDefaultURL,
		LoadoutBaseURL: ocxLoadoutDefaultBase,
		LaunchMode:     OcxModeUnknown,
		Protection:     "none",
		CheckedAt:      time.Now().Format(time.RFC3339),
		// 切片字段必须初始化为空切片而不是 nil：Go 的 nil slice 序列化成 JSON null，
		// 前端 .length / v-for 会抛 "Cannot read properties of null"，整块卡片渲染失败。
		Missing:      []string{},
		Capabilities: []OcxCapability{},
		Methods:      []OcxMethod{},
	}

	// ① CLI 安装检测（设置页「安装 Open Codex」= npm install -g @bitkyc08/opencodex）。
	if path, err := ocxLookPath("ocx"); err == nil && path != "" {
		res.CLIInstalled = true
		res.CLIVersion = readOcxCLIVersion(path)
	} else {
		res.Missing = append(res.Missing,
			"Open Codex CLI 未安装（请到设置 → 依赖管理执行 npm install -g @bitkyc08/opencodex）")
		res.buildDerived()
		return res // CLI 都没有，后面三项检测必然失败，直接返回
	}

	// ②~④ 都依赖 ~/.opencodex/config.json，一次读出全部需要的信息。
	cfgInfo := s.readOcxConfigFull()
	port := cfgInfo.port
	loadoutBase := cfgInfo.loadoutBase
	hasLoadoutProvider := cfgInfo.hasLoadout
	res.Port = port
	res.ProxyURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	res.LoadoutBaseURL = loadoutBase
	res.LoadoutProvider = hasLoadoutProvider
	res.ConfigInitialized = cfgInfo.initialized
	res.ProviderCount = cfgInfo.providerCount
	res.DefaultProvider = cfgInfo.defaultProvider
	res.ModelCount = cfgInfo.modelCount

	// ② 服务运行检测：直接打 /healthz（实测 0.6s），顺带拿 PID 与版本。
	// 不用 ocx status（17~21s）也不用 ocx service status（3.4s）。
	if hz, ok := ocxHealthz(port); ok {
		res.ServiceRunning = true
		res.ProxyPID = hz.PID
		res.ProxyVersion = hz.Version
	}

	// ④ Codex 接入检测：读 ~/.codex/config.toml，判断是否含 opencodex provider
	// 且 base_url 指向本机代理（即 ocx sync 已把 Loadout 的模型源注入 Codex）。
	res.CodexInjected = readCodexInjected(port)

	// ③ 服务注册 / shim / 拉起方式（决定重启电脑后还在不在）。
	svc := readServiceState()
	res.ServiceInstalled = svc.Installed
	res.ServiceBackend = svc.Backend
	res.ShimInstalled = readShimInstalled()
	res.ServiceLog = ocxServiceLogPath()
	res.LaunchMode = detectLaunchMode(res.ServiceInstalled, res.ShimInstalled, res.ProxyPID)

	if !res.ServiceRunning {
		res.Missing = append(res.Missing,
			"Open Codex 代理没有在运行（三种启动方式任选其一，见下方「启动方式」）")
	}
	if res.CLIInstalled && !res.ConfigInitialized {
		res.Missing = append(res.Missing,
			"Open Codex 尚未初始化（运行 ocx init 写入 ~/.opencodex/config.json）")
	}
	if !res.LoadoutProvider {
		res.Missing = append(res.Missing,
			"Open Codex 配置中没有指向 Loadout 的 provider（打开管理面板添加 baseUrl 为本机 :3000 的 provider）")
	}
	if !res.CodexInjected {
		res.Missing = append(res.Missing,
			"Codex 配置没有注入 OpenCodex provider（运行 ocx sync）")
	}
	if res.ServiceRunning && !res.ServiceInstalled && !res.ShimInstalled {
		res.Missing = append(res.Missing,
			"代理是手动启动的，关掉终端/重启电脑就会断（建议安装后台服务或自启动 shim）")
	}
	res.buildDerived()
	return res
}

// readOcxConfig 读 ~/.opencodex/config.json，返回（端口，Loadout baseUrl，是否含 Loadout provider）。
// 文件缺失 / JSON 损坏时返回默认值并视为未配置。
func (s *Service) readOcxConfig() (port int, loadoutBase string, hasLoadout bool) {
	info := s.readOcxConfigFull()
	return info.port, info.loadoutBase, info.hasLoadout
}

// ocxConfigInfo config.json 解析结果（一次读盘，供分层检测共用）。
type ocxConfigInfo struct {
	initialized     bool
	port            int
	loadoutBase     string
	hasLoadout      bool
	providerCount   int
	defaultProvider string
	modelCount      int
}

// readOcxConfigFull 读 ~/.opencodex/config.json，解析端口 / provider / 模型数量。
// 文件不存在或 JSON 损坏时 initialized=false，其余字段回默认值。
func (s *Service) readOcxConfigFull() ocxConfigInfo {
	info := ocxConfigInfo{
		port:        ocxDefaultPort,
		loadoutBase: ocxLoadoutDefaultBase,
	}
	p := ocxConfigPath()
	if p == "" {
		return info
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return info
	}
	var cfg struct {
		Port int `json:"port"`
		Providers map[string]struct {
			BaseURL  string   `json:"baseUrl"`
			Disabled bool     `json:"disabled"`
			Models   []string `json:"models"`
			DefaultModel string `json:"defaultModel"`
		} `json:"providers"`
		DefaultProvider string `json:"defaultProvider"`
		ModelDiscovery  struct {
			KnownModels map[string]struct {
				IDs []string `json:"ids"`
			} `json:"knownModels"`
		} `json:"modelDiscovery"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return info
	}
	info.initialized = true
	info.defaultProvider = cfg.DefaultProvider
	info.providerCount = len(cfg.Providers)
	if cfg.Port > 0 {
		info.port = cfg.Port
	}
	// providers 里的 baseUrl 可能写成 http://localhost:3000、http://127.0.0.1:3000、
	// 带路径（/v1）或没带端口。统一提取 host:port 再对比。
	var loadoutName string
	for name, pv := range cfg.Providers {
		if pv.Disabled {
			continue
		}
		if isLoadoutBaseURL(pv.BaseURL) {
			info.hasLoadout = true
			info.loadoutBase = normalizeLoadoutBase(pv.BaseURL)
			loadoutName = name
			info.modelCount = len(pv.Models)
			if info.modelCount == 0 && pv.DefaultModel != "" {
				info.modelCount = 1
			}
			break
		}
	}
	// 端口不匹配（例如 OpenCodex 里配的是 :3000、当前后端跑在 :5009）但 provider
	// 名字或默认 provider 明显是 Loadout 时，也要把模型数读出来——否则 UI 会同时
	// 显示「没接入 Loadout」和「0 个模型」，丢掉了用户本来已有的信息。
	if loadoutName == "" {
		loadoutName = pickLoadoutProviderName(cfg.DefaultProvider, cfg.Providers)
		if loadoutName != "" {
			pv := cfg.Providers[loadoutName]
			info.modelCount = len(pv.Models)
			if info.modelCount == 0 && pv.DefaultModel != "" {
				info.modelCount = 1
			}
		}
	}
	// config 的 models 常为空（模型靠实时发现），用 modelDiscovery 的缓存条数兜底。
	if info.modelCount == 0 && loadoutName != "" {
		if km, ok := cfg.ModelDiscovery.KnownModels[loadoutName]; ok {
			info.modelCount = len(km.IDs)
		}
	}
	return info
}

// pickLoadoutProviderName 在 baseUrl 端口不匹配时，用名字兜底找 Loadout provider。
// 只认「默认 provider 是 Loadout」或「唯一一个叫 Loadout」的情况，避免误认。
func pickLoadoutProviderName(defaultProvider string, providers map[string]struct {
	BaseURL      string   `json:"baseUrl"`
	Disabled     bool     `json:"disabled"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"defaultModel"`
}) string {
	const want = "loadout"
	if strings.EqualFold(defaultProvider, want) {
		if _, ok := providers["Loadout"]; ok {
			return "Loadout"
		}
	}
	for name := range providers {
		if strings.EqualFold(name, want) {
			return name
		}
	}
	return ""
}

// ocxHealthSnapshot /healthz 的最小字段集（0.6s 返回，比任何 CLI 命令都快）。
type ocxHealthSnapshot struct {
	Status  string `json:"status"`
	Version string `json:"version"`
	PID     int    `json:"pid"`
	Port    int    `json:"port"`
}

// ocxHealthz 探测本机代理存活，返回健康快照；失败返回 false。
var ocxHealthz = func(port int) (ocxHealthSnapshot, bool) {
	if port <= 0 {
		return ocxHealthSnapshot{}, false
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ocxHealthSnapshot{}, false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ocxHealthSnapshot{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ocxHealthSnapshot{}, false
	}
	var snap ocxHealthSnapshot
	if json.NewDecoder(resp.Body).Decode(&snap) != nil {
		return ocxHealthSnapshot{}, false
	}
	return snap, snap.Status == "ok"
}

// detectLaunchMode 由「服务是否注册 + shim 是否安装 + 进程 PID」推断拉起方式。
// 无法 100% 确认时返回 unknown，前端只做「可能」提示，不误导。
func detectLaunchMode(serviceInstalled, shimInstalled bool, pid int) OcxLaunchMode {
	if pid <= 0 {
		return OcxModeUnknown
	}
	if serviceInstalled {
		return OcxModeService
	}
	if shimInstalled {
		return OcxModeShim
	}
	return OcxModeManual
}

// ocxServiceLogPath 返回服务日志路径（前端诊断链接用）。
func ocxServiceLogPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opencodex", "service.log")
}

// readOcxCLIVersion 从 npm 全局包目录读 package.json 版本（比跑 ocx --version 便宜）。
// LookPath 拿到的是 ocx.cmd / ocx.ps1，其同目录 node_modules 下才有 package.json。
func readOcxCLIVersion(ocxPath string) string {
	dir := filepath.Dir(ocxPath)
	candidates := []string{
		filepath.Join(dir, "node_modules", "@bitkyc08", "opencodex", "package.json"),
		filepath.Join(dir, "..", "node_modules", "@bitkyc08", "opencodex", "package.json"),
	}
	for _, p := range candidates {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var pkg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &pkg) == nil && pkg.Version != "" {
			return pkg.Version
		}
	}
	return ""
}

// buildDerived 由基础布尔量派生：保护等级、能否安全重启、能力清单、启动方式清单。
// 拆出来是为了让前端只需要渲染，不再自己推断业务语义。
func (s *OcxStatus) buildDerived() {
	// ---- 保护等级：决定「重启电脑后代理还回不回来」 ----
	switch {
	case s.ServiceInstalled && s.ShimInstalled:
		s.Protection = "service+shim"
	case s.ServiceInstalled:
		s.Protection = "service"
	case s.ShimInstalled:
		s.Protection = "shim"
	default:
		s.Protection = "none"
	}
	// 重启安全的充分条件：服务或 shim 至少一个在，且代理当前真的活着。
	s.RebootSafe = (s.ServiceInstalled || s.ShimInstalled) && s.ServiceRunning

	// ---- 生命周期能力清单（前端主视图：每行一条，带状态与修复建议）----
	caps := make([]OcxCapability, 0, 7)
	caps = append(caps, OcxCapability{
		Key:     "cli",
		Label:   "CLI 已安装",
		Desc:    "ocx 命令可用，代理、服务、配置面板都靠它驱动",
		OK:      s.CLIInstalled,
		Detail:  versionDetail(s.CLIVersion),
		FixHint: "在设置 → 依赖管理安装 @bitkyc08/opencodex",
		FixAction: "install-cli",
	})
	caps = append(caps, OcxCapability{
		Key:     "init",
		Label:   "已完成初始化",
		Desc:    "ocx init 写入 config.json：端口、provider、默认模型",
		OK:      s.ConfigInitialized,
		Detail:  initDetail(s),
		FixHint: "运行 ocx init 走一遍向导（或用管理面板配置）",
		FixAction: "init",
	})
	caps = append(caps, OcxCapability{
		Key:     "running",
		Label:   "代理正在运行",
		Desc:    "Codex 的请求要经过这个本地代理才能到 Loadout",
		OK:      s.ServiceRunning,
		Detail:  runningDetail(s),
		FixHint: "任选一种启动方式启动代理",
	})
	caps = append(caps, OcxCapability{
		Key:     "loadout",
		Label:   "已接入 Loadout",
		Desc:    "OpenCodex 里有指向本机 Loadout 服务的 provider",
		OK:      s.LoadoutProvider,
		Detail:  providerDetail(s),
		FixHint: "打开管理面板 → Providers → 添加自定义 provider",
		FixAction: "gui",
	})
	caps = append(caps, OcxCapability{
		Key:     "models",
		Label:   "Loadout 模型已就绪",
		Desc:    "同步时从这里取模型清单",
		OK:      s.ModelCount > 0,
		Detail:  modelDetail(s),
		FixHint: "在管理面板里给 Loadout provider 拉取模型，或点「同步配置」",
		FixAction: "sync",
	})
	caps = append(caps, OcxCapability{
		Key:     "codex",
		Label:   "Codex 已注入",
		Desc:    "Codex 的 config.toml 指向这个代理",
		OK:      s.CodexInjected,
		Detail:  codexDetail(s),
		FixHint: "运行 ocx sync 把代理写入 Codex 配置",
		FixAction: "sync",
	})
	caps = append(caps, OcxCapability{
		Key:     "protection",
		Label:   "重启后自动回来",
		Desc:    "装了后台服务或按需 shim，重启电脑后代理会自动拉起",
		OK:      s.RebootSafe,
		Detail:  protectionDetail(s),
		FixHint: "安装后台服务（推荐）或 Codex 自启动 shim",
		FixAction: "service-install",
	})
	s.Capabilities = caps

	// ---- 三种启动方式（官方文档：start / service / codex-shim）----
	// 兜底：调用方若没预初始化切片，这里补成非 nil，避免序列化成 null。
	if s.Missing == nil {
		s.Missing = []string{}
	}
	methods := make([]OcxMethod, 0, 3)
	methods = append(methods, OcxMethod{
		Key:       "start",
		Label:     "手动前台启动",
		Command:   "ocx start",
		Desc:      "最简单，窗口关掉就停；适合临时试一下",
		Ready:     s.CLIInstalled && s.ConfigInitialized,
		Active:    s.ServiceRunning && s.LaunchMode == OcxModeManual,
		Action:    "start",
		ActionLbl: "立即启动",
		Elevated:  true,
	})
	methods = append(methods, OcxMethod{
		Key:       "service",
		Label:     "后台服务（推荐）",
		Command:   "ocx service install",
		Desc:      "登录时自动启动、崩溃自动重启，需要管理员授权",
		Ready:     s.CLIInstalled && s.ConfigInitialized,
		Active:    s.ServiceInstalled,
		Action:    "service-install",
		ActionLbl: "安装并启动",
		Elevated:  true,
	})
	methods = append(methods, OcxMethod{
		Key:       "shim",
		Label:     "按需自启动",
		Command:   "ocx codex-shim install",
		Desc:      "不常驻，只有启动 Codex 时才把代理拉起来",
		Ready:     s.CLIInstalled && s.ConfigInitialized,
		Active:    s.ShimInstalled,
		Action:    "shim-install",
		ActionLbl: "安装 shim",
		Elevated:  false,
	})
	s.Methods = methods
}

func versionDetail(v string) string {
	if v == "" {
		return "版本未知"
	}
	return "v" + v
}

func initDetail(s *OcxStatus) string {
	if !s.ConfigInitialized {
		return "还没有 ~/.opencodex/config.json"
	}
	return fmt.Sprintf("%d 个 provider · 默认 %s", s.ProviderCount, orDash(s.DefaultProvider))
}

func runningDetail(s *OcxStatus) string {
	if !s.ServiceRunning {
		return "端口 " + fmt.Sprint(s.Port) + " 没有响应"
	}
	pid := "PID 未知"
	if s.ProxyPID > 0 {
		pid = "PID " + fmt.Sprint(s.ProxyPID)
	}
	ver := ""
	if s.ProxyVersion != "" {
		ver = " · v" + s.ProxyVersion
	}
	return fmt.Sprintf("端口 %d · %s%s", s.Port, pid, ver)
}

func providerDetail(s *OcxStatus) string {
	if !s.LoadoutProvider {
		return "没找到指向 " + s.LoadoutBaseURL + " 的 provider"
	}
	return "指向 " + s.LoadoutBaseURL
}

func modelDetail(s *OcxStatus) string {
	if s.ModelCount <= 0 {
		return "还没有可同步的模型"
	}
	return fmt.Sprintf("%d 个模型可同步", s.ModelCount)
}

func codexDetail(s *OcxStatus) string {
	if !s.CodexInjected {
		return "config.toml 里没有 opencodex 路由"
	}
	return s.ProxyURL + "/v1"
}

func protectionDetail(s *OcxStatus) string {
	switch s.Protection {
	case "service+shim":
		return "后台服务 + shim 双保险"
	case "service":
		return "后台服务已注册（" + orDash(s.ServiceBackend) + "）"
	case "shim":
		return "Codex 自启动 shim 已安装"
	}
	if s.ServiceRunning {
		return "代理是手动启动的，重启后不会自动回来"
	}
	return "没有安装服务或 shim"
}

func orDash(v string) string {
	if v == "" {
		return "-"
	}
	return v
}

// loadoutPorts 返回「可能是本机 Loadout 服务」的端口集合。
//
// 为什么不是一个端口：生产用 loadout-desktop 监听 :3000，而 debug.ps1 会把后端
// 起在 :5009。用户手里的 OpenCodex 配置通常指向 :3000（那是长期在跑的那个），
// 只认当前进程端口会把「明明配好了」误判成「没配」——实测就踩到过。
// 因此同时接受 3000（Loadout 默认端口）与当前进程的 LOADOUT_SERVER_ADDR 端口。
func loadoutPorts() map[string]bool {
	ports := map[string]bool{"3000": true}
	if addr := strings.TrimSpace(config.ServerAddr); addr != "" {
		if i := strings.LastIndexByte(addr, ':'); i >= 0 {
			if p := strings.TrimSpace(addr[i+1:]); p != "" {
				ports[p] = true
			}
		}
	}
	return ports
}

// isLoadoutBaseURL 判断 baseUrl 是否指向本机 Loadout 服务。
// 判定条件：回环主机（localhost / 127.0.0.1 / ::1）+ 端口属于 loadoutPorts()。
// 不带端口视为 80，不算 Loadout（Loadout 从不监听 80）。
func isLoadoutBaseURL(raw string) bool {
	u := strings.TrimSpace(raw)
	if u == "" {
		return false
	}
	// 补协议便于后续 host 提取（用户可能写 "localhost:3000"）。
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	u = strings.ToLower(u)
	host := u
	port := ""
	if i := strings.LastIndexByte(u, ':'); i >= 0 {
		host = u[:i]
		port = u[i+1:]
	}
	host = strings.Trim(host, "[]") // IPv6 形式 [::1]
	isLocal := host == "localhost" || host == "127.0.0.1" || host == "::1"
	if !isLocal {
		return false
	}
	return loadoutPorts()[port]
}

// normalizeLoadoutBase 把 baseUrl 归一化成 http://host:port 形式（去路径，保留协议）。
func normalizeLoadoutBase(raw string) string {
	u := strings.TrimSpace(raw)
	if !strings.Contains(u, "://") {
		u = "http://" + u
	}
	if i := strings.Index(u[8:], "/"); i >= 0 {
		u = u[:8+i]
	}
	return strings.TrimRight(u, "/")
}

// ocxPortOpen 探测本机 TCP 端口是否可连。
func ocxPortOpen(port int) bool {
	if port <= 0 {
		return false
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 1500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// readCodexInjected 读 ~/.codex/config.toml，判断是否含 [model_providers.opencodex]
// 且 base_url 指向本机代理端口。TOML 解析略重，这里做行级判断（OpenCodex 注入的
// 块固定包含这三行）：
//   [model_providers.opencodex]
//   base_url = "http://127.0.0.1:<port>/v1"
//   model_provider = "opencodex"
func readCodexInjected(port int) bool {
	p := ocxCodexConfigPath()
	if p == "" {
		return false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return false
	}
	text := strings.ToLower(string(data))
	if !strings.Contains(text, "[model_providers.opencodex]") {
		return false
	}
	if !strings.Contains(text, "base_url") {
		return false
	}
	// 端口必须匹配（避免 Codex 指到了别的代理端口）。
	wantBase := fmt.Sprintf("http://127.0.0.1:%d", port)
	wantBaseLocal := fmt.Sprintf("http://localhost:%d", port)
	return strings.Contains(text, wantBase) || strings.Contains(text, wantBaseLocal)
}

// ============================================================================
// 操作接口（启动 / 关闭 / 自启 / 打开 GUI / 同步配置）
// ============================================================================

// ocxRunCommand 是 procreg.Run 的 seam（测试可替换，避免真实执行命令）。
var ocxRunCommand = func(id, name string, args []string) error {
	cmd, err := exec.LookPath("ocx")
	if err != nil {
		return fmt.Errorf("未找到 ocx 指令（npm install -g @bitkyc08/opencodex）")
	}
	h, err := procreg.Run(procreg.Options{
		ID:   id,
		Name: name,
		Kind: "opencodex",
		Cmd:  cmd,
		Args: args,
	})
	if err != nil {
		return err
	}
	// 异步等待结束，不阻塞 handler；结束后 procreg 自动把进程移入历史。
	go func() { _ = h.Wait() }()
	return nil
}

// ocxElevatedCommand 在管理员 PowerShell 中执行一条 ocx 指令（Windows 专用）。
// 使用 PowerShell 的 Start-Process -Verb RunAs 弹 UAC 提示，结果写日志文件供前端查看。
// 非 Windows 平台降级为普通执行（ocx 在 Linux/macOS 以 systemd/launchd 方式管理）。
var ocxElevatedCommand = func(id, name string, args []string) error {
	if runtime.GOOS != "windows" {
		return ocxRunCommand(id, name, args)
	}
	cmdPath, err := exec.LookPath("ocx")
	if err != nil {
		return fmt.Errorf("未找到 ocx 指令（npm install -g @bitkyc08/opencodex）")
	}
	fullArgs := append([]string{cmdPath}, args...)
	return ocxRunElevatedImpl(id, name, fullArgs)
}

// ocxElevatedLogPath 返回一次提权执行对应的日志文件路径。
// 写在临时目录、文件名带 action 与时间戳，失败后用户可自行打开排查。
var ocxElevatedLogPath = func(id string) string {
	safe := strings.NewReplacer(":", "-", "/", "-", "\\", "-").Replace(id)
	if safe == "" {
		safe = fmt.Sprintf("ocx-%d", time.Now().UnixNano())
	}
	return filepath.Join(os.TempDir(), "loadout-"+safe+".log")
}

// quoteForSingleQuotes 把字符串包进 PowerShell 单引号字符串（内部 ' 翻倍）。
// 仅 ocxRunInteractive（交互式 init 向导）还在用 PowerShell。
func quoteForSingleQuotes(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// ocxRunElevatedImpl 以管理员身份执行 ocx 子命令（Windows）。
//
// 实现要点（与旧版 PowerShell 方案的区别）：
//   - 提权用系统 API ShellExecuteExW(runas)，不再拼接 PowerShell 字符串。
//     之前把命令塞进 -Command/-File 里，引号或制表符任何一处出错都会让
//     PowerShell 直接语法报错、窗口一闪就退，用户什么也看不到。
//   - 命令由 CMD 执行，输出重定向到日志文件（CMD 的引号规则简单，无语法陷阱）。
//   - Go 侧跟随该文件并逐行写入 procreg（AppendLog 会实时广播 SSE），
//     所以进度弹窗里能看到真实输出，失败时也能看到退出码与完整日志路径。
func ocxRunElevatedImpl(id, name string, fullArgs []string) error {
	if len(fullArgs) == 0 {
		return fmt.Errorf("缺少要执行的命令")
	}
	logPath := ocxElevatedLogPath(id)
	batchPath := strings.TrimSuffix(logPath, ".log") + ".cmd"

	// 日志先清空，避免上一次的残留被当成本次结果。
	_ = os.Remove(logPath)

	exe, args := fullArgs[0], fullArgs[1:]
	if err := os.WriteFile(batchPath, []byte(buildElevatedBatch(exe, args, logPath)), 0o600); err != nil {
		return fmt.Errorf("写入提权脚本失败: %w", err)
	}

	// 登记到进程面板：先建一条运行中的记录，日志由 AppendLog 实时追加。
	h := procreg.RegisterExisting(id, name+"（管理员）", "opencodex", nil)
	_ = h
	procreg.AppendLogPackage(id, fmt.Sprintf("> %s %s", exe, strings.Join(args, " ")))
	procreg.AppendLogPackage(id, "正在请求管理员授权（UAC）…")

	handle, err := startElevated(batchPath)
	if err != nil {
		msg := "提权失败：" + err.Error() + "（如果点了「否」，请重新点击并在 UAC 对话框选择「是」）"
		procreg.AppendLogPackage(id, msg)
		procreg.Unregister(id, err)
		return nil // 错误已写进日志，前端能看到
	}

	go func() {
		exited := make(chan int, 1)
		go func() { exited <- waitElevated(handle) }()

		var code int
		got := false
		select {
		case code = <-exited:
			got = true
		case <-time.After(10 * time.Minute):
			// 超时不强行结束，交给日志跟随收尾。
		}

		stop := make(chan struct{})
		tailLog(logPath, 0, func(line string) {
			procreg.AppendLogPackage(id, line)
		}, stop, func() bool { return got })
		close(stop)

		if !got {
			code = <-exited
		}
		if code == 0 {
			procreg.AppendLogPackage(id, "完成。")
			procreg.Unregister(id, nil)
			return
		}
		procreg.AppendLogPackage(id, fmt.Sprintf("命令失败，退出码 %d。完整日志: %s", code, logPath))
		procreg.Unregister(id, fmt.Errorf("退出码 %d", code))
	}()
	return nil
}

// ocxRunInteractive 在可见的管理员终端里跑交互式 ocx 命令（init / setup）。
// 与 ocxRunElevatedImpl 的区别：不用 -Wait、不隐藏窗口，用户能看到提问并自己回答。
// 命令结束后窗口保留（-NoExit），方便用户回看向导输出。
func ocxRunInteractive(id, name string, args []string) error {
	cmdPath, err := exec.LookPath("ocx")
	if err != nil {
		return fmt.Errorf("未找到 ocx 指令（npm install -g @bitkyc08/opencodex）")
	}
	fullArgs := append([]string{cmdPath}, args...)
	inner := "& " + quoteForSingleQuotes(strings.Join(fullArgs, " "))

	var ps *exec.Cmd
	if runtime.GOOS == "windows" {
		// -NoExit：命令结束后保留窗口；-Verb RunAs 提权。
		outer := fmt.Sprintf(
			"Start-Process powershell -Verb RunAs -ArgumentList %s",
			quoteForSingleQuotes("-NoProfile -ExecutionPolicy Bypass -NoExit -Command "+inner),
		)
		ps = exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", outer)
	} else {
		// 非 Windows：直接开终端（macOS Terminal / Linux x-terminal-emulator 兜底到当前进程）。
		ps = exec.Command("sh", "-c", "command -v open >/dev/null && open -a Terminal . || true")
	}
	if err := ps.Start(); err != nil {
		return fmt.Errorf("打开交互式终端失败: %w", err)
	}
	h := procreg.RegisterExisting(id, name+"（交互终端）", "opencodex", ps)
	go func() {
		runErr := ps.Wait()
		procreg.Unregister(id, runErr)
	}()
	_ = h
	return nil
}

// handleOcxStatus GET /api/opencodex/status → 检测结果。
func (s *Service) handleOcxStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.OcxStatus())
}

// ocxElevatedActions 需要管理员权限的 ocx 子命令。
//   service install / repair / start / stop / uninstall —— Windows 任务计划程序要提权
//   start / stop —— 会改动服务注册与 Codex 配置
// 其余（gui / sync / codex-shim / init）用普通权限即可。
var ocxElevatedActions = map[string]bool{
	"start":             true,
	"stop":              true,
	"autostart":         true,
	"service-install":   true,
	"service-repair":    true,
	"service-start":     true,
	"service-stop":      true,
	"service-uninstall": true,
}

// ocxInteractiveActions 需要用户自己回答的交互式命令。
// ocx init 会问 provider / API key / 模型 / 端口，不能由后端代答。
// 这里开一个管理员终端窗口，让用户操作，命令跑完窗口保留结果。
var ocxInteractiveActions = map[string]bool{
	"init": true,
}

// ocxActionHandler 通用操作端点。
// 根据动作表自动决定提权与交互，调用方不再传 elevated。
func (s *Service) ocxActionHandler(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		id := req.ID
		if id == "" {
			id = fmt.Sprintf("ocx:%s:%d", action, time.Now().UnixNano())
		}
		var args []string
		var display string
		switch action {
		case "start":
			args = []string{"start"}
			display = "OpenCodex 启动服务"
		case "stop":
			args = []string{"stop"}
			display = "OpenCodex 关闭服务"
		case "autostart":
			args = []string{"service", "install"}
			display = "OpenCodex 安装后台服务"
		case "service-install":
			args = []string{"service", "install"}
			display = "OpenCodex 安装后台服务"
		case "service-repair":
			args = []string{"service", "repair"}
			display = "OpenCodex 修复后台服务"
		case "service-start":
			args = []string{"service", "start"}
			display = "OpenCodex 启动后台服务"
		case "service-stop":
			args = []string{"service", "stop"}
			display = "OpenCodex 停止后台服务"
		case "service-uninstall":
			args = []string{"service", "uninstall"}
			display = "OpenCodex 卸载后台服务"
		case "shim-install":
			args = []string{"codex-shim", "install"}
			display = "OpenCodex 安装按需自启动"
		case "shim-uninstall":
			args = []string{"codex-shim", "uninstall"}
			display = "OpenCodex 卸载按需自启动"
		case "init":
			// ocx init 是交互式向导（要选 provider / 填 key / 选模型），
			// 跑到这里只打开一个管理员终端让用户自己完成，不代答。
			args = []string{"init"}
			display = "OpenCodex 初始化向导"
		case "gui":
			args = []string{"gui"}
			display = "OpenCodex 管理面板"
		case "sync":
			args = []string{"sync"}
			display = "OpenCodex 同步配置"
		default:
			writeError(w, http.StatusBadRequest, "未知操作: "+action)
			return
		}
		var err error
		if ocxInteractiveActions[action] {
			err = ocxRunInteractive(id, display, args)
		} else if ocxElevatedActions[action] {
			err = ocxElevatedCommand(id, display, args)
		} else {
			err = ocxRunCommand(id, display, args)
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"started": true, "id": id})
	}
}
// ----------------------------------------------------------------------------
// 生命周期分层（对应官方 docs-site/reference/cli/lifecycle）：
//   ① CLI 可执行        npm install -g @bitkyc08/opencodex
//   ② 初始化 config     ocx init / ocx setup 写 ~/.opencodex/config.json
//   ③ 代理在跑          ocx start（前台）| ocx service（后台）| ocx codex-shim（按需）
//   ④ Codex 已注入      ocx sync 写 ~/.codex/config.toml
//   ⑤ 开机/崩溃保护      service 或 shim，决定 rebootSafe
// 检测只读本地文件 + 0.6s 的 /healthz；绝不调用 ocx status（实测 17~21s，会卡死 UI）。
// ----------------------------------------------------------------------------

// OcxLaunchMode 当前代理是由哪种方式拉起的。
//   service  = ocx service install 注册的后台服务（Windows 任务计划程序）
//   shim     = ocx codex-shim install 的按需启动（只在 codex 启动时拉起）
//   tray     = OpenCodex 桌面应用的托盘常驻
//   manual   = 手动 ocx start（关掉终端就没了）
//   unknown  = 进程在跑但来源不可判定
type OcxLaunchMode string

const (
	OcxModeService OcxLaunchMode = "service"
	OcxModeShim    OcxLaunchMode = "shim"
	OcxModeTray    OcxLaunchMode = "tray"
	OcxModeManual  OcxLaunchMode = "manual"
	OcxModeUnknown OcxLaunchMode = "unknown"
)

// OcxCapability 一个可勾选的生命周期能力（前端渲染成带说明的清单）。
type OcxCapability struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Desc      string `json:"desc"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail,omitempty"`
	FixHint   string `json:"fixHint,omitempty"`
	FixAction string `json:"fixAction,omitempty"`
}

// OcxMethod 一种启动方式的可用状态（前端渲染成方式选择卡）。
type OcxMethod struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Command   string `json:"command"`
	Desc      string `json:"desc"`
	Ready     bool   `json:"ready"`
	Active    bool   `json:"active"`
	Action    string `json:"action,omitempty"`
	ActionLbl string `json:"actionLabel,omitempty"`
	Elevated  bool   `json:"elevated"`
}

// ocxServiceState 读 ~/.opencodex/service-state.json 得到的服务注册信息。
type ocxServiceState struct {
	Installed bool
	Backend   string
}

// ocxServiceStatePath 返回 ~/.opencodex/service-state.json（服务注册凭据）。
var ocxServiceStatePath = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opencodex", "service-state.json")
}

// readServiceState 读 service-state.json 判服务是否注册。
// 不跑 ocx service status：实测 3.4 秒，且和配置文件表达的是同一件事。
func readServiceState() ocxServiceState {
	p := ocxServiceStatePath()
	if p == "" {
		return ocxServiceState{}
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ocxServiceState{}
	}
	var raw struct {
		Backend string `json:"backend"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return ocxServiceState{}
	}
	return ocxServiceState{Installed: true, Backend: raw.Backend}
}

// shimMarkerCandidates 列出各平台可能的 Codex 自启动 shim 标记路径。
// Unix：OPENCODEX_HOME/bin/codex 私有 wrapper；Windows：没有独立文件，靠状态元数据。
func shimMarkerCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(home, ".opencodex")
	return []string{
		filepath.Join(base, "bin", "codex"),
		filepath.Join(base, "codex-shim.json"),
		filepath.Join(base, "codex-shim-state.json"),
	}
}

// readShimInstalled 判定 Codex 自启动 shim 是否已安装。
func readShimInstalled() bool {
	for _, p := range shimMarkerCandidates() {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

