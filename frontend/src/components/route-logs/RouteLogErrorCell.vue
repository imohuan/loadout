<script setup lang="ts">
// 统一错误展示组件：列表列（trigger + hover 卡片）与折叠详情（内嵌直显）共用。
// - 传入一个响应体（error_body，JSON 或纯文本）+ 可选摘要（error_message），组件内部用
//   extractErrorSummary 识别 msg/message/error.message 等字段，得到一行基础文本。
// - trigger 模式（默认）：默认插槽渲染基础文本（截断一行），hover 弹出悬浮卡片显示
//   完整内容。插槽是 scoped slot，会把 `message`（提取结果）传给调用方，方便自定义 trigger 外观。
// - 内嵌模式（trigger=false）：直接渲染同样的卡片壳。列表折叠区、attempt 行内
//   都用这一份，不靠任何其他外壳组件——一个文件管所有错误展示。
//
// 「只要有内容就能 hover」是硬要求：很多失败没有上游响应体（聚合模型「所有目标当前
// 不可用」、网络错误、路由前置拒绝等，error_body 为空），此前 hover 卡片被「有没有 JSON」
// 挡住，一半的错误点不出任何东西。现在只要有响应体或 error_message 就弹卡：
// 响应体能解析成 JSON 走彩色预览，纯文本原样展示，两者都没有才回落完整的 error_message。
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
// 上游响应体（JSON 或纯文本）。是否 JSON 由 ErrorJsonPreview 内部判断，这里只判有无。
const bodyText = computed(() => (props.json || '').trim())
const hasBody = computed(() => bodyText.value !== '')
// 无响应体时的正文：完整 error_message（截断只发生在列表那一行摘要里）
const messageText = computed(() => (props.message || '').trim() || summary.value)
// 卡片正文（不截断）：有响应体就用响应体原文；没有（如聚合模型「所有目标当前不可用」
// 这类只有 error_message 的失败）才回落 error_message。
const cardBody = computed(() => (hasBody.value ? (props.json as string) : messageText.value))
// 只要有一丁点可展示内容就允许 hover：不再要求必须有 error_body，
// 否则一半失败（无上游响应体）会点不出任何东西。
const hasContent = computed(() => hasBody.value || Boolean(messageText.value))

// 复制到剪贴板。失败（如非安全上下文）静默吞掉。
async function copyJson() {
  const text = cardBody.value
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
  <!-- trigger + hover 卡片模式：列表列用。只要有可展示内容（上游响应体或后端摘要）
       就启用 hover 弹卡，避免无 error_body 的失败点不出详情。默认插槽 = 基础文本，
       scoped 传出 message -->
  <HoverCard v-if="props.trigger && hasContent" :open-delay="150" :close-delay="100">
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
      <!-- 卡片壳 = label 行 + 复制按钮 + 正文。本组件自己渲染，不再外套；
           外层已保证 hasContent 才会进入此分支 -->
      <div class="rounded-md border border-border/60 bg-muted/40 px-3 py-2 text-xs">
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
        <!-- 有上游响应体：JSON 走彩色预览，纯文本原样展示（由 ErrorJsonPreview 内部降级）；
             完全没有响应体时展示完整 error_message -->
        <ErrorJsonPreview
          v-if="hasBody"
          :body="bodyText"
          :compact="true"
          max-height-class="max-h-72"
        />
        <pre
          v-else
          class="max-h-72 overflow-auto whitespace-pre-wrap break-all font-mono text-[11px] leading-snug text-foreground/80"
          >{{ messageText }}</pre>
      </div>
    </HoverCardContent>
  </HoverCard>

  <!-- 完全无内容时的 trigger 降级：不弹卡片（避免空悬浮层） -->
  <span v-else-if="props.trigger" class="inline-flex max-w-full items-center rounded px-1 text-xs text-destructive">
    <slot :message="summary">
      <span class="truncate underline">{{ summary }}</span>
    </slot>
  </span>

  <!-- 内嵌模式：折叠详情直接用。跟 hover 卡片里的壳样式对齐：label + 复制按钮 + 正文。
       无上游响应体时同样展示完整 error_message（此前只显示 80 字截断摘要，展开也看不全） -->
  <div
    v-else-if="hasContent"
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
      v-if="hasBody"
      :body="bodyText"
      :compact="props.compact"
      :max-height-class="props.compact ? 'max-h-40' : 'max-h-80'"
      class="mt-1"
    />
    <pre
      v-else
      :class="[
        'overflow-auto whitespace-pre-wrap break-all font-mono leading-snug text-foreground/80',
        props.compact ? 'max-h-40 text-[11px]' : 'max-h-80 text-xs',
      ]"
      class="mt-1"
      >{{ messageText }}</pre>
  </div>
  <p v-else-if="summary" class="mt-1 text-xs text-muted-foreground">{{ summary }}</p>
</template>
