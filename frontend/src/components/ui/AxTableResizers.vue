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
 *   把手柄拆成独立子组件后，高频状态只驱动这 7 个手柄重渲染，表格本体一次都不动。
 *
 * 样式（.axt-overlay / .axt-handle / .axt-line）由 AxTable 注入的样式表提供，
 * 选择器以 AxTable 根 id 作用域限定，本组件渲染在其内部，因此照常生效。
 */
import { ref } from 'vue'

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

/** hover 状态留在本组件内部：它是纯视觉反馈，不该影响外层。 */
const hovered = ref<number | null>(null)

/**
 * 方案 B：跟随鼠标的交互条。
 * 鼠标在表头内任意位置移动时，指示条始终出现在「离鼠标最近的那条列边界」上
 * （不限制距离），鼠标左右移动时它在边界间跳转；一次只显示一条。
 * 拖拽中的那条强制常亮。
 */
const nearest = ref<number | null>(null)

function onMove(e: PointerEvent) {
  const rect = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const x = e.clientX - rect.left
  let best = -1
  let bestDist = Infinity
  for (let i = 0; i < props.dividers.length; i++) {
    const d = Math.abs(props.dividers[i] - x)
    if (d < bestDist) {
      bestDist = d
      best = i
    }
  }
  nearest.value = best
}

function onLeave() {
  nearest.value = null
  hovered.value = null
}

/** 某条边界当前是否应显示（被鼠标选中 或 正在拖拽）。 */
function isActive(i: number) {
  return nearest.value === i || props.draggingIndex === i
}
</script>

<template>
  <div
    class="axt-overlay"
    :style="{
      top: `${overlayTop}px`,
      height: `${headerHeight}px`,
      transform: `translateX(${-scrollLeft}px)`,
    }"
    data-ax-overlay
    @pointermove="onMove"
    @pointerleave="onLeave"
  >
    <div
      v-for="(x, i) in dividers"
      :key="i"
      class="axt-handle"
      :class="{ 'is-dragging': draggingIndex === i }"
      role="separator"
      aria-orientation="vertical"
      :aria-label="`调整第 ${i + 1} 列宽度，双击恢复自动宽度`"
      :tabindex="0"
      :style="{ left: `${x}px` }"
      @pointerdown="(e) => emit('dragStart', i, e)"
      @dblclick="emit('reset', i)"
      @keydown="(e) => emit('dragKey', i, e)"
    >
      <!-- 交互条：仅当鼠标靠近该边界（±6px）或正在拖拽时显示。 -->
      <span class="axt-line" :class="{ 'is-active': isActive(i) }" />
    </div>
  </div>
</template>

