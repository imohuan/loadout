/**
 * tableWidth — 表格自动列宽的计算核心（纯函数，无 DOM 依赖）
 *
 * 目标：列宽由「内容自然宽度」决定，调用方只需给某一列一个上限（最大宽度）；
 * 上限以内的列跟随内容，超过上限的内容换行显示，而不是被切掉。
 *
 * 为什么不是一句 CSS 就能解决（以下均为真实 Chrome 实测结论）：
 *   - table-layout:auto + width:100% 时，单元格上的 max-width 只是「偏好」，
 *     列仍会被富余空间撑大（实测 max-width:320px 的列被撑到 696px），上限形同虚设。
 *   - table-layout:fixed 才是「宽度说了算」，但它必须配合明确的列宽，
 *     而列宽只能由「内容自然宽度」算出来 —— 那正是本模块的职责。
 *
 * 核心不变式（三条，缺一不可）：
 *   1. 装得下时，列宽贴着内容走，不出现难看的横向滚动条。
 *   2. 装不下时，压缩「最宽的那一列」而不是全部平摊 —— 超宽通常只来自某一列的长文本，
 *      平摊会把每列都挤窄、每列都换行，反而更难读。
 *   3. 永远不裁掉内容：被压缩的列由组件改成换行显示（见 AxTable 的表格 CSS），
 *      所以压缩是安全的；下限由调用方的 min-w-* 决定，没写才回落到兜底值。
 */

/** 未显式声明下限时的兜底最小宽度，防止列被压成一条缝。 */
export const DEFAULT_MIN_WIDTH = 56

/** 每列的输入描述。 */
export interface ColumnWidthInput {
  /** 内容自然宽度（无约束时这列「想要」多宽），单位 px。 */
  natural: number
  /**
   * 下限：列不会被压得比它更窄。缺省用 DEFAULT_MIN_WIDTH。
   * 调用方通过在表头写 min-w-* 表达「这列窄到多少就读不下去了」。
   */
  min?: number
  /** 上限：列不会被撑得比它更宽，超出上限的内容应换行。缺省视为无上限。 */
  max?: number
  /** 用户拖拽钉住的宽度：一旦有值，自动计算不再覆盖它（仍受下限与拖拽上限约束）。 */
  pinned?: number
}

export interface ComputeOptions {
  /** 容器可用宽度（px）。 */
  containerWidth: number
  /** 是否把列宽铺满容器，默认 true。不铺满时表格只占内容所需宽度。 */
  fill?: boolean
  /** 拖拽／钉住能达到的最大宽度，默认 2000，避免拖出荒谬的宽表。 */
  dragCeiling?: number
}

function isNum(v: number | undefined): v is number {
  return typeof v === 'number' && Number.isFinite(v)
}

/**
 * computeColumnWidths 计算最终列宽数组。
 *
 * 阶段：
 *   1. 定基宽：钉住的列用钉住值；其余列 = clamp(自然宽度, 下限, 上限)。
 *   2. 总和小于容器且开启铺满：富余优先给「没有上限」的列按比例吸收。上限是约束而非
 *      目标，声明了 max-w 的列该停在内容宽度上，不该被撑到上限。
 *   3. 若所有列都有上限（或被钉住），再依次填到各自上限；仍有剩余则按比例摊开。
 *      这些动作只会让列变宽，不会隐藏内容。
 *   4. 总和大于容器：按「宽度从大到小」逐列压缩到刚好放得下，各列以自身下限为底。
 *      压到所有列都触底仍放不下，就让容器横向滚动 —— 宁可滚动，也不裁掉内容。
 *   5. 取整并配平取整误差：fixed 布局下总和略小于容器时，浏览器会把列宽按比例
 *      二次放大（实测 899/900 就把 180px 的列拉成 231px），从而破坏拖拽结果。
 */
