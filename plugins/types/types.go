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

// MetadataVirtualModel pipe.Metadata 中"聚合（虚拟）模型名"的键。
// 聚合插件在 ProxyBeforeUpstream 把请求 model 改写为真实目标模型名时，将原虚拟名
// 写入此键保留；能力插件（message-inject/sensitive/field/vision 等）在改写后的
// ProxyBeforeAttempt 做路由选择时，用 MatchModelsEx 让虚拟前缀路由（如 `git-*`）命中。
const MetadataVirtualModel = "__virtual_model"

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
	Cipher    string   `json:"cipher,omitempty"` // 完整 key 的 AES 密文（仅服务端测试代理解密用，列表接口不返回）
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
	Name          string               `json:"name"`                    // Key 名称（同一 Base URL 下多个 Key 各有名字）
	ChannelName   string               `json:"channel_name,omitempty"`  // 渠道名称（Base URL 组级名称，同组 Key 同步一致）
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
	// RouteError 已废弃：历史 route="error" 数据运行时按 native（透传）降级处理，
	// 见 SelectCapabilityRoutes 注释。常量删除后不再有新数据写入 error。
)

// SelectCapabilityRoutes 按能力路由表选择命中的路由（所有能力插件统一入口：
// vision / sensitive-filter / field-filter / request-log 共用，避免各插件选择策略分叉）：
//
//   - 模型命中 MatchModels 且渠道命中 MatchChannelScopeEx 才算匹配；
//   - 非 proxy 路由（native，及历史数据 error）命中即短路返回该项——豁免/降级语义优先，
//     不依赖路由表 position 排序（历史 error 自动按 native 透传处理，即「不支持就不管他」）；
//     注意：短路会屏蔽同 model+channel 的其它 proxy 替换规则（与旧 native/error 优先语义一致，
//     若需替换与豁免并存，豁免行须精确限定 model/渠道，避免宽通配误伤）；
//   - proxy 路由收集全部匹配项（叠加规则，如字段过滤多条规则合并），返回数组。
//
// 返回 nil = 无匹配（调用方按透传处理）。读表/解析失败由调用方自行 fail-open。
//
// 本函数按真实模型名匹配（虚拟模型名传空）；需要按虚拟模型（聚合模型）名匹配的调用方
// 请用 SelectCapabilityRoutesEx（见下），避免各插件选择策略分叉。
func SelectCapabilityRoutes(routes []CapabilityRoute, capability, model string, scope ChannelRequestScope) []*CapabilityRoute {
	return SelectCapabilityRoutesEx(routes, capability, model, "", scope)
}

// SelectCapabilityRoutesEx 同 SelectCapabilityRoutes，但额外支持按虚拟模型名匹配。
// virtualModel 为请求的聚合（虚拟）模型名（pipe.Metadata["__virtual_model"]，可能为空）。
// 模型命中判定用 MatchModelsEx：真实模型名或虚拟模型名任一命中配置列表即匹配。
// 这样路由表里用虚拟模型前缀（如 `git-*`）配置的能力，能命中经过聚合改写后的请求
// （聚合会在 ProxyBeforeUpstream 把真实模型名写回 Request.Model，同时保留虚拟名在
// Metadata），避免「虚拟名只在入口可见、能力安检在改写后执行」导致的路由失配。
// 其余语义（native 短路 / proxy 叠加 / 渠道约束）与 SelectCapabilityRoutes 完全一致。
func SelectCapabilityRoutesEx(routes []CapabilityRoute, capability, model, virtualModel string, scope ChannelRequestScope) []*CapabilityRoute {
	var matched []*CapabilityRoute
	for i := range routes {
		if routes[i].Capability != capability ||
			!MatchModelsEx(routes[i].Models, model, virtualModel) ||
			!MatchChannelScopeEx(routes[i].ChannelIDs, routes[i].ChannelBaseURLs, scope) {
			continue
		}
		// 非 proxy（native / 历史 error）：短路返回，豁免优先于叠加代理
		if routes[i].Route != RouteProxy {
			return []*CapabilityRoute{&routes[i]}
		}
		matched = append(matched, &routes[i])
	}
	return matched
}

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

// MatchModelsEx 判断真实模型名 model 或虚拟模型名 virtualModel 是否命中目标模型列表。
// 虚拟模型名（聚合模型名）为空时退化为一元匹配（等价 MatchModels(model)）。
// 场景：聚合模型请求在入口被改写为真实模型名，虚拟名保留在 Metadata——能力路由用
// 虚拟名（如 `git-*`）配置时，靠本函数让改写过后的请求仍能命中。
func MatchModelsEx(models []string, model, virtualModel string) bool {
	if MatchModels(models, model) {
		return true
	}
	if virtualModel != "" {
		return MatchModels(models, virtualModel)
	}
	return false
}

