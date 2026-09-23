<script setup lang="ts">
/**
 * AxTable — 自动列宽的表格容器（在 shadcn Table 之上做一层封装）
 *
 * 你只需要告诉它「哪一列最多多宽」，其余交给内容自己决定：
 *   - 列宽跟着内容走：内容长了列自动变宽，不会像写死像素那样被切掉；
 *   - 在某列表头写 max-w-[320px]，超过的内容自动换行显示（而不是被隐藏）；
 *   - 表头列边界可以左右拖拽调宽（边界平时透明，不干扰阅读；鼠标移上去淡淡浮现，
 *     拖拽时高亮成选中色）；拖过的列被「钉住」，窗口缩放不再改变它；
 *     双击边界（或按 Home）恢复该列自动宽度；
 *     方向键也能微调列宽。拖拽是默认能力，不需要（也没有）开关 —— 项目里所有
 *     通过 AxTable 渲染的表格都自动具备。
 *
 * 用法：把原来的 <Table> 换成 <AxTable>，其余结构原样不动。
 *
 *   <AxTable>
 *     <TableHeader>
 *       <TableRow>
 *         <TableHead class="w-[150px]">时间</TableHead>
 *         <TableHead class="max-w-[320px]">错误摘要</TableHead>
 *       </TableRow>
 *     </TableHeader>
 *     <TableBody> ... </TableBody>
 *   </AxTable>
 *
 * 为什么必须封装一层（以下均为真实 Chrome 实测结论）：
 *   1. table-layout:auto + width:100% 时，单元格上的 max-width 只是「偏好」，
 *      列会被富余空间撑大 —— 实测 max-width:320px 的列被撑到 696px，上限形同虚设。
 *   2. shadcn 的 TableHead/TableCell 自带 whitespace-nowrap，长内容只会被切掉。
 *   3. 要让上限真正生效，必须用 table-layout:fixed 配明确列宽，而列宽得先量出
 *      「内容自然宽度」再算出来 —— 这就是本组件做的事。
 *
 * 实现上刻意不改动插槽里的任何节点：列宽用一段按列序号生成的 CSS 施加，拖拽手柄是
 * 覆盖在表头上的一层透明层。这样调用方的模板与 Vue 的渲染都不受影响。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { computeColumnWidths, DEFAULT_MIN_WIDTH, parseCssLength } from '@/lib/tableWidth'
import AxTableResizers from '@/components/ui/AxTableResizers.vue'

const props = withDefaults(
  defineProps<{
    /** 是否把列宽铺满容器宽度，默认 true；false = 表格只占内容需要的宽度。 */
    fill?: boolean
    /** 列宽下限（px），防止内容很短的列被压得看不见；表头上的 min-w-* 优先。 */
    minColumnWidth?: number
  }>(),
  { fill: true, minColumnWidth: DEFAULT_MIN_WIDTH },
)

/** 实例唯一 id：把生成的列宽 CSS 限定在本实例，避免多个表格互相干扰。 */
const uid = `axt-${Math.random().toString(36).slice(2, 10)}`
const rootEl = ref<HTMLElement | null>(null)

interface ColumnSpec {
  /** 上限（px）。来自表头的 max-w-* 类；读不到 = 无上限（内容不换行）。 */
  max?: number
  /** 下限（px）。来自表头的 min-w-* 类，读不到用组件默认值。 */
  min: number
  /**
   * 调用方是否希望这列换行 —— 即「在表头写了 max-w-*」。
   * 写了 = 作者接受这列换行（超长内容换行显示）；
   * 没写 = 保持 shadcn 默认的不换行，被压窄时用省略号收尾，
   * 尊重调用方自己的 truncate / 换行开关（如 Skills 页的描述列有「换行」切换）。
   */
  wrappable: boolean
}

const columns = ref<ColumnSpec[]>([])
const natural = ref<number[]>([])
const widths = ref<number[]>([])
const dividers = ref<number[]>([])
/** 表头在组件根部内的纵向偏移：覆盖层要盖在表头上，不能假设表格紧贴根部。 */
const overlayTop = ref(0)
const containerWidth = ref(0)
const headerHeight = ref(40)
const scrollLeft = ref(0)
const pinned = ref<Record<number, number>>({})
const dragging = ref<number | null>(null)