export function computeColumnWidths(
  columns: ColumnWidthInput[],
  options: ComputeOptions,
): number[] {
  const n = columns.length
  if (n === 0) return []

  const containerWidth = isNum(options.containerWidth) ? Math.max(0, options.containerWidth) : 0
  const fill = options.fill ?? true
  const dragCeiling = options.dragCeiling ?? 2000

  const hasMax = (c: ColumnWidthInput) => isNum(c.max) && c.max > 0
  const minOf = (c: ColumnWidthInput) => (isNum(c.min) ? Math.max(0, c.min) : DEFAULT_MIN_WIDTH)
  const naturalOf = (c: ColumnWidthInput) =>
    isNum(c.natural) && c.natural > 0 ? Math.ceil(c.natural) : minOf(c)
  const maxOf = (c: ColumnWidthInput) =>
    hasMax(c) ? Math.max(minOf(c), c.max as number) : Number.POSITIVE_INFINITY
  const pinnedOf = (c: ColumnWidthInput) =>
    isNum(c.pinned) ? Math.max(minOf(c), Math.min(c.pinned, dragCeiling)) : null

  // 阶段 1：定基宽
  const out: number[] = []
  for (const c of columns) {
    const pin = pinnedOf(c)
    if (pin !== null) {
      out.push(pin)
      continue
    }
    out.push(Math.min(Math.max(naturalOf(c), minOf(c)), maxOf(c)))
  }

  const sum = () => out.reduce((a, b) => a + b, 0)
  const unpinned = (i: number) => pinnedOf(columns[i]) === null

  if (fill && sum() < containerWidth) {
    let surplus = containerWidth - sum()

    // 阶段 2：富余优先给「没有上限」的列（按当前宽度比例分配）。
    const flexible: Array<{ i: number; w: number }> = []
    for (let i = 0; i < n; i++) {
      if (unpinned(i) && !hasMax(columns[i])) flexible.push({ i, w: out[i] })
    }
    const flexibleTotal = flexible.reduce((a, f) => a + f.w, 0)
    if (flexible.length && flexibleTotal > 0) {
      for (const f of flexible) out[f.i] += (surplus * f.w) / flexibleTotal
      surplus = 0
    }

    // 阶段 3：所有列都有上限（或被钉住）的退化场景。多轮分配，每轮只填还有余量的列，
    // 保证上限严格成立、不被单轮的比例分配冲过头。
    for (let pass = 0; pass < 6 && surplus > 0.5; pass++) {
      const growable: Array<{ i: number; room: number; w: number }> = []
      for (let i = 0; i < n; i++) {
        if (!unpinned(i)) continue
        const room = maxOf(columns[i]) - out[i]
        if (room > 0.5) growable.push({ i, room, w: out[i] })
      }
      if (!growable.length) break
      const weightTotal = growable.reduce((a, g) => a + g.w, 0)
      let moved = 0
      for (const g of growable) {
        const share = weightTotal > 0 ? (surplus * g.w) / weightTotal : surplus / growable.length
        const add = Math.min(share, g.room)
        if (add <= 0.5) continue
        out[g.i] += add
        moved += add
      }
      if (moved <= 0.5) break
      surplus -= moved
    }

    // 连有上限的列都顶满了：余量按比例摊到所有未钉住的列（只变宽，不隐藏内容）。
    if (surplus > 0.5) {
      const spread: Array<{ i: number; w: number }> = []
      for (let i = 0; i < n; i++) if (unpinned(i)) spread.push({ i, w: out[i] })
      const total = spread.reduce((a, s) => a + s.w, 0)
      if (total > 0) for (const s of spread) out[s.i] += (surplus * s.w) / total
    }
  } else if (sum() > containerWidth) {
    // 阶段 4：按「宽 → 窄」逐列压缩，直到放得下或全部触底。
    //
    // 为什么贪心压最宽的列，而不是按比例平摊：超宽几乎总是来自某一列的长文本
    // （实测 Skills 页「描述」列被 495 字内容撑到 4994px，整表爆到 5703px）。
    // 平摊会把所有列一起挤窄、每列都换行，反而更难读；只动最宽的那一列，
    // 其余列保持内容宽度，视觉上最稳。
    //
    // 压缩是安全的：组件会给「最终宽度 < 自然宽度」的列开启换行（white-space:normal），
    // 所以文字只是换行，不会被裁掉。
    let overflow = sum() - containerWidth
    const byWidthDesc = out
      .map((w, i) => ({ i, w }))
      .filter(({ i }) => unpinned(i))
      .sort((a, b) => b.w - a.w)
      .map(({ i }) => i)
    for (const i of byWidthDesc) {
      if (overflow <= 0.5) break
      const room = out[i] - minOf(columns[i])
      if (room <= 0.5) continue
      const take = Math.min(overflow, room)
      out[i] -= take
      overflow -= take
    }
  }

  // 阶段 5：取整 + 配平
  const rounded = out.map((w, i) => {
    const c = columns[i]
    // 保持在自然宽度上的列向上取整：少 1px 就会把最后一个字裁掉（真实实现里踩到过）。
    return w >= naturalOf(c) ? Math.max(0, Math.ceil(w)) : Math.max(0, Math.round(w))
  })
  if (fill) {
    const roundedTotal = rounded.reduce((a, b) => a + b, 0)
    const drift = Math.round(containerWidth) - roundedTotal
    // 只吸收「取整级别」的误差（每列最多 0.5px，合计 n/2 向上取整）。
    // 表格本就该横向滚动时 drift 会很大 —— 那种情况绝不能配平，否则会压扁某一列、
    // 把内容藏起来，正是本组件要消灭的问题。
    const driftLimit = Math.ceil(n / 2) + 1
    if (drift !== 0 && Math.abs(drift) <= driftLimit) {
      const target = findDriftTarget(columns, out, rounded, drift, pinnedOf, minOf, maxOf)
      if (target >= 0) rounded[target] = Math.max(0, rounded[target] + drift)
    }
  }
  return rounded
}

