import { api, request } from '@/lib/api'
import type { Aggregate } from '@/lib/types'

export function useAggregates() {
  const list = () => api<Aggregate[]>('/api/aggregates')
  const replaceAll = (items: Aggregate[]) => request<void>('/api/aggregates', 'PUT', items)
  return { list, replaceAll }
}