function tableEl(): HTMLTableElement | null {
  const root = rootEl.value
  if (!root) return null
  // 取「最外层」的表：插槽里可能嵌套表格（展开行里再放一张明细表），
  // 而本组件只管外层这一张。判定方式 = 没有任何 table 祖先，即根下最外层的那个 table。
  const tables = Array.from(root.querySelectorAll('table'))
  return tables.find((t) => !t.parentElement?.closest('table')) ?? tables[0] ?? null
}

/** shadcn Table 外层那个横向滚动容器。 */
function scrollerEl(): HTMLElement | null {
  return tableEl()?.parentElement ?? null
}

/**
 * 测量每列「内容自然宽度」。
 *
 * 做法：把表格克隆一份放到屏幕外，去掉列宽约束并强制不换行，量每一列表头格的宽度。
 * 克隆挂在 document.body 下，因此本组件注入的「按列序号设宽」CSS 不会命中克隆体，
 * 量到的才是内容真正需要的宽度。
 *
 * 量的是「每列里最宽的那个单元格」：克隆体保持不换行，表格宽度为 max-content，
 * 于是浏览器把每列撑到该列最宽内容的宽度，读任意一行的表头格即可得到该列需求。
 *
 * 列宽约束（max-w / min-w）也在这里读：克隆体在组件作用域之外，调用方写的
 * Tailwind 类在克隆体上仍然是原样生效的，而组件自己注入的 max-width:none 不会命中它。
 * 必须这样读 —— 组件为了不干扰拖拽会清掉表格上的 max-width，若改从「当前渲染的
 * 表头」读，第一次渲染后约束就丢掉了。
 */
function measureColumns(): { natural: number[]; specs: ColumnSpec[] } {
  const table = tableEl()
  if (!table) return { natural: [], specs: [] }
  // 打标记：后续所有 CSS 都只针对这张主表，避免误伤嵌套的明细表。
  table.setAttribute('data-ax-primary', uid)
  const holder = document.createElement('div')
  // 屏幕外容器：不可见、不占布局，但仍参与排版计算，因此能量出真实宽度。
  holder.setAttribute('aria-hidden', 'true')
  holder.style.cssText =
    'position:absolute;left:-99999px;top:0;width:max-content;height:0;overflow:hidden;visibility:hidden;pointer-events:none;'
  const clone = table.cloneNode(true) as HTMLTableElement
  // 先摘掉克隆体里的「嵌套表」（展开行里的明细表）：它们不是主表的列内容，
  // 却会把所在单元格的自然宽度撑得极大 —— 实测 Skills 页「描述」列因此被撑到
  // 4994px，整张表爆到 5703px。列宽只该由主表自己的内容决定。
  clone.querySelectorAll('table').forEach((t) => t.remove())
  // 必须挂在 body 下、且在清掉约束之前读：克隆体在组件作用域之外，
  // 调用方写的 Tailwind 类在这里原样生效，读到的才是真实上限。
  holder.appendChild(clone)
  document.body.appendChild(holder)
  // 只取主表自己的表头行：用 tHead.rows[0]，不用 querySelectorAll，
  // 否则嵌套明细表的表头也会被一起量进来。
  const heads = Array.from(clone.tHead?.rows[0]?.cells ?? [])
  const specs: ColumnSpec[] = heads.map((th) => {
    const style = getComputedStyle(th)
    const max = parseCssLength(style.maxWidth)
    return {
      max,
      min: parseCssLength(style.minWidth) ?? props.minColumnWidth,
      wrappable: max !== undefined,
    }
  })
  // 再清掉宽度约束、强制不换行，量「内容真正需要的宽度」。
  clone.style.tableLayout = 'auto'
  clone.style.width = 'max-content'
  clone.style.minWidth = '0'
  clone.querySelectorAll('th, td').forEach((cell) => {
    const el = cell as HTMLElement
    // 测量时必须禁止换行：换行宽度是「被压过」的宽度，量不出内容的真实需求。
    el.style.whiteSpace = 'nowrap'
    el.style.overflow = 'visible'
    el.style.textOverflow = 'clip'
    el.style.width = 'auto'
    el.style.minWidth = '0'
    el.style.maxWidth = 'none'
  })
  const natural = heads.map((th) => {
    const w = th.getBoundingClientRect().width
    return w > 0 ? w : props.minColumnWidth
  })
  holder.remove()
  return { natural, specs }
}

