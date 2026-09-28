import type { ChannelStatus, ModelStatus } from '@/lib/types'

/**
 * 「模型状态」页的纯计算层：把后端返回的「一个 Key 一条记录」的扁平数据，
 * 归纳成「平台（= base_url 一组 Key）→ Key → 模型」三层。
 *
 * 之所以抽成不依赖 Vue 的纯函数：页面要按同一个口径做筛选、计数、上色，
 * 一旦散落在模板里三处各算一遍，数字迟早会对不上。
 */

/** 平台下一个模型的汇总：同名模型可能挂在多个 Key 上。 */
export interface PlatformModelSummary {
  model: string
  /** 只要有一个 Key 上该模型可用，就算这个平台的模型可用。 */
  available: boolean
  /** 挂了这个模型的 Key 数（含不可用的）。 */
  keyCount: number
  availableKeyCount: number
}

export type PlatformTone = 'ok' | 'warn' | 'bad' | 'off'

/** 平台（渠道组）的汇总结果。 */
export interface PlatformSummary {
  baseUrl: string
  /** 平台名：渠道名优先，缺失回落到首个 Key 名，再回落到 base_url。 */
  name: string
  keys: ChannelStatus[]
  keyCount: number
  availableKeyCount: number
  /** 去重后的模型总数（同名只算一个）。 */
  modelCount: number
  availableModelCount: number
  /** 去重后的模型明细，顺序 = 首次出现的顺序。 */
  models: PlatformModelSummary[]
  /** 所有 Key 都被手动关闭。 */
  allManualOff: boolean
  /** 平台整体色调，决定 tag 上的圆点与徽章。 */
  tone: PlatformTone
  /**
   * 搜索只命中「模型名」时，这里给出命中的模型名列表（用于把 tag 里的 chip 收窄到
   * 相关的那几个）。命中平台名/Key 名时为 undefined —— 说明整个平台都相关，
   * 不该裁剪它自己的模型清单。
   */
  matchedModels?: string[]
}

/** 去掉尾部斜杠：https://x/v1 与 https://x/v1/ 是同一个平台。 */
export function normalizeBaseURL(url: string) {
  return url.replace(/\/+$/, '')
}

/**
 * 按 base_url 把 Key 归到平台组。组内保持原顺序（= 后端 position 顺序，
 * 也就是路由优先级），组间顺序 = 首次出现的顺序。
 */
export function groupByBaseURL(
  items: ChannelStatus[],
): { baseUrl: string; keys: ChannelStatus[] }[] {
  const map = new Map<string, ChannelStatus[]>()
  for (const item of items) {
    const key = normalizeBaseURL(item.channel.base_url)
    const list = map.get(key)
    if (list) list.push(item)
    else map.set(key, [item])
  }
  return [...map.entries()].map(([baseUrl, keys]) => ({ baseUrl, keys }))
}

/**
 * 汇总一个平台。模型按名字去重：同名模型挂在多个 Key 上时，
 * 「可用」取并集（任一 Key 可用即可用），Key 数取挂载它的 Key 数量。
 */
export function summarizePlatform(keys: ChannelStatus[]): PlatformSummary {
  const modelMap = new Map<string, PlatformModelSummary>()

  for (const key of keys) {
    for (const m of key.models) {
      const existing = modelMap.get(m.model)
      if (existing) {
        existing.keyCount += 1
        if (m.effective_available) {
          existing.availableKeyCount += 1
          existing.available = true
        }
      } else {
        modelMap.set(m.model, {
          model: m.model,
          available: m.effective_available,
          keyCount: 1,
          availableKeyCount: m.effective_available ? 1 : 0,
        })
      }
    }
  }

  const models = [...modelMap.values()]
  // 模型数按名字去重：同一个模型挂在 N 个 Key 上仍然只是「一个模型」。
  const modelCount = models.length
  const availableModelCount = models.filter((m) => m.available).length
  const availableKeyCount = keys.filter((k) => k.effective_available).length
  const allManualOff = keys.length > 0 && keys.every((k) => !k.manual_enabled)

  let tone: PlatformTone
  if (allManualOff) tone = 'off'
  else if (keys.length === 0) tone = 'bad'
  else if (models.length > 0 && availableModelCount === 0) tone = 'bad'
  else if (availableModelCount < models.length || availableKeyCount < keys.length) tone = 'warn'
  else tone = 'ok'

  const first = keys[0]?.channel
  const name =
    first?.channel_name || first?.name || normalizeBaseURL(first?.base_url || '') || '未命名平台'

  return {
    baseUrl: normalizeBaseURL(first?.base_url || ''),
    name,
    keys,
    keyCount: keys.length,
    availableKeyCount,
    modelCount,
    availableModelCount,
    models,
    allManualOff,
    tone,
  }
}

