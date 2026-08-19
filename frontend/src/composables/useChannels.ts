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

// ChannelGroup 按 base_url 归组后的渠道组：一组 = 一个「渠道」，组内每行 = 一个 Key（账号）。
export interface ChannelGroup {
  baseUrl: string
  keys: Channel[]
}

// normalizeBaseURL 去掉尾部斜杠：https://x/v1 与 https://x/v1/ 视为同一渠道组。
function normalizeBaseURL(url: string) {
  return url.replace(/\/+$/, '')
}

// groupChannelsByBaseURL 按 base_url 分组（忽略尾斜杠差异），组内保持原顺序
//（= position 顺序，即路由优先级）。
export function groupChannelsByBaseURL(channels: Channel[]): ChannelGroup[] {
  const groups = new Map<string, Channel[]>()
  for (const ch of channels) {
    const key = normalizeBaseURL(ch.base_url)
    const list = groups.get(key) || []
    list.push(ch)
    groups.set(key, list)
  }
  return [...groups.entries()].map(([baseUrl, keys]) => ({ baseUrl, keys }))
}

export function useChannels() {
  const list = () => api<Channel[]>('/api/channels')
  const save = (input: ChannelInput, id?: string) =>
    request<Channel>(id ? `/api/channels/${id}` : '/api/channels', id ? 'PUT' : 'POST', input)
  const remove = (id: string) => request<void>(`/api/channels/${id}`, 'DELETE')
  const move = (id: string, direction: 'up' | 'down') =>
    request<void>(`/api/channels/${id}/move`, 'POST', { direction })
  /** 全量重排渠道顺序：提交 id 数组按新顺序，支撑按 base_url 分组的整组移动 */
  const reorder = (ids: string[]) => request<void>('/api/channels/reorder', 'POST', { ids })
  /** 启用/禁用单个 key（渠道记录） */
  const setEnabled = (id: string, enabled: boolean) =>
    request<void>(`/api/model-status/channels/${id}`, 'PATCH', { manual_enabled: enabled })
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
  return { list, save, remove, move, reorder, setEnabled, refreshModels, replaceModels, test }
}