/** 按当前约束算出最终列宽（纯函数，见 lib/tableWidth.ts）。 */
function recompute() {
  if (!columns.value.length) {
    widths.value = []
    return
  }
  widths.value = computeColumnWidths(
    columns.value.map((spec, i) => ({
      natural: natural.value[i] ?? spec.min,
      min: spec.min,
      max: spec.max,
      pinned: pinned.value[i],
    })),
    { containerWidth: containerWidth.value, fill: props.fill },
  )
}

/**
 * 从真实 DOM 量分隔条位置（而不是累加计算值）。
 *
 * 表格有边框（border-collapse 下相邻边框合并），累加计算值会和实际渲染差几个像素；
 * 直接读表头单元格的右边缘，手柄就能严格落在列边界上。坐标换算到「内容坐标系」，
 * 再由覆盖层按其横向滚动量平移，滚动时手柄跟着列一起走。
 */
function measureDividers() {
  const table = tableEl()
  const scroller = scrollerEl()
  const root = rootEl.value
  if (!table || !scroller || !root) {
    dividers.value = []
    return
  }
  // 只取主表自己的表头单元格（tHead.rows[0]），嵌套明细表的表头不参与。
  const heads = Array.from(table.tHead?.rows[0]?.cells ?? [])
  // 相对「组件根部」测量：根部才是覆盖层的定位基准。通常与滚动容器重合，
  // 但调用方若在 AxTable 与 Table 之间垫了内边距，按根部量才不会错位。
  const rootRect = root.getBoundingClientRect()
  const base = rootRect.left - scroller.scrollLeft
  dividers.value = heads.slice(0, -1).map((th) => th.getBoundingClientRect().right - base)
  const head = table.querySelector('thead')
  if (head) {
    const rect = head.getBoundingClientRect()
    if (rect.height > 0) headerHeight.value = rect.height
    overlayTop.value = Math.max(0, rect.top - rootRect.top)
  }
}

/** 重新测量全部状态：容器宽度 → 列约束 → 内容自然宽度 → 最终列宽。 */
async function remeasure() {
  await nextTick()
  const scroller = scrollerEl()
  if (!scroller) return
  containerWidth.value = scroller.clientWidth
  const measured = measureColumns()
  columns.value = measured.specs
  natural.value = measured.natural
  recompute()
  await nextTick()
  measureDividers()
}

/**
 * 动态列宽 CSS。
 *
 * 只作用于本实例（#uid 作用域），只写在表头格上：fixed 布局下列宽由第一行单元格
 * 决定，因此不必碰数据单元格 —— 顺带避开了折叠详情行 colspan 造成的错位。
 *
 * 表格宽度用「列宽之和」而不是 100%：fixed 布局下若列宽之和与表格宽度不等，
 * 浏览器会按比例二次缩放列宽（实测 899/900 就把 180px 的列拉成 231px），
 * 那会破坏拖拽结果。写成精确的像素和，列宽才能被严格兑现；超宽时由外层容器滚动。
 */
/**
 * 按给定列宽拼出整份样式表。抽成函数是为了让拖拽路径能直接调用它写样式，
 * 而不必经过响应式状态 —— 那正是拖拽卡顿的根源（详见 onDividerMove 的注释）。
 */
