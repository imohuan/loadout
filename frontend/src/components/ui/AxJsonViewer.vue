<script setup lang="ts">
/**
 * AxJsonViewer — 可折叠 JSON 树查看器（复刻自 ax-ui-kit，样式改用 Tailwind 工具类）
 *
 * 递归渲染 JSON 数据，支持展开/折叠节点；Ctrl/⌘+点击递归展开/折叠子树。
 * 字符串值中的 http/https URL 自动转为可点击链接（v-html，先转义防 XSS）。
 */
import { computed, ref, watch } from 'vue'

/** URL 链接化：先 HTML 转义防 XSS，再正则替换为可点击 <a>。 */
const URL_RE = /https?:\/\/[^\s<>"'`（）()「」【】[\]]+/g
function linkify(text: string | undefined): string {
  if (!text) return ''
  const escaped = text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
  return escaped.replace(URL_RE, (url) => {
    const clean = url.replace(/[.,;:!?)]+$/, '')
    return `<a href="${clean}" target="_blank" rel="noopener noreferrer" class="text-accent underline decoration-dotted underline-offset-2 hover:decoration-solid">${clean}</a>`
  })
}

const props = withDefaults(
  defineProps<{
    data: unknown
    nodeKey?: string | number | null
    isLast?: boolean
    isRoot?: boolean
    expandTrigger?: number
    /** 递归展开/折叠触发器：>0 递归展开，<0 递归折叠，0 无操作 */
    deepExpandTrigger?: number
    /** 是否启用自动换行 */
    wrapEnabled?: boolean
    /** 默认全部展开（覆盖 isExpanded 初始值） */
    defaultExpandAll?: boolean
    /** 当前节点深度（根节点为 0），用于层级展开控制 */
    depth?: number
    /**
     * 展开级别：
     *   -1 = 全部折叠（仅根节点可见）
     *    0 = 全部展开（无限层级）
     *   N = 只展开前 N 层（depth < N 的节点可见）
     */
    expandLevel?: number
  }>(),
  {
    nodeKey: null,
    isLast: false,
    isRoot: false,
    expandTrigger: 0,
    deepExpandTrigger: 0,
    wrapEnabled: true,
    defaultExpandAll: false,
    depth: 0,
    expandLevel: 0,
  },
)

const isExpanded = ref(props.isRoot || props.expandTrigger > 0 || props.defaultExpandAll)
// 向下传递的递归触发器：Ctrl+点击时生成，逐层透传
const childDeepTrigger = ref(0)

/** 是否应基于展开级别显示 */
const expandedByLevel = computed(() => {
  if (props.expandLevel === -1) return false
  if (props.expandLevel === 0) return true
  return (props.depth ?? 0) < props.expandLevel
})

// 初始化时应用 expandLevel
if (expandedByLevel.value) {
  isExpanded.value = true
} else if (props.expandLevel >= 0 && !expandedByLevel.value && !props.isRoot && !props.defaultExpandAll) {
  isExpanded.value = false
}

function toggle() {
  isExpanded.value = !isExpanded.value
}

/** 根节点响应 defaultExpandAll 变化 → 递归传播到所有子节点 */
watch(
  () => props.defaultExpandAll,
  (val) => {
    if (!props.isRoot) return
    if (!isComplex.value || isEmpty.value) return

    const newState = !!val
    isExpanded.value = newState
    childDeepTrigger.value = newState
      ? Math.abs(childDeepTrigger.value) + 1
      : -(Math.abs(childDeepTrigger.value) + 1)
  },
)

/** 响应 expandLevel 变化：每个节点根据自身 depth 重新计算展开/折叠状态 */
watch(
  () => [props.expandLevel, props.depth] as const,
  () => {
    const shouldExpand = props.isRoot || props.defaultExpandAll || expandedByLevel.value
    if (isExpanded.value !== shouldExpand) {
      isExpanded.value = shouldExpand
    }
    if (isComplex.value && !isEmpty.value) {
      childDeepTrigger.value = shouldExpand
        ? Math.abs(childDeepTrigger.value) + 1
        : -(Math.abs(childDeepTrigger.value) + 1)
    }
  },
)

/** 响应父级传来的递归展开/折叠：自身先执行，然后原值透传给子级 */
watch(
  () => props.deepExpandTrigger,
  (val) => {
    if (val === 0) return
    if (!isComplex.value || isEmpty.value) return

    if (val > 0 && !isExpanded.value) {
      isExpanded.value = true
    }
    if (val < 0 && isExpanded.value) {
      isExpanded.value = false
    }
    childDeepTrigger.value = val
  },
)

/** Ctrl/⌘ + 点击：递归展开/折叠当前节点及所有嵌套子节点 */
function handleToggle(e: MouseEvent) {
  if (e.ctrlKey || e.metaKey) {
    if (!isComplex.value || isEmpty.value) {
      isExpanded.value = !isExpanded.value
      return
    }
    const newState = !isExpanded.value
    isExpanded.value = newState
    childDeepTrigger.value = newState
      ? Math.abs(childDeepTrigger.value) + 1
      : -(Math.abs(childDeepTrigger.value) + 1)
  } else {
    toggle()
  }
}

const parsedData = computed(() => {
  if (typeof props.data === 'string') {
    const trimmed = props.data.trim()
    if ((trimmed.startsWith('{') && trimmed.endsWith('}')) || (trimmed.startsWith('[') && trimmed.endsWith(']'))) {
      try {
        return JSON.parse(trimmed)
      } catch {
        /* ignore */
      }
    }
  }
  return props.data
})

