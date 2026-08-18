import { emitter } from './emitter'
import type { McpStats, ModelStats } from './types'

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
  const qs = params.toString()
  return api<McpStats>(`/api/stats/mcp${qs ? '?' + qs : ''}`)
}

export const getModelStats = (opts: { days?: number } = {}) => {
  const params = new URLSearchParams()
  if (opts.days) params.set('days', String(opts.days))
  const qs = params.toString()
  return api<ModelStats>(`/api/stats/models${qs ? '?' + qs : ''}`)
}