function buildTableCss(w: number[]): string {
  const n = w.length
  if (!n) return ''
  const scope = `#${uid}`
  const total = w.reduce((a, b) => a + b, 0)
  const rules = [
    // 选择器统一用 [data-ax-primary]：只命中主表，嵌套的明细表完全不受影响。
    `${scope} table[data-ax-primary] { table-layout: fixed; width: ${total}px; }`,
    // 调用方常写 <colgroup><col class="w-40"> 这类固定列宽。fixed 布局下 col 的宽度
    // 会压过我们注入的 width（col 优先级更高），导致拖拽结果被静默覆盖。
    // 这里把它们清掉：列宽统一由本组件按内容算出来，调用方只保留「上限 / 下限」语义。
    `${scope} table[data-ax-primary] col { width: auto; }`,
    // 固定布局下单元格默认会溢出到隔壁列，统一裁掉，避免长内容压住相邻列。
    //
    // max-width / min-width 必须清掉：调用方写在表头上的 max-w-[320px] 是给「自动宽度
    // 算法」读的语义，算法已经把它折算进列宽了。若把它留在单元格上，fixed 布局会让它
    // 直接压过我们注入的 width —— 实测拖拽 +150px 只生效 +39px，超出的部分被浏览器
    // 按比例摊回其它列，拖拽手感完全失真。
    `${scope} table[data-ax-primary] > thead > tr > th, ${scope} table[data-ax-primary] > tbody > tr > td { overflow: hidden; max-width: none; min-width: 0; }`,
  ]
  // 拖拽竖线与手柄的样式全部写在这里，而不是用 Tailwind 类：
  // 项目里没用过的工具类（如 bg-primary/60）不会被 Tailwind 扫描器生成，
  // 页面上就是一条透明线（实测踩过）。用主题变量则永远可用。
  rules.push(
    // 根部兜底定位：即使调用方环境缺 relative/overflow 工具类，覆盖层也能锚对。
    `${scope} { position: relative; overflow: hidden; }`,
    // 覆盖层：只盖表头区间，本身不吃指针事件（手柄单独恢复）。
    `${scope} .axt-overlay { position: absolute; left: 0; z-index: 10; pointer-events: none; }`,
    // 手柄：比竖线更宽的命中区（10px），拖起来不费劲。
    `${scope} .axt-handle {`,
    `  position: absolute; top: 0; height: 100%; width: 10px;`,
    `  transform: translateX(-50%); cursor: col-resize; user-select: none;`,
    `  pointer-events: auto;`,
    `}`,
    // 列边界指示线：平时【完全透明】，不打扰阅读；命中区由 .axt-handle 承担（10px），
    // 所以线即使看不见也照样能拖。只有你要用它的时候它才现身：
    //   hover  → 淡淡浮现，方便瞄准（移开就消失）
    //   拖拽中 → 选中色全亮并加粗，明确标出正在调的是哪条边界
    `${scope} .axt-line {`,
    `  position: absolute; top: 4px; bottom: 4px; left: 50%; width: 2px;`,
    `  transform: translateX(-50%); border-radius: 9999px;`,
    `  background: var(--primary); opacity: 0;`,
    `  transition: opacity 0.12s, width 0.12s;`,
    `}`,
    `${scope} .axt-handle:hover .axt-line { opacity: 0.35; }`,
    `${scope} .axt-handle.is-dragging .axt-line { opacity: 1; width: 3px; }`,
    // 键盘可达：聚焦时就让线现身，并给个可见描边。
    `${scope} .axt-handle:focus-visible .axt-line { opacity: 1; }`,
    `${scope} .axt-handle:focus-visible { outline: 1px solid var(--primary); outline-offset: -1px; }`,
  )
  w.forEach((width, i) => {
    const col = `> tr > th:nth-child(${i + 1})`
    rules.push(`${scope} table[data-ax-primary] > thead ${col} { width: ${width}px; }`)
    // 什么时候允许换行：只要「最终列宽 < 内容自然宽度」，就说明这列被压过
    // （可能是调用方写了 max-w 上限，也可能是内容太多、算法压缩了最宽的列）。
    // 压过就必须允许换行 —— 否则 shadcn 自带的 whitespace-nowrap 会把文字裁掉。
    const naturalW = natural.value[i] ?? 0
    if (naturalW > 0 && width < naturalW - 0.5) {
      // 被压过的列必须「要么换行、要么省略号」，二选一都行，但绝不能直接裁字。
      // 调用方在表头写了 max-w-* → 说明作者接受这列换行，给它换行；
      // 没写 → 尊重调用方自己的排版意图（很多列本来就带 truncate 或自管换行），
      // 这里只补省略号兜底，避免文字被生生切断。
      if (columns.value[i]?.wrappable) {
        rules.push(
          `${scope} table[data-ax-primary] > thead ${col}, ${scope} table[data-ax-primary] > tbody > tr > td:nth-child(${i + 1}) { white-space: normal; overflow-wrap: anywhere; }`,
        )
      } else {
        rules.push(
          `${scope} table[data-ax-primary] > thead ${col}, ${scope} table[data-ax-primary] > tbody > tr > td:nth-child(${i + 1}) { overflow: hidden; text-overflow: ellipsis; }`,
        )
      }
    }
  })
  return rules.join('\n')
}

