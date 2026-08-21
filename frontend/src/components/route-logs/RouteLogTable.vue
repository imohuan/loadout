<script setup lang="ts">
import { computed, ref } from 'vue'
import { RiArrowDownSLine, RiArrowRightSLine } from '@remixicon/vue'
import type { Channel, RouteAttempt, RouteLog } from '@/lib/types'
import { BUILTIN_CHANNEL } from '@/lib/constants'
import { formatDate, formatDuration } from '@/lib/format'
import ChannelRef from '@/components/ChannelRef.vue'
import EmptyState from '@/components/EmptyState.vue'
import DataPagination from '@/components/DataPagination.vue'

const props = withDefaults(
  defineProps<{
    logs: RouteLog[]
    channels: Channel[]
    loadingDetail?: string
    /** false = 不做折叠：详情行始终展开、无箭头（如模型测试请求记录） */
    collapsible?: boolean
  }>(),
  { collapsible: true },
)
const emit = defineEmits<{ expand: [log: RouteLog] }>()
const expanded = ref(new Set<string>())
// 日志里 attempt.channel 三种粒度（与 AggregateTarget 对齐）：
// 单 Key / Key 多选 / 渠道级。把 candidate 行的渠道标签统一构造为
// ChannelRefInput，让 ChannelRef 渲染 "@ 渠道名(Key1, Key2)"。
// 缺省三种粒度都没拿到时返回空对象，ChannelRef 内部 v-if 兜底不渲染。
function channelRefFor(attempt: RouteAttempt): {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
} {
  if (attempt.channel_base_url) {
    return { channel_base_url: attempt.channel_base_url }
  }
  if (attempt.channel_ids?.length) {
    return { channel_ids: attempt.channel_ids }
  }
  return { channel_id: attempt.channel_id || '' }
}
// 最终目标（list 行）同样支持三种粒度：渠道级 > Key 多选 > 单 Key。
// BUILTIN_CHANNEL（自带模式）不在此返回——它走 finalTargetLabel 哨兵分支。
function finalChannelRef(log: RouteLog): {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
} | null {
  if (log.final_channel_base_url) {
    return { channel_base_url: log.final_channel_base_url }
  }
  if (log.final_channel_ids?.length) {
    return { channel_ids: log.final_channel_ids }
  }
  if (log.final_channel_id && log.final_channel_id !== BUILTIN_CHANNEL) {
    return { channel_id: log.final_channel_id }
  }
  return null
}
// 自带模式哨兵（与 ModelTestView 一致）：final_channel_id 持有此值时显示为「自带」+ key 名。
function finalTargetLabel(channelId?: string, skKeyName?: string) {
  if (channelId === BUILTIN_CHANNEL) {
    return skKeyName ? `自带 · ${skKeyName}` : '自带'
  }
  return ''
}
function toggle(log: RouteLog) {
  expanded.value.has(log.request_id)
    ? expanded.value.delete(log.request_id)
    : (expanded.value.add(log.request_id), emit('expand', log))
}
// 折叠模式：details 是否展开由 expanded 集合决定。
function isExpanded(log: RouteLog) {
  return expanded.value.has(log.request_id)
}
// 详情行是否渲染：
//   - collapsible === false（模型测试请求记录）：用户显式要求隐藏整个详情区
//     （含 attempts 列表与「暂无步骤详情」占位），详情不渲染。
//   - 折叠模式：展开时渲染详情。
function showDetails(log: RouteLog) {
  if (props.collapsible === false) return false
  return isExpanded(log)
}

// ---------- 前端分页 ----------
const page = ref(1)
const pageSize = ref(20)
const pagedLogs = computed(() => {
  const start = (page.value - 1) * pageSize.value
  return (props.logs || []).slice(start, start + pageSize.value)
})

