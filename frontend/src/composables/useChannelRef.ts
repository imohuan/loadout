// 渠道引用统一格式化：「渠道名」/「渠道名(Key1, Key2)」/「渠道A(k1), 渠道B(k2, k3)」（跨组聚合）。
// 单一真理源：trigger / 列表 / 日志 / 跨渠道标签全部走这里。
//   - channel_base_url：渠道级 → 仅显示渠道名
//   - channel_ids（多 Key）：
//       单组：「渠道名(Key1, Key2)」
//       跨组：「渠道A(k1), 渠道B(k2, k3)」（按 channels 出现顺序，「, 」分隔）
//       compactAll=true 且某组全部 Key 被选中：「渠道名」（视为选了整个渠道，省略括号，对齐 ChannelRef 的渠道级展示）
//   - channel_ids=[单个] 或 老 channel_id：「渠道名(Key1)」
//   - 空：返回空串
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from './useChannels'

export interface ChannelRefInput {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

// 渠道展示段（按 base_url 聚合的一段）：{ title, names, ids, baseUrl, allKeys }。
// - title   为该组渠道名（channel_name 兜底 name 兜底 baseUrl）
// - names   为该组内已选 Key 显示名（顺序按 channels 原序）
// - ids     为对应的 Channel id（用于删除整组 / 复用）
// - baseUrl 为分组的标准化 base_url（v-for key 用）
// - allKeys 该组全部 Key 是否都被选中（= 视为「选整个渠道」）
export interface ChannelGroupSegment {
  title: string
  names: string[]
  ids: string[]
  baseUrl: string
  allKeys: boolean
}

// 把 ids 按 base_url 分组聚合（忽略空 / 不在 channels 里的 id）。
// 段顺序与 channels 列表一致（前端稳定 key，便于定位）。
export function groupSegmentsFor(channels: Channel[], ids: string[]): ChannelGroupSegment[] {
  if (!ids.length) return []
  const idSet = new Set(ids)
  const byBase = new Map<string, ChannelGroupSegment>()
  const totalByBase = new Map<string, number>()
  for (const ch of channels) {
    const base = normalizeBaseURL(ch.base_url)
    totalByBase.set(base, (totalByBase.get(base) || 0) + 1)
    if (!idSet.has(ch.id)) continue
    let seg = byBase.get(base)
    if (!seg) {
      seg = {
        title: ch.channel_name || ch.name || ch.base_url,
        names: [],
        ids: [],
        baseUrl: base,
        allKeys: false,
      }
      byBase.set(base, seg)
    }
    seg.names.push(ch.name || ch.id)
    seg.ids.push(ch.id)
  }
  for (const seg of byBase.values()) {
    seg.allKeys = seg.ids.length >= (totalByBase.get(seg.baseUrl) || 0)
  }
  // channels 顺序遍历，Map 保留插入顺序，结果天然与 channels 顺序一致。
  return [...byBase.values()]
}

// 单组标签文本：「title(name1, name2)」；compactAll 且全组选中时仅「title」。
export function formatChannelGroupLabel(seg: ChannelGroupSegment, compactAll = false): string {
  if (compactAll && seg.allKeys) return seg.title
  return `${seg.title}(${seg.names.join(', ')})`
}

// 核心规范函数。多组 ids 跨组时按段逗号分隔（每段 "title(name1, name2)"）。
// compactAll=true 时全组选中的段省略括号（仅供「选渠道」语义场景使用，如能力路由编辑）。
export function formatChannelRef(
  channels: Channel[],
  ref: ChannelRefInput | undefined | null,
  compactAll = false,
): string {
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
  const ids = ref.channel_ids?.length ? ref.channel_ids : ref.channel_id ? [ref.channel_id] : []
  if (!ids.length) return ''
  const segments = groupSegmentsFor(channels, ids)
  if (!segments.length) return ''
  return segments.map((seg) => formatChannelGroupLabel(seg, compactAll)).join(', ')
}