const tableCss = computed(() => buildTableCss(widths.value))
/** 把动态 CSS 落到一个 <style> 标签（用 DOM 构造，避免 SFC 把模板里的 style 当样式块提取）。 */
let styleEl: HTMLStyleElement | null = null
function syncStyle() {
  if (!styleEl) {
    styleEl = document.createElement('style')
    styleEl.setAttribute('data-ax-table', uid)
    document.head.appendChild(styleEl)
  }
  styleEl.textContent = tableCss.value
}
watch(tableCss, syncStyle)

// 列宽变化后重算分隔条位置。拖拽期间跳过：那时位置由「基准 + 位移」直接算出，
// 不读 DOM —— 原因见 onDividerMove 上的性能注释。
watch(widths, () => {
  if (dragging.value !== null) return
  void nextTick(measureDividers)
})

// ===== 拖拽调列宽 =====//

// 性能要求：拖拽必须跟手。这里刻意**在拖拽过程中不读任何 DOM 几何**，
// 原因是一组实测数据（100 行 × 8 列，真实 Chrome）：
//   - 只写列宽 CSS（不读几何）：0.05ms
//   - 写完 CSS 再读一次表格几何：12.25ms  ← 相差 240 倍
// 多出来的时间全是「强制同步布局」：浏览器为了立刻回答几何查询，必须先把刚改的样式
// 对整张表重排一次。pointermove 每帧触发多次，于是每次移动都重排好几遍，帧间隔被顶到
// 66~87ms（实测 longtask 60~87ms），手感就是「卡、不跟手」。
//
// 所以拖拽期间：
//   1. 只更新列宽（纯 JS 计算 + 写样式），不读几何；
//   2. 分隔条位置不重测，按「拖拽前位置 + 鼠标位移」直接算 —— 一次只移动一条边界，
//      其余边界分毫不动，重测毫无必要；
//   3. pointermove 用 rAF 合并，每帧最多处理一次（鼠标事件常比刷新更密）。
// 松手后再实测一次，纠正累加与真实渲染之间 1px 级的边框误差。
let dragState: {
  index: number
  startX: number
  startWidth: number
  /** 拖拽开始时各分隔条的位置（DOM 实测值），拖拽中据此平移。 */
  // 拖拽期间的工作副本（都**不是**响应式数据）：每帧只改它们，松手才提交。
  // 碰 widths / dividers 会触发 AxTable 重渲染，而它的 <slot/> 承载着整张表 ——
  // 实测那样会让 100 行 × 8 列被整表重建，每次鼠标移动触发 100 次行渲染。
  columns: number[]
  positions: number[]
} | null = null
/** pointermove 的最新坐标，交给 rAF 每帧处理一次。 */
let pendingX: number | null = null
let dragRafId = 0

function onDividerDown(index: number, event: PointerEvent) {
  event.preventDefault()
  event.stopPropagation()
  dragState = {
    index,
    startX: event.clientX,
    startWidth: widths.value[index] ?? columns.value[index]?.min ?? props.minColumnWidth,
    // 工作副本：拖拽中只改这两份非响应式数据，不碰 widths / dividers。
    columns: widths.value.slice(),
    positions: dividers.value.slice(),
  }
  dragging.value = index
  window.addEventListener('pointermove', onDividerMove)
  window.addEventListener('pointerup', onDividerUp)
  window.addEventListener('pointercancel', onDividerUp)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
}

