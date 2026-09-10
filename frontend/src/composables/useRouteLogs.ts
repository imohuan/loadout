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

/**
 * 本页记录里仍然存在于完整日志库的 request-log id 集合。
 *
 * 后端只回「还存在的那些」（request_log_ids）+ request_log_resolved 标记：
 *   - resolved=false（能力未装配/查询失败）→ 返回 undefined，表格退化为只判空；
 *   - resolved=true  → 返回 Set，表格据此隐藏已被保留策略清理掉的「进入日志」入口。
 *     「查到了但一条都没有」（空集合）也要返回 Set，那代表所有入口都该藏起来。
 */
export function routeLogValidity(page?: RouteLogPage): Set<string> | undefined {
  if (!page?.request_log_resolved) return undefined
  return new Set(page.request_log_ids ?? [])
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
