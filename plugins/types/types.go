// Package types 定义 Loadout 运行时数据的 JSON 结构（DESIGN.md 第 5 节）。
//
// 这些结构体对应 ~/.loadout/data/*.json 里的记录，由各业务插件通过
// core/store 读写。所有字段的 JSON tag 与 DESIGN.md 5.1~5.11 保持一致。
package types

import "strings"

// ============ 数据文件名常量 ============

const (
	FileUsers            = "users.json"             // 管理员账号
	FileAPIKeys          = "api_keys.json"          // 模型 API key（sk-）
	FileMCPKeys          = "mcp_keys.json"          // MCP endpoint key
	FileChannels         = "channels.json"          // 上游渠道
	FileCapabilityRoutes = "capability_routes.json" // 能力路由表
	FileMCPServers       = "mcp_servers.json"       // 上游 MCP 服务器
	FileToolsState       = "tools_state.json"       // 单工具开关与分类
	FileGroups           = "groups.json"            // 分组
	FileSkills           = "skills.json"            // 技能仓库清单
	FilePresets          = "presets.json"           // 技能预设
	FileSettings         = "settings.json"          // 运行时设置
	FileAggregates       = "aggregates.json"        // 聚合模型（轮询）
	FileModelHealth      = "model_health.json"      // 模型健康状态
)

// ============ 5.1 管理员账号 ============

// User 管理员账号（单账号）。
type User struct {
	Username        string `json:"username"`         // 登录名（admin）
	PasswordHash    string `json:"password_hash"`    // bcrypt 哈希
	PasswordChanged bool   `json:"password_changed"` // 是否已修改初始密码
}

// ============ 5.2 模型 API key（sk-） ============

// APIKey 模型 API key。完整 key 只在创建时展示一次，落盘只存哈希。
type APIKey struct {
	ID        string   `json:"id"`         // 唯一 ID
	Name      string   `json:"name"`       // 备注名，如「本机调用」
	Prefix    string   `json:"prefix"`     // 展示用前缀，如 sk-abc
	Hash      string   `json:"hash"`       // 完整 key 的 sha256 哈希
	Models    []string `json:"models"`     // 允许的模型；空或 ["*"] 表示不限制
	Enabled   bool     `json:"enabled"`    // 是否启用
	CreatedAt string   `json:"created_at"` // 创建时间（RFC3339）
}

// ============ 5.3 MCP endpoint key ============

// MCPKey 一把 key 绑定一个 MCP 端点。
type MCPKey struct {
	Endpoint   string `json:"endpoint"`    // 端点路径，如 /mcp/group1
	HeaderName string `json:"header_name"` // 自定义 header 名，默认 X-Loadout-Key
	Hash       string `json:"hash"`        // key 的 sha256 哈希
}

// ============ 5.4 上游渠道 ============

// Channel 上游渠道（NewAPI 及任何 OpenAI 兼容地址）。APIKey 用 AES-GCM 加密存储。
type Channel struct {
	ID            string               `json:"id"`                      // 唯一 ID
	Name          string               `json:"name"`                    // 名称
	BaseURL       string               `json:"base_url"`                // 地址，如 https://api.deepseek.com/v1
	APIKeyCipher  string               `json:"api_key_cipher"`          // AES 密文（密钥在 .secret）
	Enabled       bool                 `json:"enabled"`                 // 旧 JSON 兼容字段
	ManualEnabled bool                 `json:"manual_enabled"`          // 手动开关的唯一来源
	SyncBilling   bool                 `json:"sync_billing"`            // 明确渠道余额错误时是否传播
	Models        []string             `json:"models,omitempty"`        // 启用的模型 id 列表（兼容旧字段；空 = 未知）
	ModelsDetail  []ChannelModelDetail `json:"models_detail,omitempty"` // 完整模型清单（含禁用/手动来源；DB 版返回）
	ModelsError   string               `json:"models_error,omitempty"`  // 最近一次 /v1/models 探测失败原因；空 = 正常
	CreatedAt     string               `json:"created_at,omitempty"`
	UpdatedAt     string               `json:"updated_at,omitempty"`
}

// ChannelModelDetail 渠道模型详情：model/来源/开关（DB 版渠道列表返回完整清单，
// 含禁用的模型与手动添加的模型，供管理后台展示与编辑）。
type ChannelModelDetail struct {
	Model   string `json:"model"`
	Source  string `json:"source"` // probe（探测）/ manual（手动配置）
	Enabled bool   `json:"enabled"`
}

// ============ 5.5 能力路由表 ============

// 能力路由的 route 取值。
const (
	RouteNative = "native" // 模型原生支持，直接透传
	RouteProxy  = "proxy"  // 转发给 via_options（视觉等能力）
	RouteError  = "error"  // 明确不支持且不附加，直接报错
)

