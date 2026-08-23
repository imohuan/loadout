<script setup lang="ts">
/**
 * 高度过渡的折叠块基础组件。
 *
 * 设计参考：backup/codex-base-ui/web/src/components/chat/CollapsibleBlock.vue
 * 区别在于：用 Tailwind v4 token（m- 前缀）+ shadcn-vue 风格颜色代替 gray-*，
 * 并提供 disabled、hide-icon 等更贴合项目风格的 API。
 */
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'

const props = withDefaults(
  defineProps<{
    /** 折叠态的标题 */
    collapsedTitle?: string
    /** 展开态的标题（默认与 collapsedTitle 相同） */
    expandedTitle?: string
    /** 受控开合状态 */
    open: boolean
    /** 禁用点击切换（用于内容必展开的场合） */
    disabled?: boolean
    /** 隐藏左侧前缀图标 */
    hideIcon?: boolean
  }>(),
  { collapsedTitle: '', expandedTitle: '', disabled: false, hideIcon: false },
)

const emit = defineEmits<{
  /** v-model:open 等价事件：父组件用 :open + @update:open 双向绑定 */
  'update:open': [value: boolean]
}>()

function handleToggle() {
  if (props.disabled) return
  emit('update:open', !props.open)
}

const innerRef = ref<HTMLDivElement | null>(null)
const height = ref(0)

function updateHeight() {
  const el = innerRef.value
  if (el) height.value = el.offsetHeight
}

const contentStyle = computed(() => {
  if (!props.open) return { height: '0px' }
  return { height: `${height.value}px` }
})

let ro: ResizeObserver | null = null

onMounted(() => {
  updateHeight()
  if (typeof ResizeObserver !== 'undefined' && innerRef.value) {
    ro = new ResizeObserver(() => {
      if (props.open) updateHeight()
    })
    ro.observe(innerRef.value)
  }
})

onBeforeUnmount(() => {
  ro?.disconnect()
  ro = null
})

watch(
  () => props.open,
  async (isOpen) => {
    if (isOpen) {
      await nextTick()
      updateHeight()
    }
  },
)
</script>

<template>
  <div class="w-full overflow-hidden">
    <div
      :class="[
        'group inline-flex w-full items-center gap-1.5 py-0.5 text-xs text-muted-foreground transition-colors select-none',
        disabled ? 'cursor-default' : 'hover:text-foreground cursor-pointer',
      ]"
      @click="handleToggle"
    >
      <slot name="icon">
        <svg
          v-if="!hideIcon"
          class="h-3.5 w-3.5 shrink-0 text-muted-foreground/70 group-hover:text-foreground/80"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"
          />
        </svg>
      </slot>
      <slot name="title" :open="open">
        <span class="truncate font-medium">{{ open ? expandedTitle : collapsedTitle }}</span>
      </slot>
      <svg
        v-show="!disabled"
        :class="[open ? '' : '-rotate-90']"
        class="h-3 w-3 shrink-0 text-muted-foreground/60 opacity-0 transition-all duration-300 group-hover:opacity-100"
        fill="none"
        stroke="currentColor"
        viewBox="0 0 24 24"
      >
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
      </svg>
    </div>
    <div class="overflow-hidden transition-[height] duration-300 ease-out" :style="contentStyle">
      <div ref="innerRef">
        <slot />
      </div>
    </div>
  </div>
</template>
