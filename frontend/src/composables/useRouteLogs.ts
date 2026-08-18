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
  const detail = (requestId: string) => api<RouteLog>(`/api/route-logs/${requestId}`)
  const clear = () => request<void>('/api/route-logs', 'DELETE')
  return { list, detail, clear }
}
