import { emitter } from './emitter'
import type { McpInvocationPage, McpStats, ModelStats } from './types'

const jsonHeaders = { 'Content-Type': 'application/json' }

export class ApiError extends Error {
  status?: number

  constructor(message: string, status?: number) {
    super(message)
    this.status = status
  }
}

export async function api<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    headers:
      options.body instanceof FormData ? options.headers : { ...jsonHeaders, ...options.headers },
    ...options,
  })
  const body = await response.json().catch(() => ({}))
  if (response.status === 401) emitter.emit('unauthorized')
  if (!response.ok) {
    throw new ApiError(
      body.error?.message || body.message || `请求失败（${response.status}）`,
      response.status,
    )
  }
  return body as T
}

export function request<T>(path: string, method: string, body?: unknown) {
  return api<T>(path, { method, body: body === undefined ? undefined : JSON.stringify(body) })
}

export const getMcpStats = (opts: { days?: number; top?: number } = {}) => {
  const params = new URLSearchParams()
  if (opts.days) params.set('days', String(opts.days))
  if (opts.top) params.set('top', String(opts.top))
  const tz = clientTimeZone()
  if (tz) params.set('tz', tz)
  const qs = params.toString()
  return api<McpStats>(`/api/stats/mcp${qs ? '?' + qs : ''}`)
}

// getMcpInvocations 分页查询工具调用日志（mcp_invocations 明细）。
export const getMcpInvocations = (opts: {
  page?: number
  pageSize?: number
  kind?: string
  tool?: string
  server?: string
  auth?: string
} = {}) => {
  const params = new URLSearchParams()
  if (opts.page) params.set('page', String(opts.page))
  if (opts.pageSize) params.set('page_size', String(opts.pageSize))
  if (opts.kind) params.set('kind', opts.kind)
  if (opts.tool) params.set('tool', opts.tool)
  if (opts.server) params.set('server', opts.server)
  if (opts.auth) params.set('auth', opts.auth)
  const qs = params.toString()
  return api<McpInvocationPage>(`/api/mcp-invocations${qs ? '?' + qs : ''}`)
}

export const getModelStats = (opts: { days?: number } = {}) => {
  const params = new URLSearchParams()
  if (opts.days) params.set('days', String(opts.days))
  const tz = clientTimeZone()
  if (tz) params.set('tz', tz)
  const qs = params.toString()
  return api<ModelStats>(`/api/stats/models${qs ? '?' + qs : ''}`)
}

// clientTimeZone 返回浏览器 IANA 时区（如 "Asia/Shanghai"）；SSR/异常环境下为 ""，
// 服务端会回落到 time.Local。stats 端点用此 tz 算"今天"边界，避免 UTC 0:00–本地 08:00
// （GMT+8 区）用户的请求被归类到"昨天"。
function clientTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
}

// ---- 翻译（translate 插件）----

export interface TranslateRequest {
  source_text?: string
  texts?: string[]
  target_lang?: string
  model?: string
  prompt?: string
  source_type?: string
  source_id?: string
  key?: string
  type?: string
}

export interface TranslateResponse {
  texts?: string[]
  text?: string
}

export const translateText = (body: TranslateRequest) =>
  request<TranslateResponse>('/api/translate', 'POST', body)

export const getTranslateSources = () =>
  api<{
    items: {
      source_type: 'mcp' | 'skill' | 'custom'
      source_id: string
      name: string
      description: string
      input_schema?: Record<string, unknown>
      params?: { name: string; title?: string; description?: string; type?: string; required?: boolean }[]
    }[]
    count: number
  }>('/api/translate/sources')

export interface TranslateBatchRequest {
  items: { source_type: string; source_id: string; description: string; key?: string }[]
  target_lang?: string
  model?: string
  prompt?: string
  type?: string
  concurrency?: number
}

export interface TranslateBatchStart {
  task_id: string
  total: number
}

export interface TranslateBatchStatus {
  task_id: string
  done: number
  total: number
  running: boolean
  finished: boolean
  cancelled: boolean
  error?: string
}

// startTranslateBatch 启动一个后台批量翻译任务，立即返回 task_id。
// 任务在后端独立运行，不随本连接断开而取消；后续用 getTranslateBatchStatus 轮询进度。
export const startTranslateBatch = (body: TranslateBatchRequest) =>
  request<TranslateBatchStart>('/api/translate/batch', 'POST', body)

// getTranslateBatchStatus 查询后台批量翻译任务进度。
export const getTranslateBatchStatus = (taskId: string) =>
  api<TranslateBatchStatus>(`/api/translate/batch/status?task_id=${encodeURIComponent(taskId)}`)

// cancelTranslateBatch 取消后台批量翻译任务。
export const cancelTranslateBatch = (taskId: string) =>
  request<{ task_id: string; cancelled: boolean }>(
    `/api/translate/batch/cancel?task_id=${encodeURIComponent(taskId)}`,
    'POST',
  )

// translateLookup 只读查询已有译文（不触发翻译）。texts 与结果一一对应，未命中为 null。
export const translateLookup = (body: {
  source_text?: string
  target_lang?: string
  type?: string
  items?: { text: string }[]
}) => request<{ texts: (string | null)[] }>('/api/translate/lookup', 'POST', body)
