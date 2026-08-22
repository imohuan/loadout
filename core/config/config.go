// Package config 集中管理 Loadout 的所有「程序级」配置。
//
// 设计原则（见 DESIGN.md 4.2）：
//   - 每一项配置都有「默认值」与「环境变量」两个来源；
//   - 读取顺序：环境变量优先，环境变量未设置/为空时才回落到默认值；
//   - 运行时数据（渠道、路由、MCP 列表、key 等）不在这里，放在 ~/.loadout/data/*.json。
//
// 环境变量统一使用 LOADOUT_ 前缀，命名规则为「大写下划线」，例如：
//
//	LOADOUT_SERVER_ADDR=:4000 LOADOUT_LOG_LEVEL=debug ./loadout
//
// 所有导出变量在 init() 阶段一次性读入；需要重新加载（如测试）时调用 Load()。
package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ============ 应用信息 ============

// AppName 应用名，用于日志前缀、窗口标题、目录名。
var AppName = "Loadout" // env: LOADOUT_APP_NAME

// Version 版本号，随每次发布更新。
var Version = "0.1.0" // env: LOADOUT_VERSION

// ============ 运行模式 ============

// RunMode: "server"（Linux 服务器，监听全网卡）或 "desktop"（Windows 桌面，仅监听 127.0.0.1）。
var RunMode = "server" // env: LOADOUT_RUN_MODE

// ============ 目录路径 ============

// HomeDir 数据根目录（日志、数据、技能仓库、备份都在这里），支持 "~" 前缀。
var HomeDir = "~/.loadout" // env: LOADOUT_HOME_DIR

// AgentSkillsDir 技能预设的目标目录（切换预设时在此重建链接），支持 "~" 前缀。
var AgentSkillsDir = "~/.agents/skills" // env: LOADOUT_AGENT_SKILLS_DIR

// 以下派生目录在 Load() 中基于 HomeDir 解析后计算得到。

// DataDir JSON 数据目录（原子写入的 *.json 与 .secret 都在这）。
var DataDir string

// SkillsDir 技能完整仓库（所有安装过的技能真实文件所在，永不删除）。
var SkillsDir string

// LogsDir 日志文件目录（loadout.log，轮转）。
var LogsDir string

// BackupsDir 备份目录（一键备份命令的输出）。
var BackupsDir string

// AdminPasswordFile 首启随机密码存放文件（无后缀，0600 权限）。
var AdminPasswordFile string

// SecretFile 本地密钥文件（0600 权限，用于 AES 加密与 JWT 签名）。
var SecretFile string

// VisionCacheDir 视觉描述缓存目录（图片 md5 → 描述文本）。
var VisionCacheDir string

// ============ 端口 ============

// ServerAddr 统一监听端口（RunMode 决定监听 127.0.0.1 还是全网卡）。
// 单端口服务，三类入口按路径分发：
//
//	/v1/*  → 模型 API（sk- key）
//	/mcp/* → MCP 端点（header key，单 MCP/分组/$smart 三种路由方式）
//	其余   → 管理后台（session）
var ServerAddr = ":3000" // env: LOADOUT_SERVER_ADDR

// ============ 超时 ============

// UpstreamTimeout 转发上游的最大时长（含流式生成全程）。
var UpstreamTimeout = 300 * time.Second // env: LOADOUT_UPSTREAM_TIMEOUT

// VisionTimeout 视觉模型调用超时。
var VisionTimeout = 60 * time.Second // env: LOADOUT_VISION_TIMEOUT

// MCPInvokeTimeout MCP 工具调用超时。
var MCPInvokeTimeout = 120 * time.Second // env: LOADOUT_MCP_INVOKE_TIMEOUT

// McpListToolsTimeout 构建工具索引时，单个上游 ListTools 的超时。
// 上游挂起时若无超时，$smart / 分组端点会无限卡住（BuildIndex 用 Background ctx）。
var McpListToolsTimeout = 15 * time.Second // env: LOADOUT_MCP_LIST_TOOLS_TIMEOUT

// HTTPReadTimeout 普通 HTTP 请求读超时。
var HTTPReadTimeout = 10 * time.Second // env: LOADOUT_HTTP_READ_TIMEOUT

// ============ 日志 ============

// LogLevel 日志级别：debug/info/warn/error。
var LogLevel = "info" // env: LOADOUT_LOG_LEVEL

// LogMaxSizeMB 单个日志文件多大（MB）后轮转。
var LogMaxSizeMB = 50 // env: LOADOUT_LOG_MAX_SIZE_MB

// LogMaxBackups 保留多少个历史日志文件。
var LogMaxBackups = 7 // env: LOADOUT_LOG_MAX_BACKUPS

