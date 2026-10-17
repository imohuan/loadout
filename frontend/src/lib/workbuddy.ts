import { api, request } from '@/lib/api'

/** 单个资源包来源分组。 */
export interface WbCreditGroup {
  name: string
  packages: number
  remain: number
  size: number
  expiry: { end: string; remain: number }[]
}

/** 模型倍率与能力（来自上游 models 接口）。 */
export interface WbModel {
  id: string
  name: string
  credits: string
  rate: number
  description?: string
  context?: number
  max_output?: number
  supports_images?: boolean
  supports_tool?: boolean
  supports_reasoning?: boolean
  only_reasoning?: boolean
  is_default?: boolean
  reasoning_effort?: string
}

/** 单个「渠道 Key + 模型」的真实健康状态（后端读 model_states 表）。 */
export interface WbModelState {
  model: string
  manual_enabled: boolean
  status: string
  disabled_until?: string
  failure_class?: string
  last_error?: string
  fail_count: number
  last_success_at?: string
  effective_available: boolean
  label: string
}

export interface WbChannelHealth {
  status: string
  disabled_until?: string
  failure_class?: string
  last_error?: string
  effective_available: boolean
}

/** 单账号（= 渠道 Key）面板数据。 */
export interface WbAccount {
  channel_id: string
  label: string
  remain: number
  total: number
  groups: WbCreditGroup[]
  models: WbModel[]
  synced_at: string
  last_error?: string
  usage_synced_at?: string
  channel: WbChannelHealth
  model_states: Record<string, WbModelState>
  spent_today: number
  spent_7d: number
  spent_month: number
  spent_total: number
  requests: number
}

export interface WbPanelData {
  accounts: WbAccount[]
  usage_daily: Record<string, WbUsageDay[]>
  usage_by_model: WbUsageModel[]
  usage_logs: WbUsageLog[]
}

/** 消耗明细一行：source = remote（上游基线）/ local（本地拦截）。 */
export interface WbUsageLog {
  request_id: string
  account_id: string
  account: string
  model: string
  credit: number
  prompt_tokens: number
  completion_tokens: number
  cached_tokens: number
  created_at: string
  source: 'remote' | 'local'
}

export interface WbRefreshResult {
  synced: number
  failed: string[]
}

/** 每日消耗点。 */
export interface WbUsageDay {
  date: string
  requests: number
  credit: number
  models: Record<string, number>
}

/** 按模型聚合消耗。 */
export interface WbUsageModel {
  model: string
  requests: number
  credit: number
}

export const getWbPanelData = () => api<WbPanelData>('/api/workbuddy-panel/data')
export const refreshWbPanel = () =>
  request<WbRefreshResult>('/api/workbuddy-panel/refresh', 'POST', {})
export const getWbSettings = () =>
  api<{ concurrency: number; auto_refresh: number }>('/api/workbuddy-panel/settings')
export const saveWbSettings = (concurrency: number) =>
  request<{ concurrency: number }>('/api/workbuddy-panel/settings', 'POST', {
    concurrency,
  })
export const saveWbAutoRefresh = (autoRefresh: boolean) =>
  request<{ concurrency: number; auto_refresh: number }>(
    '/api/workbuddy-panel/settings/auto-refresh',
    'POST',
    { auto_refresh: autoRefresh ? 1 : 0 },
  )

/**
 * KeyTone 与「模型状态」页完全同源（lib/modelStatus.ts）：ok / warn / bad / off。
 * 账号标签语义 = modelStatus.keyStatusBrief 的账号版：
 *   手动关闭 → 额度用尽 → 冷却中 → 已禁用 → 可用（正常）。
 */
export type WbAccountStatus = 'ok' | 'warn' | 'bad' | 'off'

export function wbAccountBrief(a: WbAccount): { label: string; tone: WbAccountStatus } {
  // 渠道手动关闭。
  if (a.channel && a.channel.effective_available === false) {
    if (a.channel.status === 'cooling') return { label: '冷却中', tone: 'warn' }
    if (a.channel.status === 'disabled') return { label: '已禁用', tone: 'bad' }
    return { label: '手动关闭', tone: 'off' }
  }
  // 与「模型状态」页同口径：只要有异常模型（禁用/冷却/手动关），这个 Key 就算异常，
  // 标签取最严重异常的成因（disabled > cooling-quota > cooling > manual）。
  const states = Object.values(a.model_states || {})
  const abnormal = states.filter((m) => !m.effective_available)
  if (abnormal.length > 0) {
    // 最严重优先：disabled > cooling（quota/daily → 额度用尽）> 手动关闭。
    const disabled = abnormal.find((m) => m.status === 'disabled')
    if (disabled) {
      const cls = disabled.failure_class || ''
      if (cls.includes('quota') || cls.includes('daily')) return { label: '额度用尽', tone: 'warn' }
      return { label: '已禁用', tone: 'bad' }
    }
    const cooling = abnormal.find((m) => m.status === 'cooling')
    if (cooling) {
      const cls = cooling.failure_class || ''
      if (cls.includes('quota') || cls.includes('daily')) return { label: '额度用尽', tone: 'warn' }
      return { label: '冷却中', tone: 'warn' }
    }
    return { label: '手动关闭', tone: 'off' }
  }
  if (a.last_error) return { label: '冷却中', tone: 'warn' }
  if (!a.synced_at) return { label: '已禁用', tone: 'bad' }
  return { label: '可用', tone: 'ok' }
}

/** 排序权重：可用在前，异常沉底（与 keyGroup 顺序一致）。 */
export function wbAccountStatus(a: WbAccount): WbAccountStatus {
  return wbAccountBrief(a).tone
}

export const WB_TONE_SOFT: Record<WbAccountStatus, string> = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  warn: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  bad: 'bg-red-500/15 text-red-700 dark:text-red-300',
  off: 'bg-slate-500/15 text-slate-700 dark:text-slate-300',
}

export const WB_TONE_DOT: Record<WbAccountStatus, string> = {
  ok: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-red-500',
  off: 'bg-slate-400',
}

/** 模型健康短标签（后端已算好，这里只兜底）。 */
export function wbModelLabel(a: WbAccount, modelId: string): string {
  const st = a.model_states?.[modelId]
  if (st) return st.label
  // model_states 无记录 = 从未失败，可用。
  return '可用'
}
