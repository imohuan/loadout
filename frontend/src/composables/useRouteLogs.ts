import { api, request } from '@/lib/api'
import type { RouteLog } from '@/lib/types'

export interface RouteLogFilters {
  model?: string
  channel_id?: string
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
  function list(filters: RouteLogFilters) {
    const search = new URLSearchParams()
    if (filters.model) search.set('model', filters.model)
    if (filters.channel_id) search.set('channel_id', filters.channel_id)
    if (filters.result) search.set('result', filters.result)
    if (toISOString(filters.from)) search.set('from', toISOString(filters.from)!)
    if (toISOString(filters.to)) search.set('to', toISOString(filters.to)!)
    return api<RouteLog[]>(`/api/route-logs${search.size ? `?${search}` : ''}`)
  }
  // detail：默认纯读；带 repair: true 时加 ?repair=1，触发后端对卡死 running 记录的自愈收尾。
  const detail = (requestId: string, options?: { repair?: boolean }) =>
    api<RouteLog>(`/api/route-logs/${requestId}${options?.repair ? '?repair=1' : ''}`)
  const clear = () => request<void>('/api/route-logs', 'DELETE')
  return { list, detail, clear }
}
