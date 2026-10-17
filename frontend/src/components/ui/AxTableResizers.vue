<script setup lang="ts">
/**
 * AxTableResizers — AxTable 的列宽拖拽手柄层（内部子组件）
 *
 * 为什么必须单独拆一个组件（性能关键，实测数据说话）：
 *   拖拽时每帧都要更新分隔条位置。如果手柄和 <slot/> 待在同一个组件里，
 *   这份高频状态就会让「承载整张表」的组件每帧重渲染 —— 于是 100 行 × 8 列
 *   约 800 个单元格在拖动过程中被反复重建。实测：每次鼠标移动触发 109 次行渲染，
 *   帧间隔被顶到 66~87ms，手感就是「卡、不跟手」。
 *
 *   把手柄拆成独立子组件后，高频状态只驱动这几个手柄重渲染，表格本体一次都不动。
 *
 * 样式（.axt-overlay / .axt-band）由 AxTable 注入的样式表提供，
 * 选择器以 AxTable 根 id 作用域限定，本组件渲染在其内部，因此照常生效。
 */
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'

const props = defineProps<{
  /** 每条列边界的水平位置（px，相对表格内容坐标系）。 */
  dividers: number[]
  /** 覆盖层高度 = 表头高度。 */
  headerHeight: number
  /** 覆盖层距组件顶部的偏移。 */
  overlayTop: number
  /** 当前横向滚动量，用于让手柄跟着表格一起平移。 */
  scrollLeft: number
  /** 正在拖拽的边界序号；null = 没有在拖。 */
  draggingIndex: number | null
}>()

const emit = defineEmits<{
  dragStart: [index: number, event: PointerEvent]
  dragKey: [index: number, event: KeyboardEvent]
  reset: [index: number]
}>()

/**
 * 列高亮（方案 C）：
 *   鼠标在表内移动时，高亮「鼠标所在的那一列」——表头上的一个矩形色块
 *   （即该列自己的宽度）。在表头色块内任意位置按下即可开始拖拽该列右边界，
 *   拖拽时色块宽度实时变化，所见即所得。
 */
const hoverCol = ref<number | null>(null)
/** 鼠标是否在表头色块区内（列内行 hover 也会让表头列保持高亮）。 */
const inHeader = ref(false)
/** 本组件的覆盖层 / 其父容器（AxTable 根）。 */
const overlayEl = ref<HTMLElement | null>(null)
/** 色块显示的列：优先鼠标悬停列；拖拽中没有 hover，就用被拖边界右侧的列。 */
const bandCol = computed<number | null>(() => {
  if (hoverCol.value !== null) return Math.min(hoverCol.value, props.dividers.length - 1)
  if (props.draggingIndex !== null) {
    return Math.min(props.draggingIndex, props.dividers.length - 1)
  }
  return null
})

/** 指针 x（相对覆盖层）→ 列序号。dividers[i] 是第 i+1 列的右边界。 */
function colAt(x: number) {
  let col = 0
  while (col < props.dividers.length && x > props.dividers[col]) col++
  return Math.min(col, props.dividers.length - 1)
}

function onMove(e: PointerEvent) {
  const overlay = overlayEl.value
  if (!overlay) return
  const rect = overlay.getBoundingClientRect()
  hoverCol.value = colAt(e.clientX - rect.left)
  // 鼠标纵向是否落在表头色块区内（overlay 覆盖的就是表头条带）。
  inHeader.value = e.clientY >= rect.top && e.clientY <= rect.bottom
}

function onLeave() {
  inHeader.value = false
  hoverCol.value = null
}

/** 表头色块内任意位置按下 = 开始拖该列右边界（仅限表头区内）。 */
function onDown(e: PointerEvent) {
  const overlay = overlayEl.value
  if (!overlay) return
  const rect = overlay.getBoundingClientRect()
  if (e.clientY < rect.top || e.clientY > rect.bottom) return
  const col = colAt(e.clientX - rect.left)
  if (props.dividers.length === 0) return
  emit('dragStart', col, e)
}

// 覆盖层 pointer-events:none，收不到指针事件；改在父容器（组件根）上监听，
// 表头/行上的指针事件都会冒泡到这里。坐标始终相对覆盖层计算。
let container: HTMLElement | null = null
function onRootMove(e: PointerEvent) {
  onMove(e)
}
function onRootDown(e: PointerEvent) {
  onDown(e)
}

onMounted(() => {
  container = overlayEl.value?.parentElement ?? null
  container?.addEventListener('pointermove', onRootMove)
  container?.addEventListener('pointerdown', onRootDown)
  container?.addEventListener('pointerleave', onLeave)
})
onBeforeUnmount(() => {
  container?.removeEventListener('pointermove', onRootMove)
  container?.removeEventListener('pointerdown', onRootDown)
  container?.removeEventListener('pointerleave', onLeave)
})

/** 第 col 列高亮矩形的 left/width：左边界 = 前一条 divider（或 0），右边界 = 本列 divider。 */
function bandLeft(col: number) {
  return col === 0 ? 0 : props.dividers[col - 1]
}
function bandWidth(col: number) {
  return props.dividers[col] - bandLeft(col)
}
</script>

<template>
  <div
    ref="overlayEl"
    class="axt-overlay"
    :style="{
      top: `${overlayTop}px`,
      height: `${headerHeight}px`,
      transform: `translateX(${-scrollLeft}px)`,
    }"
    data-ax-overlay
  >
    <!-- 列高亮色块：表头底色；拖拽时跟随边界实时变宽/变窄。 -->
    <div
      v-if="bandCol !== null && inHeader"
      class="axt-band is-on"
      :class="{ 'is-dragging': draggingIndex === bandCol || draggingIndex === bandCol + 1 }"
      :style="{ left: `${bandLeft(bandCol)}px`, width: `${bandWidth(bandCol)}px` }"
    />
  </div>
</template>

