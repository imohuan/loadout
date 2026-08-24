<script setup lang="ts">
// 公共组件 SplitPane：通用可拖拽左右分栏容器。
//
// 设计要点（与用户需求一一对应）：
// 1) 左右两侧各自有最小宽度 minLeft/minRight（像素）。正常拖拽时 divider
//    被限制在 [minLeft, W-D-minRight] 区间内，永不越过最小限制。
// 2) 支持某一面"占满全屏"：当鼠标继续往越界方向拖过 SNAP 阈值后，
//    对应侧塌缩为 0，另一侧占满整个区域；divider 滑块紧贴塌缩侧的边缘。
// 3) 拖拽期间一律走 pointer 事件 + 实时写内联 style，**禁用任何 transition /
//    动画**，divider 始终精确跟随鼠标位置（减去抓取偏移，绝不跳变）。
// 4) 位置换算严格扣除 divider 自身宽度 D：left 面板宽度 L 与鼠标坐标一一对应，
//    右侧宽度 = W - D - L，避免"差一像素"或溢出。

import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    /** 左侧最小宽度（px） */
    minLeft?: number
    /** 右侧最小宽度（px） */
    minRight?: number
    /** 初始占比（0~1，左侧占比） */
    initial?: number
    /** 越过最小限制多少 px 后触发"占满全屏"塌缩 */
    snap?: number
    /** 高度类（透传到根容器，默认自适应内容） */
    heightClass?: string
  }>(),
  {
    minLeft: 180,
    minRight: 180,
    initial: 0.5,
    snap: 56,
    heightClass: '',
  },
)

// divider 视觉宽度（hit area 容器宽度）——与模板里 w-2.5 保持一致
const DIVIDER_W = 10

const rootRef = ref<HTMLElement | null>(null)
const containerW = ref(0) // 可用宽度（不含 divider，即左右内容区总宽）

// 权威状态：左面板占可用宽度的比例（0~1）。塌缩态用 collapsed 表示。
const ratio = ref(props.initial)
const collapsed = ref<'left' | 'right' | null>(null)

// 拖拽态
const dragging = ref(false)
let grabOffset = 0 // 抓取点相对 divider 左边缘的偏移（保证 divider 贴手移动）
let curContainerLeft = 0 // 拖拽时容器相对 viewport 的 left

// 可用左侧宽度上/下限（不含 divider，单位 px）
const maxLeft = computed(() => Math.max(props.minLeft, containerW.value - props.minRight))

// 当前左面板实际宽度（px，不含 divider）
const leftPx = computed(() => {
  const w = containerW.value
  if (w <= 0) return 0
  if (collapsed.value === 'left') return 0
  if (collapsed.value === 'right') return w
  return Math.min(Math.max(ratio.value * w, props.minLeft), maxLeft.value)
})

const leftStyle = computed(() => ({ width: `${leftPx.value}px` }))

// ---- 尺寸观测：容器宽度变化时按比例重算（非拖拽态） ----
let ro: ResizeObserver | null = null
function measure() {
  const el = rootRef.value
  if (!el) return
  // 内容区宽度 = 总宽 - divider 宽；这样 leftPx + DIVIDER_W + rightPx 恰好等于总宽，
  // 塌缩贴右时 divider 才能完整留在容器内可见。
  containerW.value = Math.max(0, el.clientWidth - DIVIDER_W)
}
function onResize() {
  // 拖拽中不打断手部位置；resize 仅在非拖拽态重算
  if (dragging.value) return
  measure()
}
onMounted(async () => {
  measure()
  await nextTick()
  // 初次挂载若比例越界则夹紧
  const w = containerW.value
  if (w > 0) {
    ratio.value = Math.min(Math.max(ratio.value, props.minLeft / w), maxLeft.value / w)
  }
  ro = new ResizeObserver(onResize)
  if (rootRef.value) ro.observe(rootRef.value)
  window.addEventListener('resize', measure)
})
onBeforeUnmount(() => {
  ro?.disconnect()
  window.removeEventListener('resize', measure)
})