// LogMaxAgeDays 历史日志最多保留多少天。
var LogMaxAgeDays = 30 // env: LOADOUT_LOG_MAX_AGE_DAYS

// ============ 认证 ============

// SessionTTLHours 管理后台登录有效期（小时）。
var SessionTTLHours = 24 * 7 // env: LOADOUT_SESSION_TTL_HOURS

// ============ 模型网关 ============

// DefaultVisionModel 默认视觉模型（能力路由表 via_options 为空时兜底）。
// var DefaultVisionModel = "qwen-vl-max" // env: LOADOUT_DEFAULT_VISION_MODEL
var DefaultVisionModel = "qwen3.7-flash-2026-07-15" // env: LOADOUT_DEFAULT_VISION_MODEL

// ============ 火山引擎免费额度 ============

// VolcQuotaMinRemaining 本地免费额度最低保留阈值：local_remaining <= 该值即视为
// "耗尽"并拦截（volc-free-quota 插件，force_block=1 生效）。默认 0 = 扣到 0 才拦；
// 配 10000 则剩余不足 1 万就停止，防止额度冲成负数。
var VolcQuotaMinRemaining = 200000 // env: LOADOUT_VOLC_QUOTA_MIN_REMAINING

// VisionDescriptionPrompt 视觉描述提示词（多图合并描述）。
// 空字符串时使用 vision 插件内置的结构化板块模板（plugins/vision/prompt.go）。
// 设过旧版自由文本提示词的部署在升级后需清掉该 env，否则结构化输出不生效。
var VisionDescriptionPrompt string // env: LOADOUT_VISION_DESCRIPTION_PROMPT

// VisionCompressEnabled 图片压缩开关（仅处理 base64 data URI，远程 URL 透传）。
var VisionCompressEnabled = true // env: LOADOUT_VISION_COMPRESS_ENABLED

// VisionCompressMinBytes 图片字节数小于该值不压缩（小图压缩无意义）。
var VisionCompressMinBytes = 512 * 1024 // env: LOADOUT_VISION_COMPRESS_MIN_BYTES

// VisionMaxEdgePx 图片最长边超过该像素数才等比缩放（2048 是视觉模型输入典型上限）。
var VisionMaxEdgePx = 2048 // env: LOADOUT_VISION_MAX_EDGE_PX

// VisionCompressQuality JPEG 重编码质量（1-100）。有损重编码 + 缩放会轻微模糊
// 小字号文字，通常不影响模型识别，但截图类文本密集图优先考虑 PNG 输入。
var VisionCompressQuality = 90 // env: LOADOUT_VISION_COMPRESS_QUALITY

// VisionMaxImageBytes 单张图片字节硬上限，超过直接报错（防内存耗尽）。
var VisionMaxImageBytes = 25 * 1024 * 1024 // env: LOADOUT_VISION_MAX_IMAGE_BYTES

// VisionMaxImages 单次请求图片张数上限（25MB × N 的 base64 + 解码内存峰值）。
var VisionMaxImages = 10 // env: LOADOUT_VISION_MAX_IMAGES

// ============ 转发日志自愈 ============

// RouteLogSelfHealTimeout 启用转发日志"卡死 running"自愈：
// 前端 /api/route-logs/{id} 命中"started_at 之后超过该阈值仍卡在 running 且
// finished_at 为空"的记录时，后端推断 finished_at=now、duration_ms=now-started_at、
// result=stream_interrupted，并写回 DB；返回的详情会反映已修复的字段。
// 修复仅在 result='running' 且 finished_at 为空且 age>threshold 时生效，
// 不影响正常结束的请求。设为 0 或负值禁用。env: LOADOUT_ROUTE_LOG_SELF_HEAL_TIMEOUT
var RouteLogSelfHealTimeout = 60 * time.Second

// RouteLogSelfHealMaxAlive 活跃登记表的最大存活时间：proxyForward 开始登记、
// 结束注销；请求仍活跃且未超该值 → 自愈跳过（可能只是慢）。超过该值即使
// 登记表里有也按死锁/泄漏判死。进程崩溃时表随进程消失，残留 running 日志
// 因此天然判死。env: LOADOUT_ROUTE_LOG_SELF_HEAL_MAX_ALIVE
var RouteLogSelfHealMaxAlive = 10 * time.Minute

// ReasoningInjectionStyle 流式注入风格：reasoning_content（DeepSeek 系）。
var ReasoningInjectionStyle = "reasoning_content" // env: LOADOUT_REASONING_INJECTION_STYLE

// ============ MCP 网关 ============

// MaxToolResultChars invoke 结果截断上限（字符）。
var MaxToolResultChars = 8000 // env: LOADOUT_MAX_TOOL_RESULT_CHARS

// StatusFlatThreshold status 无参数时，工具总数 ≤ 该值直接返回完整列表（省一轮往返）。
var StatusFlatThreshold = 10 // env: LOADOUT_STATUS_FLAT_THRESHOLD

