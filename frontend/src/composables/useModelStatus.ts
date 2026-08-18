import { api, request } from '@/lib/api'
import type { ChannelStatus } from '@/lib/types'

export function useModelStatus() {
  const list = () => api<ChannelStatus[]>('/api/model-status')
  const setChannel = (id: string, manual_enabled: boolean) =>
    request<void>(`/api/model-status/channels/${id}`, 'PATCH', { manual_enabled })
  const setModel = (channelId: string, model: string, manual_enabled: boolean) =>
    request<void>(`/api/model-status/models/${channelId}/${encodeURIComponent(model)}`, 'PATCH', {
      manual_enabled,
    })
  const recoverChannel = (id: string) =>
    request<void>(`/api/model-status/channels/${id}/recover`, 'POST')
  const recoverModel = (channelId: string, model: string) =>
    request<void>(
      `/api/model-status/models/${channelId}/${encodeURIComponent(model)}/recover`,
      'POST',
    )
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
    recoverChannel,
    recoverModel,
    check,
    recoverAll,
    recoverAllByChannel,
    recoverAllChannels,
  }
}