const dataType = computed(() => {
  const val = parsedData.value
  if (val === null) return 'null'
  if (Array.isArray(val)) return 'array'
  return typeof val
})

const isComplex = computed(() => dataType.value === 'object' || dataType.value === 'array')

const isEmpty = computed(() => {
  if (dataType.value === 'object') return Object.keys(parsedData.value as object).length === 0
  if (dataType.value === 'array') return (parsedData.value as unknown[]).length === 0
  return false
})

const itemCount = computed(() => {
  if (dataType.value === 'object') return Object.keys(parsedData.value as object).length
  if (dataType.value === 'array') return (parsedData.value as unknown[]).length
  return 0
})

const openBracket = computed(() => (dataType.value === 'array' ? '[' : '{'))
const closeBracket = computed(() => (dataType.value === 'array' ? ']' : '}'))

function formatValue(val: unknown): string {
  if (val === null) return 'null'
  if (typeof val === 'boolean') return val ? 'true' : 'false'
  if (typeof val === 'string') return `"${val}"`
  if (typeof val === 'number') return String(val)
  return String(val)
}

const valueColorClass = computed(() => {
  switch (dataType.value) {
    case 'string':
      return 'text-[hsl(142_71%_45%)]'
    case 'boolean':
    case 'null':
      return 'text-[hsl(27_96%_45%)]'
    case 'number':
      return 'text-[hsl(221_83%_53%)]'
    default:
      return 'text-foreground'
  }
})

watch(
  () => props.expandTrigger,
  (val) => {
    if (val && val > 0) isExpanded.value = true
    if (val && val < 0) isExpanded.value = false
  },
)
</script>

<template>
  <div class="font-mono text-[12px] leading-[1.7] text-foreground text-left select-text" :class="{
    'whitespace-pre overflow-x-auto': !props.wrapEnabled && props.isRoot,
    'whitespace-pre-wrap break-words [overflow-wrap:anywhere]': props.wrapEnabled,
  }">
    <!-- 简单值（非对象/数组） -->
    <div v-if="!isComplex" class="flex items-start gap-[2px] py-[1px]">
      <div class="w-[14px] shrink-0" />
      <div class="flex-1 min-w-0">
        <span v-if="nodeKey !== null" class="text-primary font-medium mr-1">
          <span class="text-muted-foreground">'</span>{{ nodeKey }}<span class="text-muted-foreground">'</span><span
            class="text-muted-foreground">:</span>
        </span>
        <span :class="valueColorClass" class="inline-block align-bottom" v-html="linkify(formatValue(data))" />
        <span v-if="!isLast" class="text-muted-foreground shrink-0">,</span>
      </div>
    </div>

    <!-- 对象 / 数组 -->
    <div v-else class="flex flex-col items-start gap-[2px] py-[1px]">
      <!-- 头部行：箭头 + 键 + 开括号 -->
      <div class="flex items-start gap-[2px] cursor-pointer min-w-0" @click="handleToggle">
        <div
          class="w-[20px] h-[20px] shrink-0 inline-flex items-center justify-center rounded-[4px] text-muted-foreground "
          :class="{ invisible: isEmpty }">
          <svg class="w-[14px] h-[14px] transition-transform duration-150 hover:text-foreground" :class="{ 'rotate-90': isExpanded }"
            width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"
            stroke-linecap="round" stroke-linejoin="round">
            <path d="m9 18 6-6-6-6" />
          </svg>
        </div>

        <div class="flex-1 min-w-0">
          <span v-if="nodeKey !== null" class="mr-1">
            <span class="text-muted-foreground">'</span>{{ nodeKey }}<span class="text-muted-foreground">'</span><span
              class="text-muted-foreground">:</span>
          </span>
          <span class="text-muted-foreground px-2">{{ openBracket }}</span>
          <template v-if="!isExpanded && !isEmpty">
            <span v-if="dataType === 'array'" class="text-muted-foreground italic pr-2">...</span>
            <span v-else class="text-muted-foreground italic pr-2">{{ itemCount }} 项</span>
          </template>
          <span v-if="!isExpanded || isEmpty" class="text-muted-foreground">
            <span v-if="!isExpanded && dataType === 'array' && !isEmpty"> </span>{{ closeBracket
            }}<span v-if="!isLast">,</span>
          </span>
        </div>
      </div>

      <!-- 展开的子节点 -->
      <div v-show="isExpanded && !isEmpty" class="relative pl-[28px] min-w-0 w-full">
        <div class="absolute left-[14px] top-0 bottom-0 w-px bg-border opacity-60" />
        <div>
          <AxJsonViewer v-for="(val, key, index) in parsedData as any" :key="key"
            :node-key="dataType === 'array' ? null : key" :data="val" :is-last="index ===
              (dataType === 'array'
                ? (parsedData as any[]).length
                : Object.keys(parsedData as object).length) -
              1
              " :expand-trigger="expandTrigger" :deep-expand-trigger="childDeepTrigger" :depth="(props.depth ?? 0) + 1"
            :expand-level="props.expandLevel" :default-expand-all="props.defaultExpandAll" :is-root="false"
            :wrap-enabled="props.wrapEnabled" />
        </div>
      </div>

      <!-- 闭合括号 -->
      <div v-show="isExpanded && !isEmpty" class="flex items-start gap-[2px] min-w-0 w-full">
        <div class="w-[14px] shrink-0" />
        <div class="text-muted-foreground">
          {{ closeBracket }}<span v-if="!isLast">,</span>
        </div>
      </div>
    </div>
  </div>
</template>
