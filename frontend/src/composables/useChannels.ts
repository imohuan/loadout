import { api, request } from '@/lib/api'
import type { Channel } from '@/lib/types'

export interface ChannelInput {
  name: string
  base_url: string
  api_key: string
  manual_enabled: boolean
  sync_billing: boolean
  /** 选中的（启用的）模型 */
  models?: string[]
  /** 全部候选模型（含禁用的、自定义的），用于保存后全量替换 */
  model_candidates?: string[]
}

export interface ChannelModelInput {
  model: string
  enabled: boolean
}

export function useChannels() {
  const list = () => api<Channel[]>('/api/channels')
  const save = (input: ChannelInput, id?: string) =>
    request<Channel>(id ? `/api/channels/${id}` : '/api/channels', id ? 'PUT' : 'POST', input)
  const remove = (id: string) => request<void>(`/api/channels/${id}`, 'DELETE')
  const move = (id: string, direction: 'up' | 'down') =>
    request<void>(`/api/channels/${id}/move`, 'POST', { direction })
  const refreshModels = (id: string) =>
    request<void>(`/api/channels/${id}/refresh-models`, 'POST', {})
  const replaceModels = (id: string, models: ChannelModelInput[]) =>
    request<void>(`/api/channels/${id}/models`, 'PUT', models)
  const test = (id: string, model: string, vision: boolean) =>
    request<{ ok: boolean; latency_ms?: number; reply?: string; error?: string }>(
      '/api/channels/test',
      'POST',
      { id, model, vision },
    )
  return { list, save, remove, move, refreshModels, replaceModels, test }
}