function onDividerMove(event: PointerEvent) {
  if (!dragState) return
  pendingX = event.clientX
  // 合并到下一帧：鼠标事件常比屏幕刷新更密，逐条处理会白做很多次重排。
  if (dragRafId) return
  dragRafId = requestAnimationFrame(applyDrag)
}

/**
 * 把这一帧的拖拽位移落到界面上。**不碰响应式状态、不读 DOM。**
 *
 * 为什么不更新响应式状态（这是最关键的坑，实测卡了很久）：
 *   AxTable 的 <slot/> 承载整张表。只要 AxTable 重渲染，Vue 就会重新执行 slot，
 *   于是 100 行 × 8 列约 800 个单元格被整表重建 —— 实测「每次鼠标移动触发 100 次
 *   行渲染」（正好等于行数），帧间隔被顶到 66~87ms。
 *   把分隔条拆成子组件也没用：拖拽状态本身就在 AxTable 里，它一变，AxTable 照样重渲染。
 *
 * 所以拖拽期间走「命令式」路径：
 *   - 列宽只写进 dragState（普通变量，非响应式）；
 *   - 直接把新 CSS 写进 <style>，浏览器只做一次样式重算，Vue 完全不参与；
 *   - 分隔条位置直接改对应 DOM 节点的 left（只有这条边界及其右侧邻居要动）。
 * 松手时再一次性提交进响应式状态，回到正常数据流。
 */
function applyDrag() {
  dragRafId = 0
  if (!dragState || pendingX === null) return
  const { index, startX, startWidth } = dragState
  const colWidths = dragState.columns
  const positions = dragState.positions
  const min = columns.value[index]?.min ?? props.minColumnWidth
  // 拖的是这一列的右边界，所以宽度增量 = 鼠标水平位移。
  const next = Math.max(min, startWidth + (pendingX - startX))
  if (next === colWidths[index]) return
  const delta = next - colWidths[index]
  colWidths[index] = next

  // 1) 只写样式表：列宽与表格总宽一起变，浏览器据此重排一次。
  if (styleEl) styleEl.textContent = buildTableCss(colWidths)

  // 2) 只有「这条边界」及其右侧边界平移；左边的分毫不动。
  const handles = rootEl.value?.querySelectorAll('[data-ax-overlay] .axt-handle')
  if (handles) {
    for (let i = index; i < handles.length; i++) {
      const x = positions[i] + delta
      positions[i] = x
      ;(handles[i] as HTMLElement).style.left = `${x}px`
    }
  }
}

function onDividerUp() {
  const state = dragState
  dragState = null
  pendingX = null
  if (dragRafId) {
    cancelAnimationFrame(dragRafId)
    dragRafId = 0
  }
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
  window.removeEventListener('pointermove', onDividerMove)
  window.removeEventListener('pointerup', onDividerUp)
  window.removeEventListener('pointercancel', onDividerUp)
  if (!state) return
  // 松手：把拖拽结果一次性提交进响应式状态，回到正常数据流。
  // 提交顺序：先写 pinned，再清 dragging —— 这样中间那次 recompute 产生的重渲染
  // 只有一次，而且已经带着正确的钉住值。
  pinned.value = { ...pinned.value, [state.index]: Math.round(state.columns[state.index]) }
  recompute()
  dragging.value = null
  // 以实测为准校正一次：累加值可能与真实渲染差 1px（边框合并）。
  void nextTick(measureDividers)
}