// ToolConflictPrefix 同名工具冲突时自动加「来源_」前缀。
var ToolConflictPrefix = true // env: LOADOUT_TOOL_CONFLICT_PREFIX

// SmartGroupHeader $smart 端点通过该 header 指定分组名（空 = 全部工具）。
var SmartGroupHeader = "X-Loadout-Group" // env: LOADOUT_SMART_GROUP_HEADER

// ============ skills ============

// SkillInstallMode 技能安装方式："git"（git clone，可靠落盘到 ~/.loadout/skills）
// 或 "npx"（npx skills CLI 下载后搬运）。
var SkillInstallMode = "git" // env: LOADOUT_SKILL_INSTALL_MODE

// SkillBodyMaxChars SKILL.md 通过 get 返回时的截断上限（字符）。
var SkillBodyMaxChars = 20000 // env: LOADOUT_SKILL_BODY_MAX_CHARS

// SkillWatchRecursive 是否启用 fsnotify 递归监听 ~/.agents/skills：
// 技能目录内部任何文件新增/修改都会触发同步（删除不反向同步到技能库）。
var SkillWatchRecursive = true // env: LOADOUT_SKILL_WATCH_RECURSIVE

// SkillWatchPolling 是否启用定时全量扫描兜底：
// 周期性比对 ~/.agents/skills 与技能库，补齐递归监听漏掉的变化（如进程重启间隙）。
var SkillWatchPolling = false // env: LOADOUT_SKILL_WATCH_POLLING

// SkillWatchDebounce 递归监听的事件防抖窗口：窗口内事件合并，窗口结束才执行同步，
// 避免编辑器"临时文件+rename"产生的事件风暴把半写状态同步过去。
var SkillWatchDebounce = 1 * time.Second // env: LOADOUT_SKILL_WATCH_DEBOUNCE

// SkillWatchPollInterval 定时全量扫描的间隔。
var SkillWatchPollInterval = 5 * time.Minute // env: LOADOUT_SKILL_WATCH_POLL_INTERVAL

// ============ 视觉描述缓存 ============

// VisionCacheEnabled 同图复用描述（md5 缓存）。
var VisionCacheEnabled = true // env: LOADOUT_VISION_CACHE_ENABLED

// VisionCacheTTLHours 缓存有效期（小时）。
var VisionCacheTTLHours = 24 * 7 // env: LOADOUT_VISION_CACHE_TTL_HOURS

// VisionHistoryMode 历史消息中旧图的处理策略：
// cache（默认）：历史旧图用缓存描述替换，缓存 miss 用占位符（不调视觉模型）；
// placeholder：历史旧图一律占位符，不读缓存；
// keep：保持现状（旧图也完整识别），兼容回退。
var VisionHistoryMode = "cache" // env: LOADOUT_VISION_HISTORY_MODE

// ============ 加载逻辑 ============