// VirtualModelFromMetadata 从 pipe.Metadata 读取聚合（虚拟）模型名；未设置或非字符串返回空串。
// 各能力插件统一用它取虚拟名，避免重复的裸字符串索引与类型断言。metadata 为 nil 时安全返回空串。
func VirtualModelFromMetadata(md map[string]any) string {
	if md == nil {
		return ""
	}
	v, _ := md[MetadataVirtualModel].(string)
	return v
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

// ChannelBaseURLMatches 判断请求渠道 base_url 是否命中渠道级列表（归一化比较）。
// 调用方负责传入请求方查到的 base_url（如在 vision/sensitive-filter DecideRoute 里先查询渠道
// 元数据）；返回 true 即视为该路由在渠道级粒度上对当前请求生效。
func ChannelBaseURLMatches(channelBaseURLs []string, requestBaseURL string) bool {
	if len(channelBaseURLs) == 0 {
		return false
	}
	if requestBaseURL == "" {
		return false
	}
	target := normalizeBaseURL(requestBaseURL)
	for _, bu := range channelBaseURLs {
		if normalizeBaseURL(bu) == target {
			return true
		}
	}
	return false
}

// ChannelRequestScope 一次请求实际落到的渠道上下文（从 pipe.Metadata 解析）。
//   - 普通单 key 请求：IDs=[__current_channel]，BaseURLs 由调用方按 id 查渠道表补 base_url；
//   - 聚合模型渠道级 / Key 多选：IDs=__channel_candidates（候选 key id 列表），
//     BaseURLs 含 __current_channel_base_url（渠道组地址，可能为空）。
//
// 注意：聚合模型对渠道级/Key 多选目标会写 __current_channel=""（由 proxyForward 对 candidates
// 逐个 failover），所以能力插件（vision/sensitive-filter）绝不能只看 __current_channel 一个字段，
// 否则聚合流量永远匹配不到渠道约束路由。
type ChannelRequestScope struct {
	IDs      []string // 实际渠道 key id 集合（空 = 渠道未知且无候选）
	BaseURLs []string // 实际渠道组 base_url（归一化比较，空 = 未知）
}

// ChannelScopeFromMetadata 从 pipe.Metadata 解析请求渠道上下文（vision / sensitive-filter 共用）：
//   - __current_channel（单 key，聚合单 Key 目标或普通请求写入）
//   - __channel_candidates（聚合渠道级/Key 多选目标写入的候选 Key 集合）
//   - __current_channel_base_url（聚合渠道级目标写入的组地址）
//   - __channel_hint（v2 前缀渠道名；仅当上述字段全空时兜底反查渠道组 base_urls）
//
// resolveBaseURLs 非 nil 时按 term 反查渠道表 base_url 列表（渠道级匹配需要，调用方传自己的查询闭包；
// term 可为渠道 key id 或渠道名 ChannelName，返回匹配渠道组的全部 base_urls，去重）。
// 注意：聚合模型对渠道级/Key 多选目标写 __current_channel=""，绝不能只看这一个字段。
func ChannelScopeFromMetadata(md map[string]any, resolveBaseURLs func(string) []string) ChannelRequestScope {
	scope := ChannelRequestScope{}
	if md == nil {
		return scope
	}
	appendBaseURLs := func(term string) {
		if resolveBaseURLs == nil {
			return
		}
		for _, bu := range resolveBaseURLs(term) {
			if bu != "" && !containsString(scope.BaseURLs, bu) {
				scope.BaseURLs = append(scope.BaseURLs, bu)
			}
		}
	}
	channelID, _ := md["__current_channel"].(string)
	if channelID != "" {
		scope.IDs = append(scope.IDs, channelID)
		appendBaseURLs(channelID)
	}
	if candidates, ok := md["__channel_candidates"].([]string); ok {
		for _, c := range candidates {
			if c == "" {
				continue
			}
			scope.IDs = append(scope.IDs, c)
			// 关键：候选 key 也要反查 base_url 进 scope.BaseURLs——
			// 否则路由是渠道级约束（channel_base_urls）而请求只有候选 key id 时匹配不到。
			appendBaseURLs(c)
		}
	}
	if bu, ok := md["__current_channel_base_url"].(string); ok && bu != "" {
		if !containsString(scope.BaseURLs, bu) {
			scope.BaseURLs = append(scope.BaseURLs, bu)
		}
	}
	// 兜底：入口阶段（ProxyBeforeUpstream）渠道 id/base_url 尚未写入，
	// 但 v2 前缀已把渠道名写进 __channel_hint。按 hint 反查渠道组 base_urls，
	// 让渠道级约束（channel_base_urls）路由在入口阶段也能命中——否则
	// channel_base_urls 非空的路由永远匹配不到，被全渠道兜底路由抢走。
	if len(scope.IDs) == 0 && len(scope.BaseURLs) == 0 {
		if hint, ok := md["__channel_hint"].(string); ok && hint != "" {
			appendBaseURLs(hint)
		}
	}
	return scope
}

// containsString 判断 slice 是否含指定元素（ChannelScopeFromMetadata 去重用）。
func containsString(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// MatchChannelScopeEx 判断请求渠道上下文是否命中路由的渠道约束。
// channelIDs / channelBaseURLs 为路由约束（Key 级 / 渠道级）；req 为请求实际落到的渠道集合。
// 语义：
//   - 路由约束都为空，或 channelIDs 含 `*`：全渠道（任何渠道命中，含未知渠道）；
//   - 否则请求上下文为空（渠道未知且无候选）不命中；请求任一 key id 命中 channelIDs，或
//     请求任一 base_url 命中 channelBaseURLs（归一化比较）即命中。
func MatchChannelScopeEx(channelIDs []string, channelBaseURLs []string, req ChannelRequestScope) bool {
	if len(channelIDs) == 0 && len(channelBaseURLs) == 0 {
		return true
	}
	for _, id := range channelIDs {
		if id == "*" {
			return true
		}
	}
	if len(req.IDs) == 0 && len(req.BaseURLs) == 0 {
		return false
	}
	for _, rid := range req.IDs {
		for _, id := range channelIDs {
			if rid == id {
				return true
			}
		}
	}
	for _, rb := range req.BaseURLs {
		for _, bu := range channelBaseURLs {
			if normalizeBaseURL(rb) == normalizeBaseURL(bu) {
				return true
			}
		}
	}
	return false
}

// MatchChannelScope 判断请求渠道是否命中路由的渠道作用域约束（单 key / 单 base_url 形态）。
// 兼容旧调用：等价于 MatchChannelScopeEx(channelIDs, channelBaseURLs,
// {IDs:[channelID], BaseURLs:[requestBaseURL]}（空值自动忽略））。
//
// 这是能力路由命中渠道的兼容入口：vision / sensitive-filter / model-gateway 都应优先改用
// MatchChannelScopeEx（支持聚合模型的 __channel_candidates），避免「纯渠道级路由（channel_ids 空）
// 被 MatchChannel 误判为全渠道」的漏洞。
func MatchChannelScope(channelIDs []string, channelBaseURLs []string, channelID string, requestBaseURL string) bool {
	req := ChannelRequestScope{}
	if channelID != "" {
		req.IDs = []string{channelID}
	}
	if requestBaseURL != "" {
		req.BaseURLs = []string{requestBaseURL}
	}
	return MatchChannelScopeEx(channelIDs, channelBaseURLs, req)
}

// normalizeBaseURL 去掉尾斜杠，供 ChannelBaseURLMatches / MatchChannelScope 比较时使用。
// 与 frontend/src/composables/useChannels.ts `normalizeBaseURL` 语义一致。
func normalizeBaseURL(url string) string {
	return strings.TrimRight(url, "/")
}

// ViaOption 视觉兜底候选：视觉模型 + 可选渠道，按数组顺序从上到下依次请求（failover）。
// 渠道粒度：ChannelBaseURL（渠道级，按 base_url 组轮询 Key）> ChannelIDs（Key 多选）> ChannelID（兼容单 Key）。
type ViaOption struct {
	ViaModel       string   `json:"via_model"`            // 视觉模型名
	ChannelID      string   `json:"channel_id,omitempty"` // 渠道 id；空 = 按 via_model 自动路由（走 /v1/models 探测兜底）
	ChannelIDs     []string `json:"channel_ids,omitempty"` // 渠道 id 列表（Key 多选）
	ChannelBaseURL string   `json:"channel_base_url,omitempty"` // 渠道地址（渠道级）
}

// SensitiveReplacement 敏感词替换规则：from → to。
// Regex=true 时 from 按正则匹配，to 支持 $1 等捕获组引用（Go regexp 语义）。
type SensitiveReplacement struct {
	From  string `json:"from"`            // 原始内容/敏感词（或正则表达式）
	To    string `json:"to"`              // 替换后的内容
	Regex bool   `json:"regex,omitempty"` // true = from 按正则匹配
}

// 消息注入位置常量（message_inject 能力用）。
const (
	// InjectPrepend 新增一条消息作为 messages 数组的第一项。
	InjectPrepend = "prepend"
	// InjectAppend 新增一条消息作为 messages 数组的最后一项。
	InjectAppend = "append"
	// InjectPrependFirst 把内容追加到「原始第一条」消息 content 的开头。
	InjectPrependFirst = "prepend_first"
	// InjectAppendFirst 把内容追加到「原始第一条」消息 content 的结尾。
	InjectAppendFirst = "append_first"
)

// MessageInjection 消息注入配置（message_inject 能力用）：往请求 messages 注入自定义内容。
// 一条配置 = 一段内容 + 注入位置；多条按配置顺序依次应用。
type MessageInjection struct {
	Role     string `json:"role"`               // 注入消息的 role：system / user / assistant
	Content  string `json:"content"`            // 注入的文本内容
	Position string `json:"position,omitempty"` // 注入位置：prepend / append / prepend_first / append_first
}

// FieldRules 字段过滤规则（field_filter 能力用；nil = 未配置，原样透传）。
// 字段路径支持顶层 key 与点路径嵌套（如 a.b.c）；Keep 非空时走白名单
// （只保留，忽略同方向 Strip）；均无命中时原字节透传。
// 请求/响应头按 CanonicalHeaderKey 大小写不敏感剔除。
type FieldRules struct {
	RequestStrip         []string `json:"request_strip,omitempty"`          // 请求体剔除的字段路径
	RequestKeep          []string `json:"request_keep,omitempty"`           // 请求体白名单：只保留这些字段（顶层）
	RequestHeaderStrip   []string `json:"request_header_strip,omitempty"`   // 请求头剔除（大小写不敏感；替代 proxy.go 写死的 stripAltAuth）
	ResponseStrip        []string `json:"response_strip,omitempty"`         // 非流式响应体剔除的字段路径
	ResponseKeep         []string `json:"response_keep,omitempty"`          // 非流式响应体白名单（顶层）
	ResponseHeaderStrip  []string `json:"response_header_strip,omitempty"`  // 响应头剔除（大小写不敏感）
}

// CapabilityRoute 能力路由表条目：目标模型（可多个/通配符）× 渠道 × 能力 矩阵。
type CapabilityRoute struct {
	Models          []string               `json:"models"`                       // 目标模型列表，支持 `*` 通配与 `prefix*` 前缀匹配
	ChannelIDs      []string               `json:"channel_ids,omitempty"`        // 目标模型绑定的渠道 Key 列表（Key 级）；空 = 全渠道生效
	ChannelBaseURLs []string               `json:"channel_base_urls,omitempty"`  // 渠道级：按 base_url 绑定的渠道组（保存原意图，用于渠道级展示 + 新增 Key 仍命中）
	Capability      string                 `json:"capability"`                   // 能力，如 vision / sensitive_filter
	Route           string                 `json:"route"`                        // native / proxy / error
	ViaOptions      []ViaOption            `json:"via_options,omitempty"`        // proxy 时的视觉候选，顺序即兜底优先级（vision 用）
	Replacements    []SensitiveReplacement `json:"replacements,omitempty"`       // proxy 时的敏感词替换规则，顺序即替换顺序（sensitive_filter 用）
	FieldRules      *FieldRules            `json:"field_rules,omitempty"`        // 字段过滤规则（field_filter 用；nil=未配置）
	Injections      []MessageInjection     `json:"injections,omitempty"`         // 消息注入配置，顺序即注入顺序（message_inject 用）
}

// ============ 5.12 聚合模型（轮询） ============

// AggregateTarget 聚合模型的单个目标：指定模型名和渠道 ID。
// 三种粒度（优先级从高到低）：ChannelBaseURL（渠道级，按 base_url 组轮询 Key）>
// ChannelIDs（Key 多选）> ChannelID（兼容单 Key）。
type AggregateTarget struct {
	Model          string   `json:"model"`                      // 真实模型名
	ChannelID      string   `json:"channel_id"`                 // 渠道 ID（单 Key，兼容）
	ChannelIDs     []string `json:"channel_ids,omitempty"`      // 渠道 ID 列表（Key 多选）
	ChannelBaseURL string   `json:"channel_base_url,omitempty"` // 渠道地址（渠道级，按 base_url 组轮询 Key）
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
	Path        string `json:"path,omitempty"`         // 技能目录的绝对路径（~/.loadout/skills/<name>）
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
	UseGlobalCmd        bool     `json:"use_global_cmd"`                  // 依赖执行优先用全局指令（true），否则用 npx（false）
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
