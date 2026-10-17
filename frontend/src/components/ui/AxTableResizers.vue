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
 * 样式（.axt-overlay / .axt-handle / .axt-band / .axt-line）由 AxTable 注入的
 * 样式表提供，选择器以 AxTable 根 id 作用域限定，本组件渲染在其内部，因此照常生效。
 */
import { computed, ref } from 'vue'

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
 *   鼠标在表头内移动时，高亮「鼠标所在的那一列」——一个从左边到该列右边界的
 *   矩形色块（即该列自己的宽度）。拖拽边界时色块宽度实时变化，所见即所得。
 *   不再用指示竖线：竖线在左右对齐混排的表头里没有稳定的视觉锚点。
 */
const hoverCol = ref<number | null>(null)
/** 光标是否贴近边界（±5px）：贴边才显示 col-resize 并允许拖拽。 */
const nearEdge = ref(false)
/** 鼠标是否在表头区内（真实或合成事件都会触发）。 */
const inHeader = ref(false)
/** 光标最近的一条边界序号（-1 = 没有贴边）。 */
const nearestDivider = ref(-1)
/** 色块显示的列：优先鼠标悬停列；拖拽中没有 hover，就用被拖边界右侧的列。 */
const bandCol = computed<number | null>(() => {
  if (hoverCol.value !== null) return Math.min(hoverCol.value, props.dividers.length - 1)
  if (props.draggingIndex !== null) {
    return Math.min(props.draggingIndex, props.dividers.length - 1)
  }
  return null
})

/** 指针所在位置 → 列序号 + 是否贴近右边界。dividers[i] 是第 i+1 列的右边界。 */
function onMove(e: PointerEvent) {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const x = e.clientX - rect.left
  // 列序号：x 落在 (dividers[i-1], dividers[i]] 之间就是第 i 列。
  let col = 0
  while (col < props.dividers.length && x > props.dividers[col]) col++
  if (col >= props.dividers.length) col = props.dividers.length - 1
  hoverCol.value = col
  inHeader.value = true
  // 光标位置相对最近边界（左边界或右边界）的距离。
  const leftEdge = col === 0 ? 0 : props.dividers[col - 1]
  const dRight = Math.abs(props.dividers[col] - x)
  const dLeft = Math.abs(x - leftEdge)
  nearEdge.value = Math.min(dRight, dLeft) <= 5
  nearestDivider.value = dRight <= dLeft ? col : col - 1
}

function onLeave() {
  inHeader.value = false
  hoverCol.value = null
  nearEdge.value = false
  nearestDivider.value = -1
}

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
		class="axt-overlay"
		:class="{ 'is-near-cursor': nearEdge }"
    :style="{
      top: `${overlayTop}px`,
      height: `${headerHeight}px`,
      transform: `translateX(${-scrollLeft}px)`,
    }"
    data-ax-overlay
      @pointerenter="inHeader = true"
    @pointermove="onMove"
    @pointerleave="onLeave"
  >
    <!-- 列高亮色块：悬停列的表头底色；拖拽时跟随边界实时变宽/变窄。 -->
		<div
			v-if="bandCol !== null"
			class="axt-band"
			:class="{ 'is-dragging': draggingIndex === bandCol || draggingIndex === bandCol + 1, 'is-on': inHeader }"
			:style="{ left: `${bandLeft(bandCol)}px`, width: `${bandWidth(bandCol)}px` }"
		/>
    <!-- 边界手柄：贴边 ±5px 显示 col-resize；拖拽/双击逻辑与之前一致。 -->
    <div
      v-for="(x, i) in dividers"
      :key="i"
      class="axt-handle"
			:class="{ 'is-near': nearEdge && i === nearestDivider }"
      role="separator"
      aria-orientation="vertical"
      :aria-label="`调整第 ${i + 1} 列宽度，双击恢复自动宽度`"
      :tabindex="0"
      :style="{ left: `${x}px` }"
			@pointerdown="(e) => nearEdge && i === nearestDivider && emit('dragStart', i, e)"
      @dblclick="emit('reset', i)"
      @keydown="(e) => emit('dragKey', i, e)"
    />
  </div>
</template>

