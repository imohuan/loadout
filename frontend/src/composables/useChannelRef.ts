// 渠道引用统一格式化：「渠道名」/「渠道名(Key1, Key2)」
// 单一真理源：trigger / 列表 / 日志全部走这里。
//   - channel_base_url：渠道级 → 仅显示渠道名
//   - channel_ids（多 Key）：渠道名(Key1, Key2)
//   - channel_ids=[单个] 或 老 channel_id：渠道名(Key1)
//   - 空：返回空串
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from './useChannels'

export interface ChannelRef {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

function titleOf(channels: Channel[], ids: string[]): string {
  const groups = groupChannelsByBaseURL(channels)
  const group = groups.find((g) => g.keys.some((k) => ids.includes(k.id)))
  return group?.keys[0]?.channel_name || group?.keys[0]?.name || group?.baseUrl || ''
}

function namesOf(channels: Channel[], ids: string[]): string[] {
  return ids.map((id) => channels.find((c) => c.id === id)?.name || id)
}

// 核心规范函数。可直接返回字符串拼到文本里；也可在 Vue 组件里调用再渲染。
export function formatChannelRef(channels: Channel[], ref: ChannelRef | undefined | null): string {
  if (!ref) return ''
  if (ref.channel_base_url) {
    const groups = groupChannelsByBaseURL(channels)
    const group = groups.find(
      (g) => normalizeBaseURL(g.baseUrl) === normalizeBaseURL(ref.channel_base_url!),
    )
    if (group) {
      const first = group.keys[0]
      return first?.channel_name || first?.name || group.baseUrl
    }
    return ref.channel_base_url
  }
  const ids = ref.channel_ids?.length
    ? ref.channel_ids
    : ref.channel_id
      ? [ref.channel_id]
      : []
  if (!ids.length) return ''
  const title = titleOf(channels, ids)
  const names = namesOf(channels, ids)
  return title ? `${title}(${names.join(', ')})` : names.join(', ')
}