// action 历史值兼容：后端已切换为中文，但旧库仍可能存着英文枚举
const ACTION_LABELS: Record<string, { label: string; tone: string }> = {
  首次尝试: {
    label: '首次尝试',
    tone: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  },
  切换渠道: {
    label: '切换渠道',
    tone: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  },
  切换模型: {
    label: '切换模型',
    tone: 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20',
  },
  视觉识别: {
    label: '视觉识别',
    tone: 'bg-teal-500/15 text-teal-700 dark:text-teal-300 border-teal-500/20',
  },
  // 兼容老数据
  initial: {
    label: '首次尝试',
    tone: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  },
  switch_channel: {
    label: '切换渠道',
    tone: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  },
  switch_model: {
    label: '切换模型',
    tone: 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20',
  },
}
function actionLabel(action?: string) {
  return ACTION_LABELS[action || '']?.label || action || '-'
}
function actionTone(action?: string) {
  return ACTION_LABELS[action || '']?.tone || ''
}

// 给 result 加更直观的颜色（不动 StatusBadge 全局默认）
const RESULT_TONES: Record<string, string> = {
  success: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  failed: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  running: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  skipped: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
  stream_interrupted: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
}
function resultTone(result?: string) {
  return RESULT_TONES[result || ''] || ''
}
// 模型测试的 result 字符串直接显示中文（不走 StatusBadge 的 health-status 映射，
// 避免模型健康"已关闭"语义污染测试请求记录）。
const RESULT_LABELS: Record<string, string> = {
  success: '已成功',
  failed: '失败',
  running: '进行中',
  skipped: '已跳过',
  stream_interrupted: '已中断',
}
function resultLabel(result?: string) {
  return RESULT_LABELS[result || ''] || result || '未知'
}