// MatchModels 判断 model 是否命中目标模型列表：支持 `*` 全匹配、`prefix*` 前缀匹配、精确匹配。
func MatchModels(models []string, model string) bool {
	for _, m := range models {
		if m == "*" {
			return true
		}
		if strings.HasSuffix(m, "*") {
			if strings.HasPrefix(model, strings.TrimSuffix(m, "*")) {
				return true
			}
			continue
		}
		if m == model {
			return true
		}
	}
	return false
}

// MatchChannel 判断 channelID 是否命中渠道约束列表：
//   - 空列表 或 列表含 `*` = 全渠道（不约束，任何渠道都命中）；
//   - 列表非空且不含 `*` 时，channelID 为空（请求渠道未知）不命中，须精确匹配列表内 id。
//
// `*` 与模型通配语义一致，由前端"通用（全匹配）"选项写入。
func MatchChannel(channelIDs []string, channelID string) bool {
	if len(channelIDs) == 0 {
		return true
	}
	for _, id := range channelIDs {
		if id == "*" {
			return true
		}
	}
	if channelID == "" {
		return false
	}
	for _, id := range channelIDs {
		if id == channelID {
			return true
		}
	}
	return false
}

// ViaOption 视觉兜底候选：视觉模型 + 可选渠道，按数组顺序从上到下依次请求（failover）。
type ViaOption struct {
	ViaModel  string `json:"via_model"`            // 视觉模型名
	ChannelID string `json:"channel_id,omitempty"` // 渠道 id；空 = 按 via_model 自动路由（走 /v1/models 探测兜底）
}

// SensitiveReplacement 敏感词替换规则：from → to。
// Regex=true 时 from 按正则匹配，to 支持 $1 等捕获组引用（Go regexp 语义）。
type SensitiveReplacement struct {
	From  string `json:"from"`            // 原始内容/敏感词（或正则表达式）
	To    string `json:"to"`              // 替换后的内容
	Regex bool   `json:"regex,omitempty"` // true = from 按正则匹配
}

// CapabilityRoute 能力路由表条目：目标模型（可多个/通配符）× 渠道 × 能力 矩阵。
type CapabilityRoute struct {
	Models       []string               `json:"models"`                // 目标模型列表，支持 `*` 通配与 `prefix*` 前缀匹配
	ChannelIDs   []string               `json:"channel_ids,omitempty"` // 目标模型绑定的渠道列表（可多选）；空 = 全渠道生效
	Capability   string                 `json:"capability"`            // 能力，如 vision / sensitive_filter
	Route        string                 `json:"route"`                 // native / proxy / error
	ViaOptions   []ViaOption            `json:"via_options,omitempty"` // proxy 时的视觉候选，顺序即兜底优先级（vision 用）
	Replacements []SensitiveReplacement `json:"replacements,omitempty"` // proxy 时的敏感词替换规则，顺序即替换顺序（sensitive_filter 用）
}

// ============ 5.12 聚合模型（轮询） ============

// AggregateTarget 聚合模型的单个目标：指定模型名和渠道 ID。
type AggregateTarget struct {
	Model     string `json:"model"`      // 真实模型名
	ChannelID string `json:"channel_id"` // 渠道 ID
}

// AggregateModel 聚合模型：对外暴露一个虚拟模型名，按顺序路由到多个真实模型+渠道。
// Targets 的数组顺序即优先级：从头开始尝试，失败（非 2xx）换下一个。
type AggregateModel struct {
	Name    string            `json:"name"`             // 聚合模型名，如 auto
	Targets []AggregateTarget `json:"targets"`          // 真实模型+渠道列表，顺序即优先级
	Models  []string          `json:"models,omitempty"` // 旧 JSON 兼容字段，迁移后不再写入
}

// Aggregate preserves the previous JSON test fixture shape while callers move
// to explicit model-plus-channel aggregate targets.
type Aggregate = AggregateModel

// ============ 5.6 上游 MCP 服务器 ============

// 上游 MCP 传输方式。
const (
	TransportStdio = "stdio" // 本地命令（npx / uvx / 本地可执行文件）
	TransportHTTP  = "http"  // streamable HTTP
	TransportSSE   = "sse"   // SSE（2024-11-05 规范的 HTTP+SSE 传输）
)

