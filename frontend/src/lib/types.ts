export interface Overview {
  app: string
  version: string
  plugins: number
  channels: number
  active_preset?: string
  active_preset_target?: string
  active_preset_targets?: string[]
}

export interface ChannelModelDetail {
  model: string
  source: string // probe（探测）/ manual（手动配置）
  enabled: boolean
}

export interface Channel {
  id: string
  name: string
  channel_name?: string
  base_url: string
  enabled?: boolean
  manual_enabled?: boolean
  sync_billing?: boolean
  models?: string[]
  models_detail?: ChannelModelDetail[]
  models_error?: string
}

export interface AggregateTarget {
  model: string
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

export interface Aggregate {
  name: string
  enabled?: boolean
  targets: AggregateTarget[]
}

export interface ModelStatus {
  model: string
  manual_enabled: boolean
  health_status: 'available' | 'cooling' | 'disabled' | string
  effective_available: boolean
  reason?: string
  last_error?: string
  fail_count?: number
  last_success_at?: string
  disabled_until?: string
  source?: string
}

export interface ChannelStatus {
  channel: Channel
  manual_enabled: boolean
  health_status: 'available' | 'cooling' | 'disabled' | string
  effective_available: boolean
  reason?: string
  models: ModelStatus[]
}

export interface RouteAttempt {
  id?: number
  step_no: number
  action: string
  result: string
  model: string
  channel_id?: string
  /** 候选 Key 列表（聚合目标 Key 多选模式） */
  channel_ids?: string[]
  /** 渠道级 base_url（聚合目标渠道级模式，按 base_url 组轮询 Key） */
  channel_base_url?: string
  started_at: string
  duration_ms?: number
  failure_class?: string
  error_message?: string
  stream?: boolean
  prompt_tokens?: number
  completion_tokens?: number
  cached_tokens?: number
}

export interface RouteLog {
  request_id: string
  requested_model: string
  final_model?: string
  final_channel_id?: string
  /** 最终目标候选 Key 列表（聚合目标 Key 多选模式） */
  final_channel_ids?: string[]
  /** 最终目标渠道级 base_url（聚合目标渠道级模式） */
  final_channel_base_url?: string
  sk_key_name?: string
  started_at: string
  result: string
  http_status?: number
  duration_ms?: number
  error_message?: string
  stream?: boolean
  prompt_tokens?: number
  completion_tokens?: number
  cached_tokens?: number
  attempts?: RouteAttempt[]
}

export interface Skill {
  name: string
  description?: string
  source?: string
  version?: string
  updated_at?: string // 上次更新时间（RFC3339，来自 .skill-lock.json）
}

export interface Preset {
  name: string
  skills: string[]
  target?: string // 兼容旧数据：目标平台（空=通用 (.agents)；codex/claudecode/opencode）
  targets?: string[] // 目标平台列表（空=通用），可同时部署多个平台
}

// 单个目标（通用或平台）的技能数量与备份状态。
export interface SkillPlatformStatus {
  name: string // 平台名：""=通用
  dir: string // 技能目录
  count: number // 一级技能目录数
  has_backup: boolean // 是否存在 <dir>-backup
}

export interface ApiKey {
  id: string
  name: string
  prefix?: string
  models?: string[]
  endpoint?: string
  header_name?: string
}

// 模型+渠道组合：通用的「模型 + 渠道」有序列表项。
// 三种粒度（优先级从高到低）：channel_base_url（渠道级）> channel_ids（Key 多选）> channel_id（兼容单 Key）。
export interface ModelChannelItem {
  model: string // 模型名
  channel_id?: string // 渠道 id；空 = 自动路由（仅允许自动路由时有效）
  channel_ids?: string[] // 渠道 id 列表（Key 多选）
  channel_base_url?: string // 渠道地址（渠道级）
}

// 能力路由：给不支持某能力的模型附加能力（如 vision 视觉）。
export interface ViaOption {
  via_model: string // 视觉模型名
  channel_id?: string // 渠道 id；空 = 按 via_model 自动路由
  channel_ids?: string[] // 渠道 id 列表（Key 多选）
  channel_base_url?: string // 渠道地址（渠道级，按 base_url 组轮询 Key）
}

// 敏感词替换规则：from → to；regex=true 时 from 按正则匹配。
export interface SensitiveReplacement {
  from: string // 原始内容/敏感词（或正则表达式）
  to: string // 替换后的内容
  regex?: boolean // true = from 按正则匹配
}

export interface CapabilityRoute {
  models: string[] // 目标模型列表，支持 * 通配与 prefix* 前缀匹配
  channel_ids?: string[] // 目标模型绑定的渠道 Key 列表（多选）；空 = 全渠道；含 "*" = 通用全匹配（任何渠道生效）
  channel_base_urls?: string[] // 渠道级（base_url 组）：新增 Key 仍命中，与 channel_ids 并存
  capability: string // 能力，如 vision / sensitive_filter
  route: 'native' | 'proxy' | 'error' | string // 路由方式：原生透传 / 附加代理 / 拒绝
  via_options?: ViaOption[] // proxy 时的候选，顺序即兜底优先级（vision 用）
  replacements?: SensitiveReplacement[] // proxy 时的敏感词替换规则，顺序即替换顺序（sensitive_filter 用）
}

// ---- MCP 调用统计 ----
export interface McpTrendPoint {
  date: string
  count: number
}
export interface McpAggregateRank {
  kind: 'single' | 'group' | '$smart'
  target: string | null
  calls: number
}
export interface McpToolRank {
  tool_name: string
  server_name: string
  calls: number
}
export interface McpStats {
  trend: McpTrendPoint[]
  rank_aggregates: McpAggregateRank[]
  rank_tools: McpToolRank[]
}

// ---- 模型使用情况 ----
export interface ModelSummary {
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
  success_rate: number
  avg_duration_ms: number
  failed: number
}
export interface ModelHitRate {
  input: number
  output: number
  total: number
}
export interface ModelTrendPoint {
  date: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  total_tokens: number
}
export interface ModelCalendarPoint {
  date: string
  tokens: number
}
export interface ModelDistPoint {
  model: string
  calls: number
  tokens: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
}
export interface ModelStats {
  summary: ModelSummary
  hit_rate: ModelHitRate
  trend: ModelTrendPoint[]
  calendar: ModelCalendarPoint[]
  model_dist: ModelDistPoint[]
}

// ---- 火山引擎免费额度（volc-free-quota 插件） ----
export interface VolcQuotaConfig {
  channel_id: string
  account_id?: string
  channel_name?: string
  base_url?: string
  key_name?: string
  access_key: string
  secret_key?: string // 列表/读取接口不回显明文
  enabled: boolean
  force_block: boolean // 强制关停：开启后即使 model_states 手动恢复，也按 volc_quota_packages 状态拦截
  last_synced_at?: string
  last_error?: string
  updated_at?: string
}

export interface VolcQuotaUsage {
  account_id: string // 后端 v11 后主键为 (account_id, model)
  model: string
  use_count: number
  last_used_at?: string
}

// v14 资源包逐条明细：同 Product（ark_bd）下几十种模型配置各有额度。
export interface VolcQuotaPackage {
  account_id: string
  instance_no: string
  product?: string
  product_name?: string
  configuration_code?: string
  configuration_name?: string
  model?: string // v15 从 configuration_code 提取的模型名，扣减/拦截锚点
  total_amount: number
  available_amount: number
  used_amount: number
  initial_total: number // v15 本地递减：首次刷新写入的总额
  local_remaining: number // v15 本地递减：每次请求成功后扣减
  unit: string
  status: string // Effective / UsedUp / Expired / ...
  effective_time?: string
  expiry_time?: string
  synced_at?: string
}

export interface VolcQuotaConfigDetails {
  config: VolcQuotaConfig
  usage?: VolcQuotaUsage[]
  packages?: VolcQuotaPackage[]
}

export interface VolcQuotaStatusResponse {
  configs: VolcQuotaConfigDetails[]
}

export interface VolcQuotaRefreshResult {
  refreshed_at: string
  configs_checked: number
  failed_channels?: string[]
  disabled_models?: string[]
}

export interface VolcQuotaRecentUsage {
  channel_id: string
  base_url: string
  minutes: number
  has_recent: boolean
  request_count: number
  last_request_at: string
}
