<script setup lang="ts">
// 通用 SSE 流式日志面板：订阅指定后端端点，实时显示命令输出。
// 用法：
//   <StreamLogPanel
//     :stream-url="'/api/unifyai/stream'"
//     :trigger="runKey"
//     v-model:status="runStatus"
//     @done="onDone"
//     @error="onError"
//   />
// 约定 SSE data 载荷为 JSON { type: 'log'|'done'|'error', line?, data? }。
import { computed, nextTick, onUnmounted, ref, watch } from 'vue'
import { ansiToHtml } from '@/lib/ansi'

export type StreamStatus = 'idle' | 'running' | 'done' | 'error'

const props = withDefaults(
  defineProps<{
    /** SSE 端点路径（同源，EventSource 自动带 session cookie） */
    streamUrl: string
    /** 递增触发值：变化时重新连接并清空日志 */
    trigger: number
    /** 状态（受控，父组件维护） */
    status: StreamStatus
    /** 日志空态文案 */
    emptyText?: string
  }>(),
  { emptyText: '暂无日志。点击「开始」后，命令输出将实时显示在这里。' },
)

const emit = defineEmits<{
  'update:status': [value: StreamStatus]
  /** 任务完成（done 事件，payload = exit code 字符串） */
  done: [exitCode: string]
  /** 任务失败（error 事件） */
  error: [message: string]
}>()

const logLines = ref<string[]>([])
const logBox = ref<HTMLElement>()
let stream: EventSource | null = null

// 首次进入且从未触发执行时，不展示日志卡片，避免在用户没点开始前就占一整块空白区。
// 父组件仍能正常 v-model:status 绑定：组件已挂载、watch 已注册；trigger 一旦 > 0
// openStream 立刻 emit('update:status','running')，status 变化后 showCard 翻转为 true。
const showCard = computed(() => props.trigger > 0 || props.status !== 'idle')

const statusLabel = () => {
  switch (props.status) {
    case 'running':
      return '运行中…'
    case 'done':
      return '已完成'
    case 'error':
      return '失败'
    default:
      return '未开始'
  }
}

function stopStream() {
  stream?.close()
  stream = null
}

function openStream() {
  stopStream()
  logLines.value = []
  emit('update:status', 'running')
  const es = new EventSource(props.streamUrl)
  stream = es
  // 统一用 onmessage + JSON.type 分桶，不依赖 SSE event: 行（更稳健）。
  es.onmessage = (e: MessageEvent) => {
    let ev: { type: string; line?: string; data?: string }
    try {
      ev = JSON.parse(e.data as string)
    } catch {
      ev = { type: 'log', line: e.data as string }
    }
    if (ev.type === 'log') {
      logLines.value.push(ev.line || '')
    } else if (ev.type === 'done') {
      emit('update:status', 'done')
      stopStream()
      emit('done', ev.data || '0')
    } else if (ev.type === 'error') {
      emit('update:status', 'error')
      stopStream()
      emit('error', ev.data || '任务失败')
    }
  }
  es.onerror = () => {
    // 任务结束或连接异常：仅当仍在 running 时视为失败。
    if (props.status === 'running') {
      emit('update:status', 'error')
    }
    stopStream()
  }
}

// 触发值变化 → 重新连接。immediate 让组件挂载时就捕获已递增的 trigger（如父组件在同一次
// 更新里先置 trigger 再让组件首挂载），避免首点只建 tab、二点才真正触发。
watch(
  () => props.trigger,
  () => {
    if (props.trigger > 0) openStream()
  },
  { immediate: true },
)

// 日志自动滚动到底部。
watch(
  () => logLines.value.length,
  async () => {
    await nextTick()
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  },
)

// 组件卸载时关闭 SSE 连接。
onUnmounted(stopStream)
</script>

<template>
  <Card v-if="showCard" class="rounded-md">
    <CardHeader class="flex flex-row flex-wrap items-center justify-between gap-x-3 gap-y-2 pb-2">
      <CardTitle class="text-base">执行日志</CardTitle>
      <div class="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs">
        <!--
          父组件可在标题旁注入自定义指标（结果 / 退出码 / 模式 等）。
          slot scope 暴露当前 status，父组件按需决定是否渲染。
        -->
        <slot name="header-extra" :status="status" />
      </div>
      <Badge
        :variant="
          status === 'done'
            ? 'default'
            : status === 'error'
              ? 'destructive'
              : status === 'running'
                ? 'secondary'
                : 'outline'
        "
        >{{ statusLabel() }}</Badge
      >
    </CardHeader>
    <CardContent class="p-0">
      <div
        ref="logBox"
        class="min-h-[320px] space-y-1 overflow-y-auto bg-muted/50 p-3 font-mono text-xs"
      >
        <div
          v-for="(line, i) in logLines"
          :key="i"
          class="whitespace-pre-wrap break-all text-foreground"
          v-html="ansiToHtml(line)"
        />
        <div v-if="!logLines.length" class="text-muted-foreground">{{ emptyText }}</div>
      </div>
    </CardContent>
  </Card>
</template>
