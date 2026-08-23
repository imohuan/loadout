import { api } from '@/lib/api'
import type { RequestLogPage, RequestLogDetail } from '@/lib/types'

export interface RequestLogFilters {
  model?: string
  channel?: string
  request_id?: string
  result?: string
  status_code?: string
  stream?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

export function useRequestLogs() {
  function list(filters: RequestLogFilters = {}) {
    const search = new URLSearchParams()
    if (filters.model) search.set('model', filters.model)
    if (filters.channel) search.set('channel', filters.channel)
    if (filters.request_id) search.set('request_id', filters.request_id)
    if (filters.result) search.set('result', filters.result)
    if (filters.status_code) search.set('status_code', filters.status_code)
    if (filters.stream) search.set('stream', filters.stream)
    if (filters.from) search.set('from', filters.from)
    if (filters.to) search.set('to', filters.to)
    if (filters.limit) search.set('limit', String(filters.limit))
    if (filters.offset) search.set('offset', String(filters.offset))
    return api<RequestLogPage>(`/api/request-logs${search.size ? `?${search}` : ''}`)
  }
  const detail = (id: string) => api<RequestLogDetail>(`/api/request-logs/${id}`)
  return { list, detail }
}
