package volcfreequota

// 火山引擎免费额度插件的核心数据结构。
//
// 内存模型与 DB 表一一对应：Config 对应 volc_quota_config，Model 对应 volc_quota_models，
// Usage 对应 volc_quota_usage。前端 JSON 直接序列化这些类型，避免重复定义。

// Config 一条配置：关联一个火山引擎渠道 Key + 一对 AK/SK。
//
//   - ChannelID：渠道记录主键，对应 https://ark.cn-beijing.volces.com/api/v3 这一组中的某个 Key。
//   - AccountID：AK/SK 所属火山账号的稳定指纹（SHA256(access_key) 前 16 位），
//     由 SaveConfigs 自动计算；同一账号的多个 Key 配置共享同一 AccountID（额度按账号对齐）。
//   - AccessKey / SecretKey：（SecretKey 在落库前会用 store.Encrypt 加密，返回给前端时永远为空）。
//   - Enabled：是否启用该配置（禁用后该通道不再被刷新与查询；前端可勾选停用）。
//   - ForceBlock：强制关停。开启后即使 model_states 被手动恢复，请求也会直接按
//     volc_quota_models.status='exhausted' 拦截（不依赖 model_states 冷却机制）。
type Config struct {
	ChannelID    string `json:"channel_id"`
	AccountID    string `json:"account_id"`
	ChannelName  string `json:"channel_name,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	KeyName      string `json:"key_name,omitempty"`
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key,omitempty"` // 列表/读取接口不返回明文
	Enabled      bool   `json:"enabled"`
	ForceBlock   bool   `json:"force_block"`
	LastSyncedAt string `json:"last_synced_at,omitempty"`
	LastError    string `json:"last_error,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// Model 一个免费模型在某个账号（AccountID）下的配额快照。
//
//   - AccountID：账号指纹（SHA256(access_key) 前 16 位）。同一账号的多个 Key 共享同一账号额度。
//   - Model 为归一化的标识（资源包 product 名称去前后缀，用于模糊匹配真实 API 模型名）。
//   - Total / Available / Used 与资源包字段一一对应；多数免费额度的 Available 字段即为
//     "剩余 Token 数"（unit = "Tokens"），所以 UI 直接展示数字。
//   - InitialTotal / LocalRemaining：本地递减余额。initial_total 来自 billing API 首次写入，
//     local_remaining 每次 API 响应成功后扣减 total_tokens。不依赖 billing API 实时性。
//   - Status: "ok" / "exhausted" / "unknown"。
type Model struct {
	AccountID       string `json:"account_id"`
	Model           string `json:"model"`
	ProductName     string `json:"product_name,omitempty"`
	TotalAmount     int64  `json:"total_amount"`
	AvailableAmount int64  `json:"available_amount"`
	UsedAmount      int64  `json:"used_amount"`
	InitialTotal    int64  `json:"initial_total"`
	LocalRemaining  int64  `json:"local_remaining"`
	Unit            string `json:"unit"`
	Status          string `json:"status"` // ok / exhausted / unknown
	SyncedAt        string `json:"synced_at,omitempty"`
}

// Usage 一条 (account_id, model) 的请求使用记录（仅作展示与匹配辅助）。
type Usage struct {
	AccountID  string `json:"account_id"`
	Model      string `json:"model"`
	UseCount   int64  `json:"use_count"`
	LastUsedAt string `json:"last_used_at,omitempty"`
}

// RefreshResult 单次刷新的聚合结果：成功条数 + 失败条数 + 过期禁用模型数量。
type RefreshResult struct {
	RefreshedAt    string   `json:"refreshed_at"`
	ConfigsChecked int      `json:"configs_checked"`
	FailedChannels []string `json:"failed_channels,omitempty"`
	DisabledModels []string `json:"disabled_models,omitempty"`
}

// ListStatusResponse GET /api/volc-quota/status 返回体：所有配置 + 每条配置下的模型列表 + 使用记录。
type ListStatusResponse struct {
	Configs []ConfigWithDetails `json:"configs"`
}

// ConfigWithDetails 一条配置 + 它关联的渠道信息 + 该渠道下的免费模型列表 + 使用记录。
//
// 用于设置页一次性渲染折叠面板，避免前端分多次请求。
type ConfigWithDetails struct {
	Config   Config    `json:"config"`
	Models   []Model   `json:"models"`
	Usage    []Usage   `json:"usage,omitempty"`
	Packages []Package `json:"packages,omitempty"` // 资源包逐条明细（v14）
}

// SaveConfigRequest PUT /api/volc-quota/config 请求体：批量覆盖配置（enable=false 的条目需省略）。
//
// SecretKey 为空字符串时保留库内既有密文（前端编辑时不回显明文）。
type SaveConfigRequest struct {
	Configs []Config `json:"configs"`
}

// RecentUsageResponse GET /api/volc-quota/recent-usage 返回体：某 base_url 近 N 分钟的请求日志状态。
//
// 用于「刷新远程」前的安全检查：提醒用户该渠道近期仍有模型请求，刷新可能互相干扰。
type RecentUsageResponse struct {
	ChannelID     string `json:"channel_id"`
	BaseURL       string `json:"base_url"`
	Minutes       int    `json:"minutes"`
	HasRecent     bool   `json:"has_recent"`      // 近 N 分钟是否有请求
	RequestCount  int    `json:"request_count"`   // 近 N 分钟请求条数
	LastRequestAt string `json:"last_request_at"` // 最后一条请求时间（RFC3339Nano，空表示无）
}

// Package 一条资源包逐条明细（volc_quota_packages）。
//
// 与 Model 的区别：Model 是按归一化 model 名聚合的额度行（用于本地扣减/拦截），
// Package 保留 billing API 返回的每个资源包原始信息（ConfigurationName/Status/时间），
// 供 UI 像 main.go 输出那样逐条展示"哪个模型还有多少额度"。
type Package struct {
	AccountID         string `json:"account_id"`
	InstanceNo        string `json:"instance_no"`
	Product           string `json:"product,omitempty"`
	ProductName       string `json:"product_name,omitempty"`
	ConfigurationCode string `json:"configuration_code,omitempty"`
	ConfigurationName string `json:"configuration_name,omitempty"`
	TotalAmount       int64  `json:"total_amount"`
	AvailableAmount   int64  `json:"available_amount"`
	UsedAmount        int64  `json:"used_amount"`
	Unit              string `json:"unit"`
	Status            string `json:"status"`
	EffectiveTime     string `json:"effective_time,omitempty"`
	ExpiryTime        string `json:"expiry_time,omitempty"`
	SyncedAt          string `json:"synced_at,omitempty"`
}
