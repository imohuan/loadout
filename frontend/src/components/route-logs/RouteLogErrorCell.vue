<script setup lang="ts">
// 统一错误展示组件：列表列（trigger + hover 卡片）与折叠详情（内嵌直显）共用。
// - 传入一个 JSON（error_body）+ 可选摘要（error_message），组件内部用
//   extractErrorSummary 识别 msg/message/error.message 等字段，得到一行基础文本。
// - trigger 模式（默认）：默认插槽渲染基础文本，hover 弹出悬浮卡片显示彩色 JSON 预览。
//   插槽是 scoped slot，会把 `message`（提取结果）传给调用方，方便自定义 trigger 外观。
// - 内嵌模式（trigger=false）：直接渲染同样的 JSON 卡片壳。列表折叠区、attempt 行内
//   都用这一份，不靠任何其他外壳组件——一个文件管所有错误展示。
import { computed, ref } from 'vue'
import { HoverCard, HoverCardContent, HoverCardTrigger } from 'shadcn-vue-cdn'
import { extractErrorSummary } from '@/lib/errorExtract'
import ErrorJsonPreview from '@/components/route-logs/ErrorJsonPreview.vue'

const props = withDefaults(
  defineProps<{
    /** 上游原始响应体（error_body）。可以是 JSON 字符串或纯文本。 */
    json?: string | null
    /** 后端摘要（error_message）。JSON 里提取不到 message 字段时兜底显示。 */
    message?: string | null
    /** 卡片 / 内嵌标题 */
    label?: string
    /** true = trigger + hover 悬浮卡片（列表列场景）；false = 直接内嵌展示（折叠详情场景） */
    trigger?: boolean
    /** 内嵌模式是否紧凑（attempt 行内用小号字） */
    compact?: boolean
  }>(),
  { label: '错误详情', trigger: true, compact: false },
)

// 基础文本：JSON 里提取 msg/message，找不到回退到传入的 error_message
const summary = computed(() => extractErrorSummary(props.json, props.message))
const hasJson = computed(() => Boolean(props.json && props.json.trim()))

// 复制到剪贴板。失败（如非安全上下文）静默吞掉。
async function copyJson() {
  const text = props.json || summary.value
  if (!text) return
  try {
    await navigator.clipboard.writeText(text)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    /* noop */
  }
}
const copied = ref(false)
</script>

<template>
  <!-- trigger + hover 卡片模式：列表列用。默认插槽 = 基础文本，scoped 传出 message -->
  <HoverCard v-if="props.trigger" :open-delay="150" :close-delay="100">
    <HoverCardTrigger as-child>
      <!-- 默认插槽 = 基础文本。scoped 传出 message，调用方自定义 trigger 时
           记得给根元素加 @click.stop，避免表格行点击展开被误触发 -->
      <slot :message="summary">
        <span
          class="inline-flex max-w-full items-center rounded px-1 text-xs text-destructive hover:bg-destructive/10"
          @click.stop
        >
          <span class="truncate underline">{{ summary }}</span>
        </span>
      </slot>
    </HoverCardTrigger>
    <HoverCardContent align="start" :side-offset="6" class="w-[min(460px,calc(100vw-2rem))] p-0">
      <!-- 卡片壳 = label 行 + 复制按钮 + 彩色 JSON。本组件自己渲染，不再外套 -->
      <div v-if="hasJson" class="rounded-md border border-border/60 bg-muted/40 px-3 py-2 text-xs">
        <div class="mb-1 flex items-center justify-between gap-2">
          <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
            {{ props.label }}
          </span>
          <button
            type="button"
            class="rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
            @click.stop="copyJson"
          >
            {{ copied ? '已复制' : '复制' }}
          </button>
        </div>
        <ErrorJsonPreview
          :body="props.json as string"
          :compact="true"
          max-height-class="max-h-72"
        />
      </div>
      <p v-else class="px-3 py-2 text-xs text-muted-foreground">
        {{ summary || '无错误详情' }}
      </p>
    </HoverCardContent>
  </HoverCard>

  <!-- 内嵌模式：折叠详情直接用。跟 hover 卡片里的壳样式对齐：label + 复制按钮 + 彩色 JSON -->
  <div
    v-else-if="hasJson"
    :class="[
      'rounded-md border border-border/60 bg-muted/40',
      props.compact ? 'mt-1 px-2 py-1.5 text-[11px]' : 'mt-2 px-3 py-2 text-xs',
    ]"
  >
    <div class="flex items-center justify-between gap-2">
      <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        {{ props.label }}
      </span>
      <button
        type="button"
        class="rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
        @click="copyJson"
      >
        {{ copied ? '已复制' : '复制' }}
      </button>
    </div>
    <ErrorJsonPreview
      :body="props.json as string"
      :compact="props.compact"
      :max-height-class="props.compact ? 'max-h-40' : 'max-h-80'"
      class="mt-1"
    />
  </div>
  <p v-else-if="summary" class="mt-1 text-xs text-muted-foreground">{{ summary }}</p>
</template>
