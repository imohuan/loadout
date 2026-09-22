// ===== 样本回放 / AI 生成规则（「回撤」） API =====
import { request } from './api'

export interface RuleSample {
  id: string
  provider_base_url: string
  model: string
  status_code: number
  body_code: string
  message: string
  channel_id?: string
  channel_name?: string
  provider_framework?: string
  fingerprint: string
  source: 'real' | 'builtin' | string
  expected_verdict?: string
  confirmed: boolean
  note?: string
  created_at: string
  updated_at: string
}

export interface SampleReplayResult {
  sample_id: string
  matched_rule_id?: string
  matched_rule_name?: string
  verdict: string
  expected?: string
  expected_ok: boolean
  reason?: string
}

export interface SampleReplaySummary {
  results: SampleReplayResult[]
  total: number
  matched: number
  unmatched: number
  confirmed_ok: number
  confirmed_total: number
  inconsistent_ids: string[] | null
}

export interface AuthorRound {
  round: number
  /** 本轮 AI 交出的规则摘要（如「all(status_code eq 599) 动作 ignore」） */
  attempt?: string
  verdict?: string
  ok: boolean
  note?: string
}

export interface AuthorSession {
  id: string
  sample_id: string
  status: 'running' | 'done' | 'failed' | 'exhausted' | string
  rounds: number
  max_rounds: number
  round_detail: AuthorRound[] | null
  /** 当前轮流式输出尾部（打字机预览；非 running 时为空） */
  stream_tail?: string
  /** 当前这段预览的类型：reasoning=模型在思考 / content=正式输出 */
  stream_kind?: 'reasoning' | 'content' | string
  draft_rule_id?: string
  ai_model?: string
  error?: string
  created_at: string
  updated_at: string
}

export const listRuleSamples = (opts: { source?: string; model?: string; limit?: number } = {}) => {
  const p = new URLSearchParams()
  if (opts.source) p.set('source', opts.source)
  if (opts.model) p.set('model', opts.model)
  p.set('limit', String(opts.limit ?? 500))
  return request<RuleSample[]>(`/api/rule-samples?${p}`, "GET")
}

export const importRuleSamples = (limit = 2000) =>
  request<{
    ok: boolean
    imported: number
    total: number
    /** 本次扫描的判定日志条数 */
    scanned: number
    /** 扫描到但已存在同样本（指纹命中）而被并入的条数 */
    merged: number
    /** 本次扫描上限 */
    limit: number
    /** 是否因为上限没扫全 */
    truncated: boolean
  }>("/api/rule-samples/import", "POST", { limit })

export const replayRuleSamples = (ids: string[] = []) =>
  request<SampleReplaySummary>("/api/rule-samples/replay", "POST", { ids })

export const setRuleSampleExpectation = (id: string, expected_verdict: string, confirmed: boolean) =>
  request<{ ok: boolean }>(`/api/rule-samples/${id}`, "PATCH", { expected_verdict, confirmed })

export const deleteRuleSample = (id: string) =>
  request<{ ok: boolean }>(`/api/rule-samples/${id}`, "DELETE")

export const authorRuleFromSample = (sampleId: string) =>
  request<AuthorSession>("/api/rule-samples/author", "POST", { sample_id: sampleId })

export const listRuleAuthorSessions = (limit = 50) =>
  request<AuthorSession[]>(`/api/rule-author-sessions?limit=${limit}`, "GET")

// getRuleAuthorSession 读单个生成会话（轮次进度轮询）。
export const getRuleAuthorSession = (id: string) =>
  request<AuthorSession>(`/api/rule-author-sessions/${id}`, "GET")