// MCPServer 一个上游 MCP 服务器。
type MCPServer struct {
	ID          string            `json:"id"`                    // 唯一 ID
	Name        string            `json:"name"`                  // 名称（决定 /mcp/{name} 端点）
	Description string            `json:"description,omitempty"` // 描述（status 第一级目录的分类描述）
	Transport   string            `json:"transport"`             // stdio / http / sse
	Command     string            `json:"command"`               // stdio：可执行文件
	Args        []string          `json:"args"`                  // stdio：参数
	Env         map[string]string `json:"env,omitempty"`         // stdio：附加环境变量
	URL         string            `json:"url"`                   // http / sse：服务地址
	Headers     map[string]string `json:"headers,omitempty"`     // http / sse：附加请求头
	Enabled     bool              `json:"enabled"`               // MCP 级开关
}

// ============ 5.7 单工具开关与分类 ============

// ToolState 单个工具的开关与分类。未记录的默认启用，分类默认 = 来源 MCP 名。
type ToolState struct {
	ServerID string   `json:"server_id"` // 所属上游 MCP 的 ID
	ToolName string   `json:"tool_name"` // 工具名（未加前缀的原始名）
	Enabled  bool     `json:"enabled"`   // 工具级开关
	Category string   `json:"category"`  // status 索引使用的分类
	Tags     []string `json:"tags"`      // 可选附加标签
}

// ============ 5.8 分组 ============

// GroupTool 分组里勾选的一个工具。
type GroupTool struct {
	ServerID string `json:"server_id"` // 上游 MCP 的 ID
	ToolName string `json:"tool_name"` // 工具名
}

// Group 一个工具分组（手动勾选，无自动规则）。
type Group struct {
	Name  string      `json:"name"`  // 分组名（决定 /mcp/{分组名} 端点）
	Tools []GroupTool `json:"tools"` // 勾选的工具
}

// ============ 5.9 技能仓库清单 ============

// Skill 一个已安装技能。
type Skill struct {
	Name        string `json:"name"`                   // 技能名（目录名；SKILL.md frontmatter 的 name 优先）
	Description string `json:"description,omitempty"`  // 从 SKILL.md frontmatter 解析的描述
	Source      string `json:"source,omitempty"`       // 来源，如 vercel-labs/agent-skills（登记过才有）
	InstalledAt string `json:"installed_at,omitempty"` // 安装时间（RFC3339，登记过才有）
	Version     string `json:"version,omitempty"`      // 版本/分支，如 main（登记过才有）
	UpdatedAt   string `json:"updated_at,omitempty"`   // 上次更新时间（RFC3339，来自 .skill-lock.json）
}

// ============ 5.10 技能预设 ============

// Preset 技能预设（名称 + 技能清单 + 目标平台）。
type Preset struct {
	Name    string   `json:"name"`              // 预设名，如「编程向」
	Skills  []string `json:"skills"`            // 技能名清单
	Target  string   `json:"target,omitempty"`  // 兼容旧字段：单目标平台（空=通用 ~/.agents/skills）；新数据用 Targets
	Targets []string `json:"targets,omitempty"` // 目标平台列表（空=通用）；可同时部署到多个平台
}

// TargetList 返回生效的目标平台列表：优先 Targets，回退旧的 Target，
// 全空视为通用（[""]）。读旧数据（只有 target）时自动兼容。
func (p Preset) TargetList() []string {
	if len(p.Targets) > 0 {
		return p.Targets
	}
	if p.Target != "" {
		return []string{p.Target}
	}
	return []string{""}
}

// ============ 5.11 运行时设置 ============

// Settings 运行时设置。
type Settings struct {
	ActivePreset        string   `json:"active_preset"`                   // 当前生效的预设名
	ActivePresetTarget  string   `json:"active_preset_target"`            // 兼容旧字段：当前预设的目标平台（空=通用，多平台时逗号连接）
	ActivePresetTargets []string `json:"active_preset_targets,omitempty"` // 当前预设的目标平台列表（空=通用）
	DefaultModel        string   `json:"default_model"`                   // 默认模型
}

// ============ 5.12 模型健康状态（aggregate 插件） ============

// ModelHealth 模型健康状态（用于 aggregate 插件的故障恢复）
type ModelHealth struct {
	Model         string  `json:"model"`          // "{model}@{channel_id}"
	Status        string  `json:"status"`         // "available" | "disabled" | "cooling"
	DisabledUntil *string `json:"disabled_until"` // 冷却结束时间（RFC3339，nil = 永久禁用或可用）
	FailCount     int     `json:"fail_count"`     // 连续失败次数
	LastError     string  `json:"last_error"`     // 最后一次错误信息
	LastChecked   string  `json:"last_checked"`   // 最后检查时间（RFC3339）
}

// FailureStrategy 失败处理策略
type FailureStrategy struct {
	Action       string `json:"action"`        // "disable" | "cooldown" | "skip"
	CooldownTime int    `json:"cooldown_time"` // 冷却时长（分钟，action=cooldown 时有效）
	Reason       string `json:"reason"`        // 策略原因
}
