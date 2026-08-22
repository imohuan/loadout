// 从上游错误响应文本里提取一句话摘要，用于在列表行里默认展示简短错误，
// hover 时再通过悬浮卡片看完整 JSON。
//
// 上游错误体五花八门（CodeBuddy / 各家网关 / 各厂商 SDK），提取规则：
//  1. 先解析 JSON（{ 或 [ 开头才尝试，避免把 HTML/纯文本误当 JSON）。
//  2. 深度遍历整个对象树（不限于已知 key——常见嵌套如 error.data.msg、
//     error.message、message 等，中间层 key 可能是任意名字）。
//  3. 在每一层按优先级找字符串字段：msg → message → errorMessage → errMsg
//     → description → error → reason → detail，取第一个非空。
//  4. 数组元素若是对象也递归。
//  5. 全都没找到才回退到外部传入的 error_message。
// 摘要会压缩换行并截断（maxLen），避免撑爆表格。

const FIELD_PRIORITY = [
  'msg',
  'message',
  'errorMessage',
  'errMsg',
  'description',
  'error',
  'reason',
  'detail',
] as const

// 本层内按优先级取第一个非空字符串字段。
function pickFromLevel(obj: Record<string, unknown>): string | undefined {
  for (const key of FIELD_PRIORITY) {
    const v = obj[key]
    if (typeof v === 'string' && v.trim()) return v.trim()
  }
  return undefined
}

function pickField(value: unknown): string | undefined {
  // 对象：先看本层优先级字段，再遍历所有子值递归（key 不限于已知列表）
  if (value && typeof value === 'object') {
    if (Array.isArray(value)) {
      for (const item of value) {
        const inner = pickField(item)
        if (inner) return inner
      }
      return undefined
    }
    const obj = value as Record<string, unknown>
    const direct = pickFromLevel(obj)
    if (direct) return direct
    for (const key of Object.keys(obj)) {
      const inner = pickField(obj[key])
      if (inner) return inner
    }
  }
  return undefined
}

function tryParseJson(s: string): unknown {
  const trimmed = (s || '').trim()
  if (!trimmed) return undefined
  // 只对看起来像 JSON 的内容尝试解析，避免误把普通文本当 JSON 报错。
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return undefined
  try {
    return JSON.parse(trimmed)
  } catch {
    return undefined
  }
}

/**
 * 从上游原始错误响应（`error_body`）+ 摘要字段（`error_message`）中提取一行摘要。
 *
 * 返回规则：
 * - 两者都有：`error_message + 提取到的 msg`，中间用「: 」拼接
 *   （error_message 是后端状态摘要，msg 是具体原因，信息互补都保留）；
 *   若 error_message 已包含/以 msg 结尾（上游已带同格式前缀时），直接返回
 *   error_message，避免再拼冒号造成多层重复前缀。
 * - 只有其一：返回那一份
 * - 都取不到：返回空字符串
 * @param errorBody 上游原始响应文本（可能是 JSON 也可能是纯文本）
 * @param fallback 后端摘要（通常用 `error_message`）
 * @param maxLen 摘要最大字符数，超出用省略号截断。0 表示不截断。
 */
export function extractErrorSummary(
  errorBody?: string | null,
  fallback?: string | null,
  maxLen = 80,
): string {
  const parsed = tryParseJson(errorBody || '')
  const bodyMsg = parsed ? pickField(parsed) : undefined
  const fbMsg = typeof fallback === 'string' && fallback.trim() ? fallback.trim() : undefined

  let summary: string
  if (bodyMsg && fbMsg) {
    // 后端摘要（error_message）已包含或以 bodyMsg 结尾时（典型如
    // 「上游返回错误(400): json: unknown field ...」），直接用 fbMsg，
    // 避免再拼冒号造成多层重复前缀。
    summary = fbMsg.includes(bodyMsg) || fbMsg.endsWith(bodyMsg) ? fbMsg : `${fbMsg}: ${bodyMsg}`
  } else {
    summary = bodyMsg || fbMsg || ''
  }
  if (!summary) return ''
  // 把多空格 / 换行压缩成单个空格，避免摘要破坏表格布局
  summary = summary.replace(/\s+/g, ' ').trim()
  if (maxLen > 0 && summary.length > maxLen) {
    summary = summary.slice(0, Math.max(1, maxLen - 1)) + '…'
  }
  return summary
}
