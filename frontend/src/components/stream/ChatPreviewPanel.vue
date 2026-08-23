<script setup lang="ts">
/**
 * 对话预览面板：把「请求 messages + 响应结果」渲染成对话气泡。
 *
 * 数据流：
 *   request_json.body（JSON 字符串）→ parse → messages[] → NormalizedMessage[]
 *   response_json.body（SSE 原文）  → parseStreamBody → contentAccum / reasoningAccum
 *     → 追加为最后一条 assistant 消息（reasoning 进 thinking 块，content 进正文块）
 *   底部保留：chunks 统计行 + 逐 chunk 时间轴（用户明确要求保留）
 *
 * 组件来源：backup/codex-base-ui/web/src/components/chat（全部复制 + 修复依赖）
 */
import { computed } from 'vue'
import ChatView from '@/components/chat/ChatView.vue'
import StreamChunkTimeline from './StreamChunkTimeline.vue'
import { parseStreamBody, extractResponseBody, looksLikeSSE } from '@/lib/parseSSE'
import type { NormalizedMessage } from '@/lib/chatTypes'

const props = defineProps<{
  /** 后端 request_logs.request_json（含 body JSON 字符串，内含 messages） */
  requestJson?: unknown
  /** 后端 request_logs.response_json（含 body SSE 原文） */
  responseJson?: unknown
  /** 是否流式（来自 log.stream） */
  stream?: boolean
  /** 是否完成（result === 'success' | 'failed'） */
  done?: boolean
  /** 后端 truncated 标记 */
  truncated?: boolean
  /** 状态码 */
  statusCode?: number
  /** 空态提示 */
  emptyText?: string
}>()

// ---- 解析请求 messages ----

interface ExtractedContent {
  text: string
  images: { path: string }[]
}

/** 从 OpenAI messages[i].content（字符串或多模态数组）提取纯文本 + 图片 */
function extractContent(content: unknown): ExtractedContent {
  if (typeof content === 'string') {
    return { text: content, images: [] }
  }
  if (Array.isArray(content)) {
    let text = ''
    const images: { path: string }[] = []
    for (const part of content) {
      if (!part || typeof part !== 'object') continue
      const p = part as Record<string, unknown>
      if (p.type === 'text' && typeof p.text === 'string') {
        text += p.text
      } else if (p.type === 'image_url') {
        // 脱敏后 base64 已被替换为 [image: mime, N bytes] 占位文本；
        // 仅 http(s)/data 开头才当作图片路径处理。
        const url = (p.image_url as { url?: unknown } | undefined)?.url
        if (typeof url === 'string' && /^(https?:\/\/|data:image\/)/.test(url)) {
          images.push({ path: url })
        }
      }
    }
    return { text, images }
  }
  return { text: '', images: [] }
}

