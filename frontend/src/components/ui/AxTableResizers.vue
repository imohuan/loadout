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
 * 交互（本版简化后的最终形态）：
 *   - 表头内任意位置按下 = 开始拖该列右边界（覆盖层 pointer-events:none，
 *     pointerdown 在父容器上监听）；
 *   - 手柄元素只承担「拖拽中竖线」与「双击恢复 / 键盘调宽」两个辅助职责，
 *     平时透明不参与视觉。
 *
 * 样式（.axt-overlay / .axt-handle / .axt-line）由 AxTable 注入的样式表提供，
 * 选择器以 AxTable 根 id 作用域限定，本组件渲染在其内部，因此照常生效。
 */
import { onBeforeUnmount, onMounted, ref } from 'vue'

const overlayEl = ref<HTMLElement | null>(null)

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

/** 指针 x（相对覆盖层）→ 列序号。dividers[i] 是第 i+1 列的右边界。 */
function colAt(x: number) {
  let col = 0
  while (col < props.dividers.length && x > props.dividers[col]) col++
  return Math.min(col, props.dividers.length - 1)
}

/** 表头内任意位置按下 = 开始拖该列右边界（仅限表头区内按下才触发）。 */
function onDown(e: PointerEvent) {
  const overlay = overlayEl.value
  if (!overlay || props.dividers.length === 0) return
  const rect = overlay.getBoundingClientRect()
  if (e.clientY < rect.top || e.clientY > rect.bottom) return
  emit('dragStart', colAt(e.clientX - rect.left), e)
}

// 覆盖层 pointer-events:none，收不到指针事件；改在父容器（AxTable 根）上监听，
// 表头上的指针事件都会冒泡到这里。手柄自身的 dblclick/keydown 仍走各自监听。
let container: HTMLElement | null = null
function onRootDown(e: PointerEvent) {
  onDown(e)
}

onMounted(() => {
  container = overlayEl.value?.parentElement ?? null
  container?.addEventListener('pointerdown', onRootDown)
})
onBeforeUnmount(() => {
  container?.removeEventListener('pointerdown', onRootDown)
})
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
    <!-- 手柄：平时透明的拖拽中竖线 + 双击恢复 / 键盘调宽的交互点。 -->
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
      @dblclick="emit('reset', i)"
      @keydown="(e) => emit('dragKey', i, e)"
    >
      <span class="axt-line" />
    </div>
  </div>
</template>