/** 键盘调宽：分隔条可聚焦，方向键微调（按住 Shift 步长更大），Home 恢复自动宽度。 */
function onDividerKey(index: number, event: KeyboardEvent) {
  const min = columns.value[index]?.min ?? props.minColumnWidth
  const current = pinned.value[index] ?? widths.value[index] ?? min
  const step = event.shiftKey ? 64 : 16
  if (event.key === 'Home' || event.key === 'Enter' || event.key === ' ') {
    event.preventDefault()
    resetColumn(index)
    return
  }
  let next: number | null = null
  if (event.key === 'ArrowLeft') next = Math.max(min, current - step)
  else if (event.key === 'ArrowRight') next = current + step
  if (next === null) return
  event.preventDefault()
  pinned.value = { ...pinned.value, [index]: next }
  recompute()
}

/** 恢复某一列的自动宽度。 */
function resetColumn(index: number) {
  if (pinned.value[index] === undefined) return
  const copy = { ...pinned.value }
  delete copy[index]
  pinned.value = copy
  recompute()
}

/** 双击分隔条 = 恢复该列自动宽度。 */
function onDividerDoubleClick(index: number) {
  resetColumn(index)
}

/** 是否存在被拖拽钉住的列。 */
const hasPinned = computed(() => Object.keys(pinned.value).length > 0)

/** 一键恢复所有列的自动宽度。 */
function resetAll() {
  if (!hasPinned.value) return
  pinned.value = {}
  recompute()
}

// ===== 尺寸 / 内容监听 =====//
let observer: ResizeObserver | null = null
let mutator: MutationObserver | null = null
let rafId = 0

function onScroll() {
  scrollLeft.value = scrollerEl()?.scrollLeft ?? 0
}

/** 合并高频触发（内容频繁变动）到下一帧，避免重复测量。 */
function scheduleRemeasure() {
  if (rafId) cancelAnimationFrame(rafId)
  rafId = requestAnimationFrame(() => {
    rafId = 0
    void remeasure()
  })
}

onMounted(() => {
  void remeasure()
  // Web Font 落地会改变文字宽度，就绪后补测一次。
  document.fonts?.ready.then(() => void remeasure()).catch(() => {})

  // 容器宽度变化：只需重算铺满分配，内容自然宽度没变，不必重新克隆测量。
  observer = new ResizeObserver(() => {
    const scroller = scrollerEl()
    if (!scroller) return
    const w = scroller.clientWidth
    if (w === containerWidth.value) return
    containerWidth.value = w
    recompute()
  })
  const scroller = scrollerEl()
  if (scroller) {
    observer.observe(scroller)
    scroller.addEventListener('scroll', onScroll, { passive: true })
  }

  // 单元格文字变化（异步加载、内容更新）也要重测 —— 否则列宽会停在旧内容上。
  // 只观察 tbody：本组件通过 <head> 里的样式表设宽，不写单元格属性，不会自触发。
  const body = tableEl()?.querySelector('tbody')
  if (body) {
    mutator = new MutationObserver(() => scheduleRemeasure())
    mutator.observe(body, { childList: true, subtree: true, characterData: true })
  }
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  mutator?.disconnect()
  mutator = null
  if (rafId) cancelAnimationFrame(rafId)
  scrollerEl()?.removeEventListener('scroll', onScroll)
  onDividerUp()
  styleEl?.remove()
  styleEl = null
})

defineExpose({ remeasure, resetAll, hasPinned })
</script>

<template>
  <div :id="uid" ref="rootEl" class="ax-table relative overflow-hidden">
    <!-- 表格本体与滚动容器由调用方提供的 shadcn Table 结构承担；这里只做两件事：
         注入列宽 CSS（见 tableCss），以及在表头上盖一层拖拽手柄。 -->
    <slot />

    <!-- 拖拽手柄覆盖层：盖在表头上、随横向滚动一起平移，与列边界严格对齐。
         拆成独立子组件是性能要求：拖拽状态只驱动手柄重渲染，绝不带着整张表重渲染。 -->
    <AxTableResizers
      v-if="dividers.length"
      :dividers="dividers"
      :header-height="headerHeight"
      :overlay-top="overlayTop"
      :scroll-left="scrollLeft"
      :dragging-index="dragging"
      @drag-start="onDividerDown"
      @drag-key="onDividerKey"
      @reset="onDividerDoubleClick"
    />
  </div>
</template>