/** 挑一个适合吸收取整误差的列：未钉住、且调整后仍落在 [下限, 上限] 内。 */
function findDriftTarget(
  columns: ColumnWidthInput[],
  raw: number[],
  rounded: number[],
  drift: number,
  pinnedOf: (c: ColumnWidthInput) => number | null,
  minOf: (c: ColumnWidthInput) => number,
  maxOf: (c: ColumnWidthInput) => number,
): number {
  let best = -1
  let bestDistance = -1
  for (let i = 0; i < columns.length; i++) {
    if (pinnedOf(columns[i]) !== null) continue
    const next = rounded[i] + drift
    if (next < minOf(columns[i]) - 0.5 || next > maxOf(columns[i]) + 0.5) continue
    // 离 0.5 最近的小数尾巴来自取整，优先由它吸收误差。
    const frac = Math.abs(raw[i] - Math.floor(raw[i]) - 0.5)
    const distance = 0.5 - frac
    if (distance > bestDistance) {
      bestDistance = distance
      best = i
    }
  }
  return best
}

/**
 * parseCssLength 把 CSS 长度字面量读成像素数（用于解析表头上 Tailwind 宽度类
 * 编译后的 computed max-width / min-width）。
 * 读不出数值（none / auto / 百分比 / 空值 / 0）时返回 undefined，交调用方兜底。
 */
export function parseCssLength(value: string | undefined | null): number | undefined {
  if (!value) return undefined
  const trimmed = value.trim()
  if (!trimmed || trimmed === 'none' || trimmed === 'auto') return undefined
  const px = /^(-?[0-9]+(?:[.][0-9]+)?)px$/.exec(trimmed)
  if (px) {
    const v = Number(px[1])
    return Number.isFinite(v) && v > 0 ? v : undefined
  }
  const rem = /^(-?[0-9]+(?:[.][0-9]+)?)rem$/.exec(trimmed)
  if (rem) {
    const v = Number(rem[1]) * 16
    return Number.isFinite(v) && v > 0 ? v : undefined
  }
  return undefined
}