// 当 containerW 因塌缩态改变后变化时，比例保持；塌缩态由 collapsed 控制。
watch(containerW, (w) => {
  if (w <= 0 || dragging.value || collapsed.value) return
  // 仅在数值异常时夹紧，不动比例本体
  ratio.value = Math.min(Math.max(ratio.value, props.minLeft / w), maxLeft.value / w)
})

// ---- 拖拽逻辑 ----
// pointerdown 绑定在 divider 上（拿初始坐标），move/up/cancel 动态挂到 window，
// 保证鼠标拖出 divider 区域后仍实时跟随、抬起任意位置都能结束拖拽。
function onPointerDown(e: PointerEvent) {
  // 仅主键（左键）触发
  if (e.button !== 0 && e.pointerType === 'mouse') return
  const el = rootRef.value
  if (!el) return
  measure()
  curContainerLeft = el.getBoundingClientRect().left
  // 抓取偏移：鼠标相对 divider 左边缘的位置
  // divider 左边缘 = curContainerLeft + leftPx（塌缩左时为 0，塌缩右时为 W）
  grabOffset = e.clientX - (curContainerLeft + leftPx.value)
  dragging.value = true
  window.addEventListener('pointermove', onPointerMove)
  window.addEventListener('pointerup', onPointerUp)
  window.addEventListener('pointercancel', onPointerCancel)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  e.preventDefault()
}

function onPointerMove(e: PointerEvent) {
  if (!dragging.value) return
  const w = containerW.value
  if (w <= 0) return
  // divider 左边缘目标位置 = 鼠标 - 抓取偏移 - 容器左
  const target = e.clientX - grabOffset - curContainerLeft
  // 越过最小限制 + snap → 塌缩占满
  if (target < props.minLeft - props.snap) {
    collapsed.value = 'left'
    return
  }
  if (target > maxLeft.value + props.snap) {
    collapsed.value = 'right'
    return
  }
  // 离开塌缩区或处于正常区：取消塌缩，按夹紧值实时跟随鼠标
  collapsed.value = null
  const clamped = Math.min(Math.max(target, props.minLeft), maxLeft.value)
  ratio.value = clamped / w
}

function endDrag() {
  if (!dragging.value) return
  dragging.value = false
  window.removeEventListener('pointermove', onPointerMove)
  window.removeEventListener('pointerup', onPointerUp)
  window.removeEventListener('pointercancel', onPointerCancel)
  document.body.style.cursor = ''
  document.body.style.userSelect = ''
}

function onPointerUp() {
  endDrag()
}
function onPointerCancel() {
  endDrag()
}
</script>

<template>
  <div ref="rootRef" class="SplitPane relative flex w-full overflow-hidden" :class="heightClass">
    <!-- 左面板 -->
    <div class="SplitPane__pane relative min-w-0 overflow-hidden" :style="leftStyle">
      <slot name="left" />
    </div>

    <!-- 拖拽滑块（含 hit area + 视觉线 + 握把） -->
    <div
      class="SplitPane__divider group relative z-20 flex w-2.5 shrink-0 cursor-col-resize touch-none select-none items-center justify-center"
      :class="dragging ? '' : 'hover:[&_div]:bg-primary/60'"
      @pointerdown="onPointerDown"
      @dblclick="
        () => {
          collapsed = null
          ratio = props.initial
        }
      "
    >
      <!-- 视觉竖线 -->
      <div
        class="pointer-events-none absolute inset-y-0 left-1/2 w-px -translate-x-1/2 bg-border transition-colors"
        :class="dragging ? 'bg-primary' : ''"
      />
      <!-- 握把胶囊 -->
      <div
        class="pointer-events-none h-8 w-1 rounded-full bg-border transition-colors"
        :class="dragging ? 'bg-primary' : 'group-hover:bg-primary/60'"
      />
    </div>

    <!-- 右面板（自适应剩余宽度） -->
    <div class="SplitPane__pane relative min-w-0 flex-1 overflow-hidden">
      <slot name="right" />
    </div>
  </div>
</template>