// 流速 t/s：completion_tokens / 秒。duration_ms<=0 或 tokens 缺失 → 返回空。
function streamTps(x: { duration_ms?: number; completion_tokens?: number }) {
  const ms = x.duration_ms ?? 0
  const tokens = x.completion_tokens ?? 0
  if (ms <= 0 || tokens <= 0) return ''
  const tps = tokens / (ms / 1000)
  if (!Number.isFinite(tps) || tps <= 0) return ''
  if (tps >= 100) return Math.round(tps).toString()
  return tps.toFixed(1)
}
function hasTokens(x: {
  prompt_tokens?: number
  completion_tokens?: number
  cached_tokens?: number
}) {
  return Boolean(x.prompt_tokens || x.completion_tokens || x.cached_tokens)
}
// 千分位格式化 tokens，超过 999 用 k/M 缩写，避免横向撑开。
function formatTokens(value?: number) {
  const n = value ?? 0
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${(n / 1000).toFixed(1)}k`
  if (n >= 1000) return n.toLocaleString('en-US')
  return String(n)
}
// 缓存占比：cached_tokens 相对 prompt_tokens（输入）的百分比
function cacheRatio(x: { prompt_tokens?: number; cached_tokens?: number }) {
  const prompt = x.prompt_tokens ?? 0
  const cached = x.cached_tokens ?? 0
  if (prompt <= 0 || cached <= 0) return ''
  return `${Math.round((cached / prompt) * 100)}%`
}
</script>

<template>
  <Card class="rounded-md pb-0!"
    ><CardHeader class="flex flex-row items-start justify-between gap-3 space-y-0"
      ><div>
        <CardTitle class="text-base">请求记录</CardTitle
        ><CardDescription>展开一条请求可查看每一次真实上游尝试和被跳过的候选。</CardDescription>
      </div>
      <slot name="actions" /> </CardHeader
    ><CardContent class="p-0"
      ><div v-if="pagedLogs.length" class="overflow-x-auto">
        <Table
          ><TableHeader
            ><TableRow
              ><TableHead class="w-10"></TableHead><TableHead>时间</TableHead
              ><TableHead>请求模型</TableHead><TableHead>最终目标</TableHead
              ><TableHead>结果</TableHead><TableHead>流</TableHead><TableHead>Tokens</TableHead
              ><TableHead>缓存</TableHead><TableHead>耗时</TableHead></TableRow
            ></TableHeader
          ><TableBody
            ><template v-for="log in pagedLogs" :key="log.request_id"
              ><TableRow
                :class="props.collapsible !== false ? 'cursor-pointer' : ''"
                @click="props.collapsible !== false && toggle(log)"
              ><TableCell
                  ><component
                    v-if="props.collapsible !== false"
                    :is="isExpanded(log) ? RiArrowDownSLine : RiArrowRightSLine"
                    size="18"
                    class="text-muted-foreground" /></TableCell
                ><TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{
                  formatDate(log.started_at)
                }}</TableCell
                ><TableCell
                  ><p class="font-mono text-xs font-medium">{{ log.requested_model }}</p>
                  <p class="mt-1 max-w-40 truncate font-mono text-[11px] text-muted-foreground">
                    {{ log.request_id }}
                  </p></TableCell
                ><TableCell class="text-sm"
                  ><span class="inline-flex items-center gap-0.5"
                    ><span class="font-mono">{{ log.final_model || '-' }}</span
                    ><template v-if="log.final_model"
                      ><span
                        v-if="log.final_channel_id === BUILTIN_CHANNEL"
                        class="text-muted-foreground"
                        >@ {{ finalTargetLabel(log.final_channel_id, log.sk_key_name) }}</span
                      ><ChannelRef
                        v-else-if="finalChannelRef(log)"
                        :target="finalChannelRef(log)"
                        :channels="channels" /></template></span
                  ></TableCell
                ><TableCell
                  ><Badge :class="resultTone(log.result)">{{ resultLabel(log.result) }}</Badge></TableCell
                ><TableCell class="whitespace-nowrap text-xs"
                  ><span
                    v-if="log.stream"
                    class="inline-flex items-center gap-1 rounded border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 text-[10px] font-medium text-sky-700 dark:text-sky-300"
                    >流<template v-if="streamTps(log)"
                      ><span class="opacity-60">·</span
                      ><span class="tabular-nums">{{ streamTps(log) }}</span
                      ><span class="opacity-60">t/s</span></template
                    ></span
                  ><span
                    v-else-if="hasTokens(log)"
                    class="inline-flex items-center rounded border border-slate-500/30 bg-slate-500/10 px-1.5 py-0.5 text-[10px] font-medium text-slate-700 dark:text-slate-300"
                    >非流</span
                  ><span v-else class="text-muted-foreground">-</span></TableCell
                ><TableCell class="whitespace-nowrap text-xs tabular-nums"
                  ><template v-if="hasTokens(log)"
                    ><span class="font-medium text-foreground"
                      >{{ formatTokens(log.prompt_tokens) }} /
                      {{ formatTokens(log.completion_tokens) }}</span
                    ><span
                      v-if="log.cached_tokens && log.cached_tokens > 0"
                      class="ml-1 text-[10px] text-violet-600 dark:text-violet-300"
                      >缓存 ↓ {{ formatTokens(log.cached_tokens) }}</span
                    ></template
                  ><span v-else class="text-muted-foreground">-</span></TableCell
                ><TableCell class="whitespace-nowrap text-xs tabular-nums"
                  ><template v-if="cacheRatio(log)"
                    ><span class="font-medium text-violet-600 dark:text-violet-300">{{
                      cacheRatio(log)
                    }}</span></template
                  ><span v-else class="text-muted-foreground">-</span></TableCell
                ><TableCell class="whitespace-nowrap tabular-nums text-sm">{{
                  formatDuration(log.duration_ms)
                }}</TableCell></TableRow
              ><TableRow v-if="showDetails(log)"
                ><TableCell colspan="9" class="bg-muted/30 p-4"
                  ><div
                    v-if="loadingDetail === log.request_id"
                    class="text-sm text-muted-foreground"
                  >
                    正在加载时间线...
                  </div>
                  <div v-else class="min-w-0 space-y-2 break-words">
                    <p
                      v-if="log.error_message"
                      class="break-all whitespace-pre-wrap text-sm text-destructive"
                    >
                      {{ log.error_message }}
                    </p>
                    <ol v-if="log.attempts?.length" class="space-y-2 border-l border-border pl-4">
                      <li
                        v-for="attempt in log.attempts"
                        :key="attempt.step_no"
                        class="relative text-sm"
                      >
                        <span
                          class="absolute -left-[21px] top-1.5 size-2 rounded-full bg-primary"
                        ></span>
                        <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
                          <span class="font-medium tabular-nums">{{ attempt.step_no }}.</span
                          ><span class="inline-flex items-center gap-0.5"
                            ><span class="font-mono text-xs">{{ attempt.model }}</span
                            ><ChannelRef
                              :target="channelRefFor(attempt)"
                              :channels="channels" /></span
                          ><Badge
                            variant="outline"
                            :class="['shrink-0 border', actionTone(attempt.action)]"
                            >{{ actionLabel(attempt.action) }}</Badge
                          ><Badge
                            :class="['shrink-0 border', resultTone(attempt.result)]"
                            >{{ resultLabel(attempt.result) }}</Badge
                          ><span class="text-xs text-muted-foreground">{{
                            formatDuration(attempt.duration_ms)
                          }}</span
                          ><span
                            v-if="attempt.stream"
                            class="inline-flex items-center gap-1 rounded border border-sky-500/30 bg-sky-500/10 px-1.5 py-0.5 text-[10px] font-medium text-sky-700 dark:text-sky-300"
                            >流<template v-if="streamTps(attempt)"
                              ><span class="opacity-60">·</span
                              ><span class="tabular-nums">{{ streamTps(attempt) }}</span
                              ><span class="opacity-60">t/s</span></template
                            ></span
                          ><span
                            v-else-if="hasTokens(attempt)"
                            class="inline-flex items-center gap-1 rounded border border-slate-500/30 bg-slate-500/10 px-1.5 py-0.5 text-[10px] font-medium text-slate-700 dark:text-slate-300"
                            >非流</span
                          ><span
                            v-if="hasTokens(attempt)"
                            class="inline-flex flex-row items-center gap-1 rounded border border-violet-500/30 bg-violet-500/10 px-1.5 py-0.5 text-[10px] font-medium text-violet-700 dark:text-violet-300"
                            ><span class="tabular-nums"
                              >{{ formatTokens(attempt.prompt_tokens) }} /
                              {{ formatTokens(attempt.completion_tokens) }}</span
                            ><span
                              v-if="attempt.cached_tokens && attempt.cached_tokens > 0"
                              class="text-[9px] font-normal opacity-80"
                              >缓存 ↓ {{ formatTokens(attempt.cached_tokens) }}</span
                            ></span
                          >
                        </div>
                        <p
                          v-if="attempt.error_message"
                          class="mt-1 break-all whitespace-pre-wrap text-xs text-muted-foreground"
                        >
                          {{ attempt.failure_class ? `${attempt.failure_class}: ` : ''
                          }}{{ attempt.error_message }}
                        </p>
                      </li>
                    </ol>
                    <p v-else class="text-sm text-muted-foreground">暂无步骤详情。</p>
                  </div></TableCell
                ></TableRow
              ></template
            ></TableBody
          ></Table
        >
      </div>
      <EmptyState
        v-else
        title="还没有请求记录"
        description="发生模型请求后，这里会按 request_id 展示完整路由过程。" />
      <div v-if="logs.length" class="border-t border-border p-3">
        <DataPagination
          v-model:page="page"
          v-model:page-size="pageSize"
          :total="logs.length"
        /></div></CardContent
  ></Card>
</template>
