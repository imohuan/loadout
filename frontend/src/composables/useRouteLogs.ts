import { api, request } from '@/lib/api'
import type { RouteLog, RouteLogPage } from '@/lib/types'

export interface RouteLogFilters {
  model?: string
  channel_name?: string
  result?: string
  from?: string
  to?: string
}

function toISOString(value?: string) {
  if (!value) return undefined
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString()
}

export function useRouteLogs() {
  function list(
    filters: RouteLogFilters,
    pagination?: { page?: number; pageSize?: number },
  ) {
    const search = new URLSearchParams()
    if (filters.model) search.set('model', filters.model)
    if (filters.channel_name) search.set('channel_name', filters.channel_name)
    if (filters.result) search.set('result', filters.result)
    if (toISOString(filters.from)) search.set('from', toISOString(filters.from)!)
    if (toISOString(filters.to)) search.set('to', toISOString(filters.to)!)
    if (pagination?.page) search.set('page', String(pagination.page))
    if (pagination?.pageSize) search.set('pageSize', String(pagination.pageSize))
    return api<RouteLogPage>(`/api/route-logs${search.size ? `?${search}` : ''}`)
  }
  // detail：默认纯读；带 repair: true 时加 ?repair=1，触发后端对卡死 running 记录的自愈收尾。
  const detail = (requestId: string, options?: { repair?: boolean }) =>
    api<RouteLog>(`/api/route-logs/${requestId}${options?.repair ? '?repair=1' : ''}`)
  const clear = () => request<void>('/api/route-logs', 'DELETE')
  return { list, detail, clear }
}
