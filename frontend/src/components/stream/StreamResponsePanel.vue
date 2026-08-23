<script setup lang="ts">
/**
 * 顶层流式响应可视化面板。
 *
 * 设计：
 *  - 顶部：统计栏（chunks / parsed / 用量 / 思考 / 正文字数 / truncated 标记）
 *  - 中部：思考过程折叠块（reasoningAccum 非空时才显示）
 *  - 主区：流式 markdown 渲染（contentAccum + streaming 光标）
 *  - 底部：逐 chunk 时间轴（默认折叠）
 *
 * 设计参考：backup/codex-base-ui/web/src/components/chat/ChatView.vue
 * 的整体三段布局（thinking / text-block / tool-group），并按 SSE 离线解析场景定制。
 */
import { computed, ref } from 'vue'
import { parseStreamBody } from '@/lib/parseSSE'
import type { StreamUsage } from '@/lib/parseSSE'
import StreamCollapsibleBlock from './StreamCollapsibleBlock.vue'
import StreamMarkdownBlock from './StreamMarkdownBlock.vue'
import StreamChunkTimeline from './StreamChunkTimeline.vue'

const props = defineProps<{
  /**
   * SSE 原文（plugins/request-log service.go responseSnapshot.Body）。
   * 类型不一定是字符串——可能是 null/undefined/非流式 JSON 字符串；
   * 任何非字符串都按「无内容」处理。
   */
  body?: unknown
  /** 后端 truncated 标记（response_json.truncated） */
  truncated?: boolean
  /** 是否流式（来自 route_log.stream）；false 时面板只展示 stats，不再展开可视化 */
  stream?: boolean
  /** 是否完成（来自 result==='success' 或 [DONE] 标志） */
  done?: boolean
  /** 当 SSE 解析无内容时的占位提示 */
  emptyText?: string
  /** 状态码 */
  statusCode?: number
}>()

// 把 body 解析成结构化数据，computed 保证 body / truncated 变化即重算。
const parsed = computed(() => parseStreamBody(props.body))

const stats = computed(() => {
  const p = parsed.value
  const reasoning = p.reasoningAccum.length
  const content = p.contentAccum.length
  return {
    chunksTotal: p.chunks.length,
    chunksParsed: p.parsedCount,
    hasReasoning: reasoning > 0,
    hasContent: content > 0,
    reasoningChars: reasoning,
    contentChars: content,
    isDone: p.isDone,
    finishReason: p.finishReason,
    truncated: !!props.truncated,
    usage: p.usage,
  }
})

const showPanel = computed(() => {
  // 仅 stream=true 时启用；body 不是字符串也仍然按 stats 走
  return props.stream === true
})

const reasoningOpen = ref(true)

function formatUsage(u?: StreamUsage): string {
  if (!u) return '—'
  const parts: string[] = []
  if (typeof u.prompt_tokens === 'number') parts.push(`prompt=${u.prompt_tokens}`)
  if (typeof u.completion_tokens === 'number') parts.push(`completion=${u.completion_tokens}`)
  if (typeof u.total_tokens === 'number') parts.push(`total=${u.total_tokens}`)
  if (u.prompt_tokens_details?.cached_tokens != null) parts.push(`cached=${u.prompt_tokens_details.cached_tokens}`)
  if (u.completion_tokens_details?.reasoning_tokens != null)
    parts.push(`reasoning_tokens=${u.completion_tokens_details.reasoning_tokens}`)
  return parts.length ? parts.join(' · ') : '—'
}
</script>

<template>
  <div v-if="showPanel" class="space-y-4">
    <!-- 统计行：metrics 一目了然 -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs">
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">状态</span>
        <span
          class="rounded-sm border px-1.5 py-0.5 font-mono text-[11px]"
          :class="
            stats.isDone
              ? 'border-emerald-500/30 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
              : stats.truncated
                ? 'border-amber-500/30 bg-amber-500/15 text-amber-700 dark:text-amber-300'
                : 'border-blue-500/30 bg-blue-500/15 text-blue-700 dark:text-blue-300'
          "
        >
          {{
            stats.isDone
              ? `已完成${stats.finishReason ? ` · ${stats.finishReason}` : ''}`
              : stats.truncated
                ? '已截断'
                : '未结束'
          }}
        </span>
      </span>
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">chunks</span>
        <span class="font-mono">{{ stats.chunksTotal }}</span>
        <span class="text-muted-foreground">· parsed {{ stats.chunksParsed }}</span>
      </span>
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">正文</span>
        <span class="font-mono">{{ stats.contentChars }} 字</span>
      </span>
      <span v-if="stats.hasReasoning" class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">思考</span>
        <span class="font-mono">{{ stats.reasoningChars }} 字</span>
      </span>
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">状态码</span>
        <span class="font-mono">{{ props.statusCode ?? '—' }}</span>
      </span>
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">usage</span>
        <span class="font-mono">{{ formatUsage(stats.usage) }}</span>
      </span>
    </div>

    <!-- 思考过程折叠块：reasoning 累计非空才显示 -->
    <StreamCollapsibleBlock
      v-if="stats.hasReasoning"
      v-model:open="reasoningOpen"
      collapsed-title="思考过程"
      expanded-title="思考过程"
      hide-icon
    >
      <div
        class="mt-2 max-h-64 overflow-auto whitespace-pre-wrap rounded border-l-2 border-amber-500/40 bg-amber-500/5 p-3 text-sm italic leading-relaxed text-foreground/80"
      >
        {{ parsed.reasoningAccum }}
      </div>
    </StreamCollapsibleBlock>

    <!-- 主文本块：流式 markdown -->
    <div>
      <div class="mb-1.5 flex items-center justify-between text-xs text-muted-foreground">
        <span>流式响应正文</span>
        <span v-if="stats.hasContent" class="font-mono">{{ stats.contentChars }} 字</span>
      </div>
      <div class="max-h-168 overflow-auto rounded-md border border-border bg-background/60 p-4">
        <StreamMarkdownBlock :text="parsed.contentAccum" :streaming="!stats.isDone && !stats.truncated" />
      </div>
    </div>

    <!-- chunk 时间轴 -->
    <StreamChunkTimeline :chunks="parsed.chunks" />
  </div>

  <div
    v-else
    class="rounded border border-dashed border-border py-6 text-center text-sm text-muted-foreground"
  >
    {{
      emptyText ??
      '当前日志非流式或 SSE 正文不可解析，未生成流式可视化。'
    }}
  </div>
</template>