/** 汇总全部平台。 */
export function summarizePlatforms(items: ChannelStatus[]): PlatformSummary[] {
  return groupByBaseURL(items).map((group) => summarizePlatform(group.keys))
}

/** 把 * 通配符转成正则（其余字符按字面量处理）。 */
export function wildcardToRegex(pattern: string): RegExp {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&')
  return new RegExp('^' + escaped.replace(/\*/g, '.*') + '$', 'i')
}

/**
 * 关键词是否命中给定文本。空关键词一律命中；带 * 走通配符匹配，否则走包含匹配。
 */
export function textMatches(text: string, query?: string): boolean {
  if (!query || !query.trim()) return true
  const q = query.trim()
  if (q.includes('*')) return wildcardToRegex(q).test(text)
  return text.toLowerCase().includes(q.toLowerCase())
}

export interface PlatformFilters {
  /** 搜索词，同时匹配平台名 / Key 名 / 模型名。 */
  keyword?: string
  /** 只看有异常的平台 / 只看全部正常的平台。 */
  health?: 'all' | 'issue' | 'ok'
}

/**
 * 平台是否通过筛选；通过时按需补上 matchedModels。
 *
 * 「平台名 / 地址 / Key 名命中」与「只有模型名命中」要区别对待：前者说明整个平台
 * 都相关，tag 里照常列出它的模型；后者只说明其中几个模型相关，把 chip 收窄到那几个
 * 才算真正回答了用户的搜索。两种情况都算通过筛选。
 */
export function matchPlatform(summary: PlatformSummary, filters: PlatformFilters): boolean {
  const kw = filters.keyword
  if (kw && kw.trim()) {
    const platformMatch =
      textMatches(summary.name, kw) ||
      textMatches(summary.baseUrl, kw) ||
      summary.keys.some(
        (k) => textMatches(k.channel.name, kw) || textMatches(k.channel.channel_name || '', kw),
      )
    if (!platformMatch) {
      const hits = summary.models.filter((m) => textMatches(m.model, kw)).map((m) => m.model)
      if (hits.length === 0) return false
      summary.matchedModels = hits
    }
  }
  if (filters.health === 'issue' && summary.tone !== 'bad' && summary.tone !== 'warn') return false
  if (filters.health === 'ok' && summary.tone !== 'ok') return false
  return true
}

/** 按筛选条件过滤平台列表。 */
export function filterPlatforms(
  items: ChannelStatus[],
  filters: PlatformFilters,
): PlatformSummary[] {
  // summarizePlatforms 每次都新建对象，所以在结果上直接挂 matchedModels 不会污染缓存。
  return summarizePlatforms(items).filter((s) => matchPlatform(s, filters))
}

/** 单个 Key 是否通过筛选（状态二左栏用）。 */
export function matchKey(key: ChannelStatus, keyword?: string): boolean {
  if (!keyword || !keyword.trim()) return true
  return (
    textMatches(key.channel.name, keyword) ||
    textMatches(key.channel.channel_name || '', keyword) ||
    key.models.some((m: ModelStatus) => textMatches(m.model, keyword))
  )
}

/** Key 的可用模型数（用于列表副标题）。 */
export function availableModelCount(key: ChannelStatus): number {
  return key.models.filter((m) => m.effective_available).length
}

export type KeyTone = 'ok' | 'warn' | 'bad' | 'off'

/**
 * Key 在左栏列表里的一行短标签。
 *
 * 左栏只有 16rem 宽，放不下 ModelHealthBadge 那种完整徽标（「账号已禁用（需手动恢复）」
 * 一条就占满整行，把 Key 名挤成省略号）。这里给一个两三个字的短版本，完整成因与
 * 恢复时间仍然在右栏头部用 ModelHealthBadge 展示。
 */
export function keyStatusBrief(key: ChannelStatus): { label: string; tone: KeyTone } {
  if (!key.manual_enabled) return { label: '手动关闭', tone: 'off' }
  if (key.health_status === 'available' && key.effective_available) {
    return { label: '可用', tone: 'ok' }
  }
  const cls = key.failure_class || ''
  // 额度用尽与「临时冷却」都落在 cooling，成因不同，标签要能区分：
  // 前者等次日刷新（规则引擎的每日恢复 / 免费额度耗尽），后者只是短暂退避。
  // 这里只做宽松的字符串归类，与 ModelHealthBadge 的 recover=daily 口径一致。
  if (cls.indexOf('quota') >= 0 || cls.indexOf('daily') >= 0) {
    return { label: '额度用尽', tone: 'warn' }
  }
  if (key.health_status === 'cooling') return { label: '冷却中', tone: 'warn' }
  return { label: '已禁用', tone: 'bad' }
}
