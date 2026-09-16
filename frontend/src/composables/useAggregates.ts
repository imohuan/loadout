import { api, request } from '@/lib/api'
import type { Aggregate, CatalogModel } from '@/lib/types'

export function useAggregates() {
  const list = () => api<Aggregate[]>('/api/aggregates')
  const replaceAll = (items: Aggregate[]) => request<void>('/api/aggregates', 'PUT', items)
  /** 拉取「加载配置」下拉的候选模型（来自 OpenRouter 元数据缓存）。 */
  const catalogModels = () =>
    api<{ models: CatalogModel[]; count: number }>('/api/unifyai/catalog-models')
  return { list, replaceAll, catalogModels }
}
