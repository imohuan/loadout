import { api, request } from '@/lib/api'
import type { CapabilityRoute } from '@/lib/types'

export function useCapabilityRoutes() {
  const list = () => api<CapabilityRoute[]>('/api/capability-routes')
  const replaceAll = (items: CapabilityRoute[]) =>
    request<void>('/api/capability-routes', 'PUT', items)
  return { list, replaceAll }
}
