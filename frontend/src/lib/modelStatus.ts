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
      // 账号级熔断会跳过它名下的全部模型；模型自己的 effective_available 仍是 true，
      // 只看它就会把已停用的账号算成「模型可用」。两者同时成立才算真的可用。
      const usable = key.effective_available && m.effective_available
      const existing = modelMap.get(m.model)
      if (existing) {
        existing.keyCount += 1
        if (usable) {
          existing.availableKeyCount += 1
          existing.available = true
        }
      } else {
        modelMap.set(m.model, {
          model: m.model,
          available: usable,
          keyCount: 1,
          availableKeyCount: usable ? 1 : 0,
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
  // 账号已熔断 → 其下全部模型都不可路由。模型自己的状态可能还是 available，
  // 直接按模型状态计数会得到「9/11 可用」这种与真实路由相反的结论。
  if (!key.effective_available) return 0
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

/**
 * Key 行内「手动开关」应呈现的样子。
 *
 * 开关绑定「这个 Key 现在能不能用」这个二元事实：
 *   - 可用 → on，用户可以拨关（手动停用）。
 *   - 手动关闭 → off，用户可以拨开（后端会顺带清自动熔断）。
 *   - 自动熔断（额度用尽 / 限速冷却 / 鉴权失效…）→ off 且置灰：拨开关只改手动
 *     标志，自动熔断还在、路由照样跳过，正确出口是「恢复 Key」按钮。
 *
 * 之前把 manual_enabled 直接灌进开关值，「额度用尽」的 Key 看起来就像用户自己
 * 关掉了，这是误解的来源。
 */
export function keySwitchState(key: ChannelStatus): { value: boolean; disabled: boolean } {
  return {
    value: key.effective_available,
    disabled: key.manual_enabled && !key.effective_available,
  }
}

/**
 * 左栏 Key 列表的分组。
 *
 * 「排序」与「筛选标签」必须共用同一口径：两处各算一遍，迟早会出现
 * 「筛了可用，列表里却夹着一个不可用的 Key」这种自相矛盾的画面。
 */
export type KeyGroup = 'ok' | 'manual' | 'issue' | 'bad'

/**
 * 把 Key 归到四档：
 *   ok     可用（现在就能路由）
 *   manual 手动关闭（用户自己关的，打开开关即可恢复）
 *   issue  其他异常（冷却中 / 额度用尽，会随时间自愈）
 *   bad    不可用（鉴权失效 / 账号禁用，要人工介入）
 */
export function keyGroup(key: ChannelStatus): KeyGroup {
  if (key.effective_available) return 'ok'
  if (!key.manual_enabled) return 'manual'
  return keyStatusBrief(key).tone === 'bad' ? 'bad' : 'issue'
}

/** 分组展示顺序：可用 → 手动关闭 → 其他异常 → 不可用。 */
export const KEY_GROUP_ORDER: KeyGroup[] = ['ok', 'manual', 'issue', 'bad']

/** 分组的中文名（筛选标签用）。 */
export const KEY_GROUP_LABEL: Record<KeyGroup, string> = {
  ok: '可用',
  manual: '手动关闭',
  issue: '异常',
  bad: '不可用',
}

/**
 * 左栏排序：可用的排最上面、不可用的排最下面，其余按「手动关闭 → 其他异常」排中间。
 *
 * 用 Array.sort（ES2019 起保证稳定）而不是自己写插入：同组内保持后端返回的顺序，
 * 也就是路由优先级顺序，用户看到的先后不会因为点了几次刷新就乱掉。
 */
export function sortKeysByGroup(keys: ChannelStatus[]): ChannelStatus[] {
  return [...keys].sort(
    (a, b) => KEY_GROUP_ORDER.indexOf(keyGroup(a)) - KEY_GROUP_ORDER.indexOf(keyGroup(b)),
  )
}

/**
 * 左栏列表最终要渲染的 Key：先按分组排序，再按选中的标签过滤。
 *
 * 抽成纯函数是为了让「列表显示什么」和「批量测试测什么」用同一份结果 ——
 * 各算一遍的话，界面上写「测试 10 项」却跑了 17 个，这种事迟早发生。
 */
export function filterKeysByGroup(
  keys: ChannelStatus[],
  filter: 'all' | KeyGroup,
): ChannelStatus[] {
  const sorted = sortKeysByGroup(keys)
  return filter === 'all' ? sorted : sorted.filter((key) => keyGroup(key) === filter)
}

/**
 * 单个 Key 的测试进度（左栏列表里那一行状态）。
 *
 * 三种取值都会留在列表上，测完不自动消失 —— 同一批里哪些通过、哪些没过，
 * 扫一眼列表就能对上账；只有下一次点测试才会清掉重来。
 */
export interface KeyTestState {
  phase: 'running' | 'ok' | 'failed'
  /** 本次测试用的模型（三种取值都带，成功时也要说明测的是哪个）。 */
  model?: string
  /** 失败原因（上游原文，可能很长，列表里截断显示）。 */
  error?: string
}

/**
 * Key 的二元状态（开 / 关），给列表与详情头部的徽章用。
 *
 * 故障成因（额度用尽、账号禁用…）是另一层信息，用 keyStatusBrief 展示；
 * 两者混在一个徽标里，「额度用尽」看起来就像第三种开关状态，这正是要修掉的误解。
 */
export function keyEnabledState(key: ChannelStatus): {
  label: '开启' | '关闭'
  tone: 'ok' | 'off'
} {
  return key.effective_available ? { label: '开启', tone: 'ok' } : { label: '关闭', tone: 'off' }
}

/**
 * 平台整体状态的文字标签（与 summarizePlatform 的 tone 一一对应）。
 *
 * 圆点没有文字、颜色全靠猜 —— 黄点和灰点到底是「部分异常」还是「手动关闭」，
 * 不解释看不出来。这里给出统一文案：平台 tag 上直接渲染文字徽标；下拉触发条
 * 与菜单项空间窄，保留圆点 + title 提示，两处颜色同源不会对不上。
 */
export const PLATFORM_TONE_LABEL: Record<PlatformTone, string> = {
  ok: '全部可用',
  warn: '部分异常',
  bad: '不可用',
  off: '手动关闭',
}

/**
 * 状态语义色的唯一来源：绿=可用、琥珀=异常、红=不可用、灰=关闭/手动关闭。
 *
 * 同一个状态在页面上有多个落点（列表短标签、筛选标签、开关徽标、平台圆点），
 * 各画各的迟早会出现「同一个可用，两处绿得不一样」。这里集中一份，各处引用；
 * 绿必须和 PlatformTagGrid 的圆点、ModelHealthBadge 对齐。
 */
export const TONE_STYLE: Record<KeyTone, string> = {
  ok: 'border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300',
  warn: 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300',
  bad: 'border-red-500/30 bg-red-500/10 text-red-700 dark:text-red-300',
  off: 'border-slate-500/30 bg-slate-500/10 text-slate-700 dark:text-slate-300',
}

/** 同色板的加重版：用于「当前选中」这类需要更醒目的场合。 */
export const TONE_STYLE_STRONG: Record<KeyTone, string> = {
  ok: 'border-emerald-500 bg-emerald-500/25 font-semibold text-emerald-800 dark:text-emerald-200',
  warn: 'border-amber-500 bg-amber-500/25 font-semibold text-amber-800 dark:text-amber-200',
  bad: 'border-red-500 bg-red-500/25 font-semibold text-red-800 dark:text-red-200',
  off: 'border-slate-500 bg-slate-500/25 font-semibold text-slate-800 dark:text-slate-200',
}

/**
 * 同色板的「无描边纯色块」版：用于列表里那种一行小徽标（开关态、故障成因短标签）。
 *
 * 与 TONE_STYLE 分开写而不是从它派生：在类名字符串上做替换拼字符串，读的人
 * 得在脑子里跑一遍正则才知道最终长什么样，不如直接写清楚。
 */
export const TONE_STYLE_SOFT: Record<KeyTone, string> = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  warn: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  bad: 'bg-red-500/15 text-red-700 dark:text-red-300',
  off: 'bg-slate-500/15 text-slate-700 dark:text-slate-300',
}
