import { api, request } from '@/lib/api'
import type { ChannelStatus } from '@/lib/types'

export interface ModelStatusFilters {
  model?: string
  manual_enabled?: boolean
  status?: string
}

export function useModelStatus() {
  const list = () => api<ChannelStatus[]>('/api/model-status')
  const setChannel = (id: string, manual_enabled: boolean) =>
    request<void>(`/api/model-status/channels/${id}`, 'PATCH', { manual_enabled })
  const setModel = (channelId: string, model: string, manual_enabled: boolean) =>
    request<void>(`/api/model-status/models/${channelId}/${encodeURIComponent(model)}`, 'PATCH', {
      manual_enabled,
    })
  const setModels = (channelId: string, models: string[], manual_enabled: boolean) =>
    request<void>(`/api/model-status/models/${channelId}`, 'PATCH', {
      models,
      manual_enabled,
    })
  const deleteModel = (channelId: string, model: string) =>
    request<void>(`/api/model-status/models/${channelId}/${encodeURIComponent(model)}`, 'DELETE')
  const deleteModels = (channelId: string, models: string[]) =>
    request<void>(`/api/model-status/models/${channelId}`, 'DELETE', { models })
  const recoverChannel = (id: string) =>
    request<void>(`/api/model-status/channels/${id}/recover`, 'POST')
  const recoverModel = (channelId: string, model: string) =>
    request<void>(
      `/api/model-status/models/${channelId}/${encodeURIComponent(model)}/recover`,
      'POST',
    )
  const recoverModels = (channelId: string, models: string[]) =>
    request<void>(`/api/model-status/models/${channelId}/recover`, 'POST', { models })
  const check = () => request<void>('/api/model-status/check', 'POST')
  const recoverAll = () =>
    request<{ ok: boolean; affected: number }>('/api/model-status/recover-all', 'POST')
  const recoverAllByChannel = (channelId: string) =>
    request<{ ok: boolean; affected: number }>(
      `/api/model-status/channels/${channelId}/recover-all`,
      'POST',
    )
  const recoverAllChannels = () =>
    request<{ ok: boolean; affected: number }>('/api/model-status/recover-all-channels', 'POST')
  return {
    list,
    setChannel,
    setModel,
    setModels,
    deleteModel,
    deleteModels,
    recoverChannel,
    recoverModel,
    recoverModels,
    check,
    recoverAll,
    recoverAllByChannel,
    recoverAllChannels,
  }
}
