// 渠道引用统一格式化：「渠道名」/「渠道名(Key1, Key2)」/「渠道A, 渠道B(k2), ...」（跨组聚合）。
// 单一真理源：trigger / 列表 / 日志 / 跨渠道标签全部走这里。
// 语义：渠道级（点了渠道名）→ 只显示渠道名，无括号；
//       Key 级（勾了具体 Key，即使勾满整组）→ 渠道名(Key1, Key2)，括号内容必有。
//   - channel_base_url / channel_base_urls：渠道级 → 仅显示渠道名
//   - channel_ids / channel_id：Key 级 → 渠道名(Key1, Key2)
//   - 空：返回空串
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from './useChannels'

export interface ChannelRefInput {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
  /** 多渠道级（多渠道模式的渠道级多选） */
  channel_base_urls?: string[]
}

// 渠道展示段（按 base_url 聚合的一段）：{ title, names, ids, baseUrl, level }。
// - title   为该组渠道名（channel_name 兜底 name 兜底 baseUrl）
// - names   为该组内已选 Key 显示名（顺序按 channels 原序；渠道级段 = 组内全部 Key）
// - ids     为对应的 Channel id（用于删除整组 / 复用）
// - baseUrl 为分组的标准化 base_url（v-for key 用）
// - level   渠道级段（点过渠道名，显示省略括号）
export interface ChannelGroupSegment {
  title: string
  names: string[]
  ids: string[]
  baseUrl: string
  level: boolean
}

// 把 ids 按 base_url 分组聚合（忽略空 / 不在 channels 里的 id），全部为 Key 级段。
// 段顺序与 channels 列表一致（前端稳定 key，便于定位）。
export function groupSegmentsFor(channels: Channel[], ids: string[]): ChannelGroupSegment[] {
  if (!ids.length) return []
  const idSet = new Set(ids)
  const byBase = new Map<string, ChannelGroupSegment>()
  for (const ch of channels) {
    if (!idSet.has(ch.id)) continue
    const base = normalizeBaseURL(ch.base_url)
    let seg = byBase.get(base)
    if (!seg) {
      seg = {
        title: ch.channel_name || ch.name || ch.base_url,
        names: [],
        ids: [],
        baseUrl: base,
        level: false,
      }
      byBase.set(base, seg)
    }
    seg.names.push(ch.name || ch.id)
    seg.ids.push(ch.id)
  }
  // channels 顺序遍历，Map 保留插入顺序，结果天然与 channels 顺序一致。
  return [...byBase.values()]
}

// 渠道级段（由 base_url 列表构造）：整组视为已选，显示省略括号。
// 段顺序与 channels 列表一致。
export function channelLevelSegments(
  channels: Channel[],
  baseUrls: string[],
): ChannelGroupSegment[] {
  if (!baseUrls?.length) return []
  const wanted = new Set(baseUrls.map(normalizeBaseURL))
  const byBase = new Map<string, ChannelGroupSegment>()
  for (const ch of channels) {
    const base = normalizeBaseURL(ch.base_url)
    if (!wanted.has(base)) continue
    let seg = byBase.get(base)
    if (!seg) {
      seg = {
        title: ch.channel_name || ch.name || ch.base_url,
        names: [],
        ids: [],
        baseUrl: base,
        level: true,
      }
      byBase.set(base, seg)
    }
    seg.names.push(ch.name || ch.id)
    seg.ids.push(ch.id)
  }
  return [...byBase.values()]
}

// 单组标签文本：渠道级段 → 「title」；Key 级段 → 「title(name1, name2)」。
export function formatChannelGroupLabel(seg: ChannelGroupSegment): string {
  if (seg.level) return seg.title
  return `${seg.title}(${seg.names.join(', ')})`
}

// 合并两组段（渠道级段在前、Key 级段在后），跨组逗号分隔。
export function mergeSegments(...lists: ChannelGroupSegment[][]): ChannelGroupSegment[] {
  const out: ChannelGroupSegment[] = []
  for (const list of lists) out.push(...list)
  return out
}

// 核心规范函数：渠道级段无括号，Key 级段带括号，跨组逗号分隔。
export function formatChannelRef(
  channels: Channel[],
  ref: ChannelRefInput | undefined | null,
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
  const segments = mergeSegments(
    channelLevelSegments(channels, ref.channel_base_urls || []),
    groupSegmentsFor(channels, ids),
  )
  if (!segments.length) return ''
  return segments.map(formatChannelGroupLabel).join(', ')
}
