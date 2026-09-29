import type { Channel } from '@/lib/types'

/**
 * 「渠道与模型」页的纯计算层：把后端「一个 Key 一条记录」的扁平列表，
 * 归纳成「平台（同 base_url 的一组 Key）→ Key」两层。
 *
 * 与模型状态页的 lib/modelStatus.ts 同构：页面只负责取数据与转发事件，
 * 分组、计数、状态汇总都走这里，避免模板里三处各算一遍导致数字对不上。
 */

/** Key 的手动开关：manual_enabled 优先，兼容旧 JSON 的 enabled 字段，默认启用。 */
export function channelEnabled(channel: Channel): boolean {
  return channel.manual_enabled ?? channel.enabled ?? true
}

/**
 * Key 的模型数：探测失败返回 -1（未知，区别于「已知 0 个」）。
 *
 * 0 与「探测失败」在 UI 上是两件事：前者说明用户清空了清单，后者说明这次没探到，
 * 合并展示会让用户以为模型被删了。
 */
export function channelModelCount(channel: Channel): number {
  if (channel.models_error) return -1
  return channel.models?.length ?? 0
}

/**
 * Key 模型目录的一句话描述。
 *
 * models_error 是「**上一次**探测的结果」，不是「当前没有模型」——探测失败时
 * 目录里往往仍留着上次成功探测的缓存。所以有模型就说模型数、把失败作为附注，
 * 否则会出现「模型探测失败」和下面一整列模型并排的自相矛盾。
 */
export function channelModelLabel(channel: Channel): string {
  const count = channel.models?.length ?? 0
  if (channel.models_error) {
    return count > 0 ? `${count} 个模型（上次探测失败）` : '模型探测失败'
  }
  return `${count} 个模型`
}

/** 平台启用状态：全部启用 / 部分启用 / 全部禁用。 */
export type PlatformEnabledState = 'all' | 'partial' | 'none'

export interface ChannelPlatform {
  baseUrl: string
  /** 平台名：channel_name → 首个 Key 名 → base_url。 */
  name: string
  keys: Channel[]
  keyCount: number
  enabledKeyCount: number
  enabledState: PlatformEnabledState
  /** 跨 Key 去重后的模型总数。 */
  modelCount: number
  /** 去重后的模型清单（首次出现顺序）。 */
  models: string[]
  /** 任一 Key 探测失败：模型清单不可信，tag 上要标出来。 */
  probedFailed: boolean
}

/** 去掉尾部斜杠：https://x/v1 与 https://x/v1/ 是同一个平台。 */
export function normalizeBaseURL(url: string) {
  return url.replace(/\/+$/, '')
}

/**
 * 按 base_url 归组为平台。组内保持原顺序（= position 顺序，即 Key 优先级），
 * 组间顺序 = 首次出现顺序（= 渠道列表顺序，即普通模型路由优先级）。
 */
export function summarizeChannelPlatforms(channels: Channel[]): ChannelPlatform[] {
  const groups = new Map<string, Channel[]>()
  for (const channel of channels) {
    const key = normalizeBaseURL(channel.base_url)
    const list = groups.get(key)
    if (list) list.push(channel)
    else groups.set(key, [channel])
  }

  return [...groups.entries()].map(([baseUrl, keys]) => {
    const enabledKeyCount = keys.filter(channelEnabled).length
    const models: string[] = []
    const seen = new Set<string>()
    for (const key of keys) {
      for (const model of key.models || []) {
        if (seen.has(model)) continue
        seen.add(model)
        models.push(model)
      }
    }
    const first = keys[0]
    return {
      baseUrl,
      name: first?.channel_name || first?.name || baseUrl,
      keys,
      keyCount: keys.length,
      enabledKeyCount,
      enabledState:
        keys.length > 0 && enabledKeyCount === keys.length
          ? 'all'
          : enabledKeyCount > 0
            ? 'partial'
            : 'none',
      modelCount: models.length,
      models,
      probedFailed: keys.some((key) => channelModelCount(key) < 0),
    }
  })
}
