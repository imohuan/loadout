import { request } from "./api"

// ===== 失败规则引擎 API =====

export interface RuleCondition {
  field: string
  op: string
  value: string | number
}

export interface RuleMatch {
  any?: RuleCondition[]
  all?: RuleCondition[]
}

export interface RuleAction {
  verdict: string
  recover?: string
  cooldown_seconds?: number
  daily_reset_hour?: number
  switch_account?: boolean
  fail_upgrade_count?: number
  fail_upgrade_recover?: string
  /** 冷却秒数模板：正则捕获组引用（如 $1），展开结果是数字 → 冷却秒数 */
  cooldown_seconds_template?: string
  /** 恢复时刻模板：正则捕获组引用（如 $1），展开结果解析成时间 → 恢复点 */
  recover_at_template?: string
}

export interface FailureRule {
  id: string
  name: string
  enabled: boolean
  source: string
  confirmed: boolean
  priority: number
  provider_base_url: string
  scope_mode?: string
  provider_base_urls?: string[]
  provider_framework?: string
  model: string
  match: RuleMatch
  action: RuleAction
  hit_count: number
  last_hit_at?: string
  created_at: string
  updated_at: string
}

export interface RuleInput {
  name: string
  enabled?: boolean
  priority: number
  provider_base_url: string
  scope_mode?: string
  provider_base_urls?: string[]
  provider_framework?: string
  model: string
  match: RuleMatch
  action: RuleAction
  confirm?: boolean
}

export interface RuleEvidence {
  request_id?: string
  model?: string
  channel_id?: string
  provider_url?: string
  status_code?: number
  body_code?: string
  message?: string
  fail_count?: number
}

export interface RuleDecision {
  id: number
  request_id: string
  model: string
  channel_id: string
  channel_name: string
  provider_base_url: string
  status_code: number
  error_excerpt: string
  matched_rule_id: string
  matched_rule_name: string
  ai_model: string
  ai_raw: string
  verdict: string
  created_at: string
}

export const listFailureRules = () => request<FailureRule[]>("/api/failure-rules", "GET")
export const createFailureRule = (body: RuleInput) =>
  request<FailureRule>("/api/failure-rules", "POST", body)
export const updateFailureRule = (id: string, body: RuleInput) =>
  request<FailureRule>(`/api/failure-rules/${id}`, "PUT", body)
export const deleteFailureRule = (id: string) =>
  request<{ ok: boolean }>(`/api/failure-rules/${id}`, "DELETE")
export const patchFailureRule = (id: string, body: { enabled?: boolean; confirm?: boolean }) =>
  request<{ ok: boolean }>(`/api/failure-rules/${id}`, "PATCH", body)
export const verifyFailureRule = (rule: Partial<FailureRule>, sample: RuleEvidence) =>
  request<{
    hit: boolean
    verdict?: string
    /** 预计恢复时刻（规则开启「提取恢复时间」且文案带时间时非空） */
    recover_until?: string
    /** 动作参数摘要（「恢复时间 2026-09-23 15:48:27」/「冷却 120 秒」） */
    action_params?: string
  }>("/api/failure-rules/verify", "POST", { rule, sample })
export const listRuleDecisions = (limit = 50) =>
  request<RuleDecision[]>(`/api/rule-decisions?limit=${limit}`, "GET")

// restoreDefaultFailureRules 把 14 条内置默认规则恢复成出厂状态（用户删掉/改坏后一键还原）。
export const restoreDefaultFailureRules = () =>
  request<{ ok: boolean; restored: number }>("/api/failure-rules-defaults/restore", "POST")

export interface ProviderFrameworkInfo {
  frameworks: string[]
  platforms: Array<{ base_url: string; name: string; framework: string }>
}

export const getProviderFrameworks = () =>
  request<ProviderFrameworkInfo>("/api/provider-frameworks", "GET")
