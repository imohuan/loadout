// 火山引擎免费额度——模型匹配规则与聚合辅助。
// 完整复刻后端 volc-free-quota 扣减/拦截用的同一套规则（service.go
// matchOne / normalizeModelName / shareSignificantToken 系列），供前端
// 关注模型分组等多处复用。必须与后端一致：卡片显示的"关注命中"要等于后端
// 实际扣额度的模型，否则出现「显示有额度但请求被拦」或「显示无额度但能扣」的错觉。
import type { VolcQuotaAggregate } from '@/lib/types'

function normalizeModelName(name: string): string {
  let out = ''
  for (const ch of name) {
    const code = ch.codePointAt(0)!
    if (ch === ' ' || ch === '·' || ch === '、') continue
    if (ch >= 'a' && ch <= 'z') out += ch
    else if (ch >= 'A' && ch <= 'Z') out += String.fromCharCode(code + 32)
    else if (ch >= '0' && ch <= '9') out += ch
    else if (ch === '-' || ch === '_' || ch === '.') out += '-'
  }
  return out
}

// 模型修饰段（与型号无关，比较字母序列时忽略）：API 模型名比资源包 code 提取名
// 多出的正式版/预览标记。lite/turbo/pro 等是型号一部分，绝不能过滤。
const modelModifierSegs = new Set(['ga', 'preview', 'latest', 'beta'])

function alphaTokens(s: string): string[] {
  const out: string[] = []
  let i = 0
  while (i < s.length) {
    const c = s[i]
    if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'))) {
      i++
      continue
    }
    let j = i
    while (j < s.length && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z'))) j++
    out.push(s.slice(i, j))
    i = j
  }
  return out
}

function filterModifiers(segs: string[]): string[] {
  return segs.filter((s) => !modelModifierSegs.has(s))
}

// 显著 token = 含至少一个数字且总长 >= 2 的连续段（数字后紧跟 k/m/g 单位字母一并纳入）。
function significantTokens(s: string): string[] {
  const out: string[] = []
  let i = 0
  while (i < s.length) {
    const c = s[i]
    if (!(c >= '0' && c <= '9')) {
      i++
      continue
    }
    let j = i
    while (j < s.length && s[j] >= '0' && s[j] <= '9') j++
    if (j < s.length && ((s[j] >= 'a' && s[j] <= 'z') || (s[j] >= 'A' && s[j] <= 'Z'))) j++
    if (j - i >= 2) out.push(s.slice(i, j))
    i = j
  }
  return out
}

// 4 位 MMdd 与 6 位 YYMMdd 视为同一天：resource 包 code 用 "0731"，API 模型名用 "260731"。
function sameDateToken(a: string, b: string): boolean {
  if (!/^\d+$/.test(a) || !/^\d+$/.test(b)) return false
  const la = a.length
  const lb = b.length
  if (la === 4 && lb === 4) return a === b
  if (la === 6 && lb === 6) return a === b
  if (la === 4 && lb === 6) return a === b.slice(2)
  if (la === 6 && lb === 4) return a.slice(2) === b
  return false
}

function shareSignificantToken(a: string, b: string): boolean {
  const ta = significantTokens(a)
  const tb = significantTokens(b)
  let hit = false
  for (const x of ta) {
    for (const y of tb) {
      if (x === y || sameDateToken(x, y)) {
        hit = true
        break
      }
    }
    if (hit) break
  }
  if (!hit) return false
  const fa = filterModifiers(alphaTokens(a))
  const fb = filterModifiers(alphaTokens(b))
  return fa.length === fb.length && fa.every((v, i) => v === fb[i])
}

// matchOne：聚合短名 quota 与勾选 API 模型名是否命中（双向包含 / 显著 token 交集）。
export function matchQuotaModel(quota: string, apiModel: string): boolean {
  const q = normalizeModelName(quota)
  const a = normalizeModelName(apiModel)
  if (!q || !a) return false
  if (a.includes(q) || q.includes(a)) return true
  return shareSignificantToken(q, a)
}

// 一个勾选模型可能命中多个聚合短名（deepseek-v4-flash-ga-260731 ↔
// deepseek-v4-flash + deepseek-v4-flash-0731），合并 SUM 展示该模型总额度。
export function mergeAggregates(list: VolcQuotaAggregate[]): VolcQuotaAggregate | undefined {
  if (!list.length) return undefined
  if (list.length === 1) return { ...list[0] }
  const initialTotal = list.reduce((s, a) => s + a.initial_total, 0)
  const localRemaining = list.reduce((s, a) => s + a.local_remaining, 0)
  return {
    model: list[0].model,
    name: list[0].name || list[0].model,
    unit: list.find((a) => a.unit)?.unit || 'token',
    initial_total: initialTotal,
    local_remaining: localRemaining,
    used_amount: list.reduce(
      (s, a) => s + (a.initial_total > 0 ? a.initial_total - a.local_remaining : a.used_amount),
      0,
    ),
    total_amount: list.reduce((s, a) => s + a.total_amount, 0),
    percentage:
      initialTotal > 0 ? Math.max(0, Math.min(100, Math.round((localRemaining / initialTotal) * 100))) : 0,
    exhausted: initialTotal > 0 && localRemaining <= 0,
  }
}