const requestMessages = computed<NormalizedMessage[]>(() => {
  const rj = props.requestJson
  if (!rj || typeof rj !== 'object') return []
  const body = (rj as Record<string, unknown>).body
  if (typeof body !== 'string') return []
  let parsed: unknown
  try {
    parsed = JSON.parse(body)
  } catch {
    return []
  }
  const msgs = (parsed as Record<string, unknown>).messages
  if (!Array.isArray(msgs)) return []
  const out: NormalizedMessage[] = []
  // tool_call_id → 对应 tool_use 消息：assistant 里的 tool_calls 占位，后续 tool role 消息
  // 通过此表把 content 回写到对应 tool_use 的 toolResult.content，实现「工具调用 + 工具结果」
  // 配对显示（ToolCallCard 渲染时 outputText = toolResult.content）。
  const toolUseById = new Map<string, NormalizedMessage>()

  msgs.forEach((raw, i) => {
    if (!raw || typeof raw !== 'object') return
    const m = raw as Record<string, unknown>
    const role = m.role
    if (role === 'system') return // 系统提示不渲染（参考 ChatView computeGroups 同款处理）

    const { text, images } = extractContent(m.content)
    const hasText = !!text || images.length > 0
    const toolCalls = Array.isArray(m.tool_calls) ? m.tool_calls : null

    // user / assistant：先推文本（若有），再推 tool_calls（若有）
    if (role === 'user' || role === 'assistant') {
      if (hasText) {
        out.push({
          kind: 'text',
          role,
          content: text,
          images: images.length ? images : undefined,
          id: `req-${role === 'user' ? 'user' : 'ai'}-${i}`,
        })
      }
      if (toolCalls) {
        for (const tcRaw of toolCalls) {
          if (!tcRaw || typeof tcRaw !== 'object') continue
          const tc = tcRaw as Record<string, unknown>
          const fn = tc.function as Record<string, unknown> | undefined
          const tcId = typeof tc.id === 'string' ? tc.id : ''
          const argsRaw = typeof fn?.arguments === 'string' ? fn.arguments : ''
          const argsParsed = argsRaw ? safeJsonParse(argsRaw) : undefined
          const msg: NormalizedMessage = {
            kind: 'tool_use',
            role,
            id: `req-tool-${tcId || i}-${out.length}`,
            toolId: tcId || undefined,
            toolName: typeof fn?.name === 'string' ? fn.name : 'unknown',
            toolInput: argsParsed !== undefined ? argsParsed : (argsRaw || null),
            toolResult: null,
            status: 'done',
          }
          out.push(msg)
          if (tcId) toolUseById.set(tcId, msg)
        }
      }
      return
    }

    if (role === 'tool') {
      // 把工具结果内容回写到对应 tool_use.toolResult
      const tcId = typeof m.tool_call_id === 'string' ? m.tool_call_id : ''
      const contentStr = stringifyToolContent(m.content)
      if (tcId && toolUseById.has(tcId)) {
        const target = toolUseById.get(tcId)!
        target.toolResult = {
          content: contentStr,
          isError: typeof m.status === 'string' && m.status === 'error',
        }
      } else {
        // 找不到对应 tool_use（messages 可能截断 / 异常）——建一条孤儿记录仍渲染
        out.push({
          kind: 'tool_use',
          role: 'assistant',
          id: `req-tool-orphan-${i}-${tcId}`,
          toolId: tcId || undefined,
          toolName:
            typeof m.name === 'string'
              ? m.name
              : tcId
                ? `orphan:${tcId}`
                : 'unknown_tool',
          toolInput: null,
          toolResult: { content: contentStr, isError: typeof m.status === 'string' && m.status === 'error' },
          status: 'done',
        })
      }
      return
    }

    if (role === 'function') {
      // 旧版 OpenAI function 结果消息（无 tool_call_id，靠 name 关联）：孤儿记录渲染
      const contentStr = stringifyToolContent(m.content)
      if (!contentStr) return
      out.push({
        kind: 'tool_use',
        role: 'assistant',
        id: `req-fn-${i}-${typeof m.name === 'string' ? m.name : i}`,
        toolId: undefined,
        toolName: typeof m.name === 'string' ? m.name : 'function',
        toolInput: null,
        toolResult: { content: contentStr, isError: false },
        status: 'done',
      })
      return
    }
    // developer 等自定义 role 跳过（系统级提示）
  })
  return out
})

/** 安全 JSON.parse：失败返回 undefined（而不是抛错） */
function safeJsonParse(s: string): unknown {
  try {
    return JSON.parse(s)
  } catch {
    return undefined
  }
}

/** 把 tool 消息的 content（字符串或数组）压成单字符串，便于 ToolCallCard 展示 */
function stringifyToolContent(content: unknown): string {
  if (content == null) return ''
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    return content
      .map((p) => {
        if (!p || typeof p !== 'object') return ''
        const obj = p as Record<string, unknown>
        if (typeof obj.text === 'string') return obj.text
        try {
          return JSON.stringify(obj)
        } catch {
          return ''
        }
      })
      .filter(Boolean)
      .join('\n')
  }
  try {
    return JSON.stringify(content, null, 2)
  } catch {
    return String(content)
  }
}

// ---- 解析响应并追加为最后一条 assistant 消息 ----

const parsed = computed(() => parseStreamBody(extractResponseBody(props.responseJson)))

