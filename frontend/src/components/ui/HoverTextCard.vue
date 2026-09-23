<script setup lang="ts">
// 通用「长文本 hover 卡片」：复刻转发日志错误列的弹出效果（白色卡片、
// 带标题行和复制按钮、宽屏自适应），替代系统默认的黑底 Tooltip。
//
// 适用场景：表格里被截断的长文本（错误摘要、URL、JSON、原因说明…）。
// 短提示（如「删除」「上移」这类按钮提示）不要用这个，用原生 Tooltip 就好——
// 那种只有两三个词，弹一张带复制按钮的卡片反而是打扰。
//
// 用法：
//   <HoverTextCard :text="row.message" label="错误详情">
//     <template #default>{ 截断的一行摘要 }</template>
//   </HoverTextCard>
// 文本为空时不弹卡（避免空悬浮层），trigger 原样显示占位符。
import { computed, ref } from 'vue'
import { HoverCard, HoverCardContent, HoverCardTrigger } from 'shadcn-vue-cdn'

const props = withDefaults(
  defineProps<{
    /** 完整内容（卡片里展示的文本）。空串/undefined 时不启用 hover。 */
    text?: string | null
    /** 卡片左上角的小标题 */
    label?: string
    /** 是否用等宽字体展示正文（JSON / 日志类内容建议开） */
    mono?: boolean
  }>(),
  { label: '详情', mono: false },
)

const content = computed(() => (props.text ?? '').trim())

async function copy() {
  if (!content.value) return
  try {
    await navigator.clipboard.writeText(content.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    /* 非安全上下文等场景静默失败 */
  }
}
const copied = ref(false)
</script>

<template>
  <HoverCard v-if="content" :open-delay="150" :close-delay="100">
    <HoverCardTrigger as-child>
      <slot />
    </HoverCardTrigger>
    <HoverCardContent align="start" :side-offset="6" class="w-[min(460px,calc(100vw-2rem))] p-0">
      <!-- 卡片壳与 RouteLogErrorCell 同款：label 行 + 复制按钮 + 正文 -->
      <div class="rounded-md border border-border/60 bg-muted/40 px-3 py-2 text-xs">
        <div class="mb-1 flex items-center justify-between gap-2">
          <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">{{ props.label }}</span>
          <button
            type="button"
            class="rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
            @click.stop="copy"
          >
            {{ copied ? '已复制' : '复制' }}
          </button>
        </div>
        <pre
          :class="[
            'max-h-72 overflow-auto whitespace-pre-wrap break-all leading-snug text-foreground/80',
            props.mono ? 'font-mono text-[11px]' : 'text-xs',
          ]"
          >{{ content }}</pre
        >
      </div>
    </HoverCardContent>
  </HoverCard>
  <!-- 无内容：不弹卡，trigger 原样输出 -->
  <span v-else><slot /></span>
</template>
