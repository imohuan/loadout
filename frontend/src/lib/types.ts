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
  /** 点分层级编号：主请求=1，子步骤=1.1、1.2（视觉识别/续流） */
  step_no: string
  action: string
  result: string
  model: string
  channel_id?: string
  /** 候选 Key 列表（聚合目标 Key 多选模式） */
  channel_ids?: string[]
  /** 渠道级 base_url（聚合目标渠道级模式，按 base_url 组轮询 Key） */
  channel_base_url?: string
  /** 渠道名称快照（Key 被删除后仍可兜底显示渠道名） */
  channel_name?: string
  started_at: string
  duration_ms?: number
  failure_class?: string
  error_message?: string
  /** 上游原始错误响应体（截断 8KB），与 error_message 互补。
   *  列表折叠面板里单独展示：error_message 一行摘要 + error_body 完整 JSON。 */
  error_body?: string
  stream?: boolean
  prompt_tokens?: number
  completion_tokens?: number
  cached_tokens?: number
  /** 收到上游响应头的时刻（流式 TTFB），前端据此算"等待响应 Xs" */
  first_byte_at?: string
  finished_at?: string
  /** 结构化扩展信息（如视觉识别的 called_via_tool/tool/image_id/prompt），
   *  called_via_tool=true 时 UI 渲染 MCP-{tool} 标签 */
  metadata?: Record<string, unknown>
  /** 本次 attempt 的独立 request-log 主键（命中 request_log 能力路由才有；空则无日志入口） */
  request_log_id?: string
}

/** 模型测试页挂在日志上的配置快照：加载记录时一键回填表单。
 *  仅存可 JSON 序列化字段（持久化进 localStorage），附件不落快照。
 */
export interface TestLogMeta {
  suffix_mode?: string
  channel_id?: string
  base_url?: string
  api_key?: string
  sk_key_hash?: string
  model?: string
  messages?: { role: string; content: string }[]
  /** 右侧输入区文本（draft） */
  draft?: string
  attachments?: { name: string; kind: "image" | "file" }[]
}

export interface RouteLog {
  request_id: string
  /** 模型测试页的配置快照（本地生成并持久化，后端 detail 覆盖时需手动保留） */
  meta?: TestLogMeta
  /** request-log 插件关联主键（独立库 request_logs.id，UUID）；非空时前端显示"完整日志"入口 */
  request_log_id?: string
  requested_model: string
  final_model?: string
  final_channel_id?: string
  /** 最终目标候选 Key 列表（聚合目标 Key 多选模式） */
  final_channel_ids?: string[]
  /** 最终目标渠道级 base_url（聚合目标渠道级模式） */
  final_channel_base_url?: string
  /** 最终渠道名称快照（Key 被删除后仍可兜底显示渠道名） */
  final_channel_name?: string
  sk_key_name?: string
  started_at: string
  result: string
  http_status?: number
  duration_ms?: number
  error_message?: string
  /** 最后一次渠道尝试的上游原始错误响应体（截断 8KB），
   *  list 视图无需展开 details 就能直接拿到完整 JSON 错误。 */
  error_body?: string
  stream?: boolean
  prompt_tokens?: number
  completion_tokens?: number
  cached_tokens?: number
  attempts?: RouteAttempt[]
}

/** 转发日志分页结果：items 为当前页记录，total 为满足过滤条件的全量条数（后端 COUNT）。 */
export interface RouteLogPage {
  items: RouteLog[]
  total: number
}

/** request-log 插件：完整请求日志列表行（不含 request_json/response_json） */
export interface RequestLogItem {
  id: string
  request_id: string
  model: string
  channel: string
  http_status?: number
  stream: boolean
  started_at: string
  finished_at?: string
  duration_ms?: number
  result: string
}

export interface RequestLogPage {
  items: RequestLogItem[]
  total: number
}