const responseMessages = computed<NormalizedMessage[]>(() => {
  const p = parsed.value
  const out: NormalizedMessage[] = []
  if (p.reasoningAccum) {
    out.push({ kind: 'thinking', role: 'assistant', content: p.reasoningAccum, id: 'resp-thinking' })
  }
  if (p.contentAccum) {
    out.push({
      kind: props.done ? 'text' : 'stream',
      role: 'assistant',
      content: p.contentAccum,
      // done 时 id 以 msg- 开头 → TextBlock isStreaming=false（无光标）
      id: props.done ? 'msg-resp-content' : 'resp-content',
    })
  }
  // tool_calls：聚合完成后每个调用一条 tool_use 消息 → ToolCallCard
  // （useGroups 会把相邻 tool_use 合并成 tool-group 渲染）
  if (p.toolCalls.length) {
    for (const tc of p.toolCalls) {
      out.push({
        kind: 'tool_use',
        role: 'assistant',
        id: `resp-tool-${tc.id || tc.index}`,
        toolId: tc.id || undefined,
        toolName: tc.name || 'unknown',
        toolInput: tc.argumentsParsed ?? tc.argumentsRaw,
        toolResult: null,
        status: 'done',
      })
    }
  }
  return out
})

const allMessages = computed<NormalizedMessage[]>(() => [
  ...requestMessages.value,
  ...responseMessages.value,
])

// ---- 统计行（保留原面板信息密度） ----

const stats = computed(() => {
  const p = parsed.value
  return {
    chunksTotal: p.chunks.length,
    chunksParsed: p.parsedCount,
    contentChars: p.contentAccum.length,
    reasoningChars: p.reasoningAccum.length,
    toolCallCount: p.toolCalls.length,
    format: p.format,
    isDone: p.isDone,
    finishReason: p.finishReason,
    usage: p.usage,
  }
})

const showPanel = computed(() => props.stream === true)

function formatUsage(u: { prompt_tokens?: number; completion_tokens?: number; total_tokens?: number; prompt_tokens_details?: { cached_tokens?: number }; completion_tokens_details?: { reasoning_tokens?: number } } | undefined): string {
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

function looksLikeStream(): boolean {
  const body = extractResponseBody(props.responseJson)
  return body ? looksLikeSSE(body) : false
}
</script>

<template>
  <div v-if="showPanel" class="space-y-4">
    <!-- 统计行 -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs">
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">状态</span>
        <span
          class="rounded-sm border px-1.5 py-0.5 font-mono text-[11px]"
          :class="
            stats.isDone
              ? 'border-emerald-500/30 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
              : props.truncated
                ? 'border-amber-500/30 bg-amber-500/15 text-amber-700 dark:text-amber-300'
                : 'border-blue-500/30 bg-blue-500/15 text-blue-700 dark:text-blue-300'
          "
        >
          {{
            stats.isDone
              ? `已完成${stats.finishReason ? ` · ${stats.finishReason}` : ''}`
              : props.truncated
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
      <span v-if="stats.reasoningChars" class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">思考</span>
        <span class="font-mono">{{ stats.reasoningChars }} 字</span>
      </span>
      <span v-if="stats.toolCallCount" class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">工具调用</span>
        <span class="font-mono">{{ stats.toolCallCount }}</span>
      </span>
      <span class="inline-flex items-center gap-1.5">
        <span class="text-muted-foreground">协议</span>
        <span class="rounded-sm bg-zinc-500/15 px-1.5 py-0.5 font-mono text-[10px] text-zinc-600 dark:text-zinc-300">{{
          stats.format
        }}</span>
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

    <!-- 对话预览区：请求 messages + 响应结果 -->
    <div
      class="overflow-hidden rounded-md border border-border"
      :class="looksLikeStream() ? 'bg-muted/40' : 'bg-background/60'"
    >
      <ChatView :messages="allMessages" :empty-text="emptyText ?? '请求中没有可渲染的 messages 内容。'" />
    </div>

    <!-- 逐 chunk 时间轴（用户明确要求保留） -->
    <StreamChunkTimeline :chunks="parsed.chunks" />
  </div>

  <div
    v-else
    class="rounded border border-dashed border-border py-6 text-center text-sm text-muted-foreground"
  >
    {{
      emptyText ??
      '当前日志非流式或 SSE 正文不可解析，未生成对话预览。'
    }}
  </div>
</template>