// Load 读取所有环境变量，回落到默认值，并计算派生目录。
// init() 会调用一次；测试或运行时重载可再次调用。
func Load() {
	AppName = strEnv("LOADOUT_APP_NAME", "Loadout")
	Version = strEnv("LOADOUT_VERSION", "0.1.0")
	RunMode = strEnv("LOADOUT_RUN_MODE", "server")

	HomeDir = strEnv("LOADOUT_HOME_DIR", "~/.loadout")
	AgentSkillsDir = strEnv("LOADOUT_AGENT_SKILLS_DIR", "~/.agents/skills")

	ServerAddr = strEnv("LOADOUT_SERVER_ADDR", ":3000")

	UpstreamTimeout = durEnv("LOADOUT_UPSTREAM_TIMEOUT", 300*time.Second)
	VisionTimeout = durEnv("LOADOUT_VISION_TIMEOUT", 60*time.Second)
	MCPInvokeTimeout = durEnv("LOADOUT_MCP_INVOKE_TIMEOUT", 120*time.Second)
	McpListToolsTimeout = durEnv("LOADOUT_MCP_LIST_TOOLS_TIMEOUT", 15*time.Second)
	HTTPReadTimeout = durEnv("LOADOUT_HTTP_READ_TIMEOUT", 10*time.Second)

	LogLevel = strEnv("LOADOUT_LOG_LEVEL", "info")
	LogMaxSizeMB = intEnv("LOADOUT_LOG_MAX_SIZE_MB", 50)
	LogMaxBackups = intEnv("LOADOUT_LOG_MAX_BACKUPS", 7)
	LogMaxAgeDays = intEnv("LOADOUT_LOG_MAX_AGE_DAYS", 30)

	SessionTTLHours = intEnv("LOADOUT_SESSION_TTL_HOURS", 24*7)

	DefaultVisionModel = strEnv("LOADOUT_DEFAULT_VISION_MODEL", "qwen-vl-max")
	VolcQuotaMinRemaining = intEnv("LOADOUT_VOLC_QUOTA_MIN_REMAINING", 0)
	VisionDescriptionPrompt = strEnv("LOADOUT_VISION_DESCRIPTION_PROMPT", "")
	VisionCompressEnabled = boolEnv("LOADOUT_VISION_COMPRESS_ENABLED", true)
	VisionCompressMinBytes = intEnv("LOADOUT_VISION_COMPRESS_MIN_BYTES", 512*1024)
	VisionMaxEdgePx = intEnv("LOADOUT_VISION_MAX_EDGE_PX", 2048)
	VisionCompressQuality = intEnv("LOADOUT_VISION_COMPRESS_QUALITY", 90)
	VisionMaxImageBytes = intEnv("LOADOUT_VISION_MAX_IMAGE_BYTES", 25*1024*1024)
	VisionMaxImages = intEnv("LOADOUT_VISION_MAX_IMAGES", 10)
	ReasoningInjectionStyle = strEnv("LOADOUT_REASONING_INJECTION_STYLE", "reasoning_content")

	MaxToolResultChars = intEnv("LOADOUT_MAX_TOOL_RESULT_CHARS", 8000)
	StatusFlatThreshold = intEnv("LOADOUT_STATUS_FLAT_THRESHOLD", 10)
	ToolConflictPrefix = boolEnv("LOADOUT_TOOL_CONFLICT_PREFIX", true)
	SmartGroupHeader = strEnv("LOADOUT_SMART_GROUP_HEADER", "X-Loadout-Group")

	SkillInstallMode = strEnv("LOADOUT_SKILL_INSTALL_MODE", "git")
	SkillBodyMaxChars = intEnv("LOADOUT_SKILL_BODY_MAX_CHARS", 20000)
	SkillWatchRecursive = boolEnv("LOADOUT_SKILL_WATCH_RECURSIVE", true)
	SkillWatchPolling = boolEnv("LOADOUT_SKILL_WATCH_POLLING", false)
	SkillWatchDebounce = durEnv("LOADOUT_SKILL_WATCH_DEBOUNCE", 1*time.Second)
	SkillWatchPollInterval = durEnv("LOADOUT_SKILL_WATCH_POLL_INTERVAL", 5*time.Minute)

	VisionCacheEnabled = boolEnv("LOADOUT_VISION_CACHE_ENABLED", true)
	VisionCacheTTLHours = intEnv("LOADOUT_VISION_CACHE_TTL_HOURS", 24*7)
	VisionHistoryMode = strEnv("LOADOUT_VISION_HISTORY_MODE", "cache")

	// 派生目录：基于解析后的 HomeDir 计算。
	home := ResolveHomeDir()
	DataDir = filepath.Join(home, "data")
	SkillsDir = filepath.Join(home, "skills")
	LogsDir = filepath.Join(home, "logs")
	BackupsDir = filepath.Join(home, "backups")
	AdminPasswordFile = filepath.Join(home, "admin-password")
	SecretFile = filepath.Join(DataDir, ".secret")
	VisionCacheDir = filepath.Join(DataDir, "vision-cache")
}

func init() {
	Load()
}

// ResolveHomeDir 展开 HomeDir 中的 "~" 前缀为当前用户主目录。
func ResolveHomeDir() string {
	return expandHome(HomeDir)
}

// ResolveAgentSkillsDir 展开 AgentSkillsDir 中的 "~" 前缀为当前用户主目录。
func ResolveAgentSkillsDir() string {
	return expandHome(AgentSkillsDir)
}

// expandHome 把路径开头的 "~" 或 "~/" 替换为用户主目录。
func expandHome(path string) string {
	if path == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
		return path
	}
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, path[2:])
		}
	}
	return path
}

// ============ 环境变量读取辅助 ============

// strEnv 读取字符串环境变量；未设置或为空时返回默认值。
func strEnv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

// intEnv 读取整数环境变量；未设置或解析失败时返回默认值。
func intEnv(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return def
	}
	return n
}

// boolEnv 读取布尔环境变量；未设置或解析失败时返回默认值。
// 接受 "1"/"true"/"yes"/"on"（真）与 "0"/"false"/"no"/"off"（假），不区分大小写。
func boolEnv(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return def
}

// durEnv 读取时长环境变量；支持 Go 时长格式（"300s"、"2m"）或纯整数秒（"300"）。
// 未设置或解析失败时返回默认值。
func durEnv(key string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	v = strings.TrimSpace(v)
	if d, err := time.ParseDuration(v); err == nil {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil {
		return time.Duration(n) * time.Second
	}
	return def
}
