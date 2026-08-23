<script setup lang="ts">
/**
 * 流式逐 chunk 时间轴：默认折叠在 StreamCollapsibleBlock 里，每行 #
 * + 摘要，点击展开看完整 chunk JSON。
 *
 * 设计参考：backup/codex-base-ui/web/src/components/chat/ToolCallCard.vue
 * 区别：
 * 1. 摘要按 delta 内容分布显示（content / reasoning 双路径），便于发现流式分片；
 * 2. 行内附 finished 标记与累计进度（用 1px 高条），不强制每行都展开；
 * 3. 折叠态 vs 展开态独立记录（uiStore key 化）。
 */
import { computed, ref } from 'vue'
import { RiArrowRightSLine } from '@remixicon/vue'
import type { ParsedChunk } from '@/lib/parseSSE'
import StreamCollapsibleBlock from './StreamCollapsibleBlock.vue'

const props = defineProps<{
  /** parseStreamBody 产出的 ParsedChunk[] */
  chunks: ParsedChunk[]
  /** 折叠态标题（默认展示 chunk 数量 + 末位状态） */
  collapsedTitle?: string
  /** 展开态标题 */
  expandedTitle?: string
}>()

const open = ref(false)
const expandedIndex = ref<number | null>(null)

const summary = computed(() => {
  const total = props.chunks.length
  const last = props.chunks[props.chunks.length - 1]
  if (!last) return { line: '0 chunks', sub: '', lastIsDone: false }
  const sub = last.rawJson === '[DONE]'
    ? '已收到 [DONE]'
    : last.parsed
      ? `末 chunk finish_reason=${last.fields?.finishReason ?? '""'}`
      : '末 chunk 解析失败'
  const abnormal =
    last.parsed && (last.fields?.finishReason === 'length' || last.fields?.finishReason === 'content_filter')
      ? ' · finish_reason 异常'
      : ''
  return { line: `${total} chunks`, sub: sub + abnormal, lastIsDone: last.rawJson === '[DONE]' }
})

function toggleChunk(i: number) {
  expandedIndex.value = expandedIndex.value === i ? null : i
}

function previewDelta(s: string | undefined, max = 24): string {
  if (!s) return ''
  if (s.length <= max) return s
  return s.slice(0, max) + '…'
}

function previewReasoning(s: string | undefined, max = 16): string {
  return previewDelta(s, max)
}

/** 协议格式徽标文本 */
function formatLabel(f?: string): string {
  switch (f) {
    case 'claude':
      return 'claude'
    case 'responses':
      return 'responses'
    default:
      return 'chat'
  }
}
</script>

<template>
  <StreamCollapsibleBlock
    v-model:open="open"
    :collapsed-title="collapsedTitle ?? `${summary.line} · 逐 chunk 时间轴`"
    :expanded-title="expandedTitle ?? `${summary.line} · 逐 chunk 时间轴`"
  >
    <template #title="{ open: isOpen }">
      <span class="truncate font-medium">
        {{ (isOpen ? expandedTitle : collapsedTitle) ?? `${summary.line} · 逐 chunk 时间轴` }}
      </span>
      <span v-if="!isOpen && summary.sub" class="ml-2 truncate text-muted-foreground/80">
        · {{ summary.sub }}
      </span>
    </template>

    <div class="mt-2 overflow-hidden rounded-md border border-border bg-muted/30">
      <div
        v-for="chunk in chunks"
        :key="chunk.index"
        class="border-b border-border/50 last:border-b-0"
      >
        <button
          type="button"
          class="flex w-full items-center gap-2 px-3 py-1.5 text-left font-mono text-xs hover:bg-muted/60"
          :class="expandedIndex === chunk.index ? 'bg-muted/60' : ''"
          @click="toggleChunk(chunk.index)"
        >
          <RiArrowRightSLine
            class="size-3 shrink-0 text-muted-foreground transition-transform"
            :class="expandedIndex === chunk.index ? 'rotate-90' : ''"
          />
          <span class="w-12 shrink-0 text-muted-foreground">#{{ chunk.index }}</span>
          <span
            class="shrink-0 rounded-sm px-1.5 py-0.5 text-[10px]"
            :class="
              chunk.rawJson === '[DONE]'
                ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
                : chunk.parsed
                  ? 'bg-sky-500/15 text-sky-700 dark:text-sky-300'
                  : 'bg-amber-500/15 text-amber-700 dark:text-amber-300'
            "
          >
            {{
              chunk.rawJson === '[DONE]'
                ? 'DONE'
                : chunk.parsed
                  ? 'parsed'
                  : 'raw'
            }}
          </span>
          <span
            v-if="chunk.format"
            class="shrink-0 rounded-sm bg-zinc-500/15 px-1.5 py-0.5 text-[10px] text-zinc-600 dark:text-zinc-300"
          >
            {{ formatLabel(chunk.format) }}
          </span>
          <span v-if="chunk.parsed && chunk.fields?.finishReason" class="shrink-0 rounded-sm bg-violet-500/15 px-1.5 py-0.5 text-[10px] text-violet-700 dark:text-violet-300">
            finish={{ chunk.fields.finishReason }}
          </span>
          <span v-if="chunk.parsed && chunk.fields?.usage" class="shrink-0 rounded-sm bg-orange-500/15 px-1.5 py-0.5 text-[10px] text-orange-700 dark:text-orange-300">
            usage
          </span>
          <span
            v-if="chunk.parsed && chunk.fields?.contentDelta"
            class="grow truncate text-foreground"
            :title="chunk.fields.contentDelta"
          >
            {{ previewDelta(chunk.fields.contentDelta) }}
          </span>
          <span
            v-else-if="chunk.parsed && chunk.fields?.reasoningDelta"
            class="grow truncate text-muted-foreground italic"
            :title="chunk.fields.reasoningDelta"
          >
            思考：{{ previewReasoning(chunk.fields.reasoningDelta) }}
          </span>
          <span
            v-else-if="chunk.parsed && chunk.fields?.refusalDelta"
            class="grow truncate text-rose-600 dark:text-rose-400 italic"
            :title="chunk.fields.refusalDelta"
          >
            拒绝：{{ previewReasoning(chunk.fields.refusalDelta) }}
          </span>
          <span v-else-if="!chunk.parsed && chunk.rawJson" class="grow truncate text-amber-700 dark:text-amber-300" :title="chunk.rawJson">
            {{ previewDelta(chunk.rawJson, 36) }}
          </span>
          <span v-else class="grow truncate text-muted-foreground">（空）</span>
          <span v-if="chunk.parsed && chunk.fields?.id" class="shrink-0 text-muted-foreground">{{
            previewDelta(chunk.fields.id, 18)
          }}</span>
        </button>
        <div v-if="expandedIndex === chunk.index" class="border-t border-border/60 bg-background/60 p-2">
          <pre class="m-0 max-h-80 overflow-auto whitespace-pre-wrap break-all rounded bg-zinc-950/95 p-2 font-mono text-[11px] leading-relaxed text-zinc-100">{{
            chunk.rawJsonString ?? chunk.rawJson ?? ''
          }}</pre>
        </div>
      </div>
      <div v-if="!chunks.length" class="px-3 py-2 text-center text-xs text-muted-foreground">无 chunk 数据。</div>
    </div>
  </StreamCollapsibleBlock>
</template>
