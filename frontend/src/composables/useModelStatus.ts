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
  // 「恢复本平台」：按 base_url 清该平台全部 Key 的自动熔断。
  const recoverPlatform = (baseUrl: string) =>
    request<{ ok: boolean; affected: number }>('/api/model-status/platforms/recover', 'POST', {
      base_url: baseUrl,
    })
  // 「强制开启本平台全部模型」（破坏性）：清自动熔断并强制打开手动开关。
  const recoverPlatformForced = (baseUrl: string) =>
    request<{ ok: boolean; affected: number }>(
      '/api/model-status/platforms/recover-forced',
      'POST',
      { base_url: baseUrl },
    )
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
    recoverPlatform,
    recoverPlatformForced,
  }
}