/** request-log 插件：详情（含完整 request/response JSON） */
export interface RequestLogDetail extends RequestLogItem {
  request_json: unknown
  response_json?: unknown
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

export interface DepStatus {
  name: string // 库名（unifyai / skills）
  installed: boolean // 是否已全局安装
  current: string // 当前已装版本（未装时为空）
  latest: string // 最新版本
  needUpdate: boolean // 已装且需要更新
  error?: string // 检查错误信息
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

// 消息注入配置（message_inject 用）：往请求 messages 注入自定义内容。
export interface MessageInjection {
  role: string // 注入消息的 role：system / user / assistant
  content: string // 注入的文本内容
  position: string // 注入位置：prepend / append / prepend_first / append_first
}

// 字段过滤规则（field_filter 用）：请求/响应方向的体字段、头字段剔除与保留。
// 字段路径支持顶层 key 与点路径嵌套（如 a.b.c）；Keep 非空走白名单（只保留，忽略同方向 Strip）。
// 请求/响应头按 HTTP 标准大小写不敏感匹配。
export interface FieldRules {
  request_strip?: string[] // 请求体剔除的字段路径
  request_keep?: string[] // 请求体白名单（顶层 key）
  request_header_strip?: string[] // 请求头剔除（替代 proxy.go 写死的 stripAltAuth）
  response_strip?: string[] // 非流式响应体剔除的字段路径
  response_keep?: string[] // 非流式响应体白名单（顶层 key）
  response_header_strip?: string[] // 响应头剔除
}

export interface CapabilityRoute {
  models: string[] // 目标模型列表，支持 * 通配与 prefix* 前缀匹配
  channel_ids?: string[] // 目标模型绑定的渠道 Key 列表（多选）；空 = 全渠道；含 "*" = 通用全匹配（任何渠道生效）
  channel_base_urls?: string[] // 渠道级（base_url 组）：新增 Key 仍命中，与 channel_ids 并存
  capability: string // 能力，如 vision / sensitive_filter / field_filter
  route: 'native' | 'proxy' | string // 路由方式：原生透传 / 附加代理（历史 'error' 数据降级为透传）
  via_options?: ViaOption[] // proxy 时的候选，顺序即兜底优先级（vision 用）
  replacements?: SensitiveReplacement[] // proxy 时的敏感词替换规则，顺序即替换顺序（sensitive_filter 用）
  field_rules?: FieldRules // 字段过滤规则（field_filter 用；nil/undefined = 未配置，原样透传）
  injections?: MessageInjection[] // 消息注入配置，顺序即注入顺序（message_inject 用）
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

// ---- MCP 工具调用日志（mcp_invocations 明细） ----
export interface McpInvocation {
  id: number
  started_at: string
  finished_at: string | null
  aggregate_kind: 'single' | 'group' | '$smart' | ''
  aggregate_target: string | null
  tool_name: string
  server_name: string
  result: 'success' | 'error' | 'not_found' | 'timeout' | 'denied' | ''
  http_status: number | null
  duration_ms: number
  error_message: string
  input_json: string
  output_json: string
  auth_kind: 'session' | 'mcp-key' | 'public' | ''
}
export interface McpInvocationPage {
  items: McpInvocation[]
  total: number
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

// ---- 卡片视图：按 model 聚合（后台 /api/volc-quota/aggregate，v19） ----
export interface VolcQuotaAggregate {
  model: string
  name?: string
  unit?: string
  initial_total: number // SUM(initial_total)
  local_remaining: number // SUM(local_remaining)
  used_amount: number // SUM(used_amount)；本地口径下 = initial_total - local_remaining
  total_amount: number
  percentage: number // 0~100 本地口径
  exhausted: boolean
}
export interface VolcQuotaAggregateDetails {
  config: VolcQuotaConfig
  aggregates?: VolcQuotaAggregate[]
}
export interface VolcQuotaAggregateResponse {
  configs: VolcQuotaAggregateDetails[]
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

// 全局进程（procreg 统一命令执行器）类型
export type ProcStatus = 'running' | 'done' | 'error'

export interface ProcessInfo {
  id: string
  name: string
  kind: string
  cmd: string
  pid: number
  status: ProcStatus
  startedAt: string
  endedAt?: string
  exitCode?: number
  memBytes: number
  log: string[]
}

export interface ProcessEvent {
  type: 'snapshot' | 'update'
  data: ProcessInfo[]
}
