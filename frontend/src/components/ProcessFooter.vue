<script setup lang="ts">
// 侧边栏底部全局进程面板：实时显示 procreg 统一执行器托管的进程。
// 运行中的进程可终止；历史记录通过标题右侧的图标按钮打开 Dialog 查看。
import { computed, onMounted, ref } from 'vue'
import { RiCloseLine, RiCpuLine, RiHistoryLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { ansiToHtml } from '@/lib/ansi'
import { useProcessMonitor } from '@/composables/useProcessMonitor'
import { useConfirm } from '@/composables/useConfirm'
import type { ProcessInfo } from '@/lib/types'

const {
  running,
  history,
  connected,
  hasProcesses,
  start,
  kill,
} = useProcessMonitor()

const { confirmDialog } = useConfirm()

const historyOpen = ref(false)

onMounted(() => start())

// 内存格式化：不足 1MB 显示 KB，否则 MB。
function fmtMem(bytes: number): string {
  if (!bytes) return '—'
  if (bytes >= 1024 * 1024) return (bytes / 1024 / 1024).toFixed(1) + ' MB'
  return Math.round(bytes / 1024) + ' KB'
}

// 状态点颜色。
function statusDot(status: string): string {
  switch (status) {
    case 'running':
      return 'bg-emerald-500'
    case 'done':
      return 'bg-sky-500'
    case 'error':
      return 'bg-red-500'
    default:
      return 'bg-muted-foreground'
  }
}

// 状态文案。
const statusLabel = (s: string) =>
  s === 'running' ? '运行中' : s === 'done' ? '已完成' : s === 'error' ? '失败' : s

function fmtDuration(p: ProcessInfo): string {
  const start = new Date(p.startedAt).getTime()
  const end = p.endedAt ? new Date(p.endedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - start) / 1000))
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}m${s}s`
}

// 格式化开始时间。
function fmtStartAt(p: ProcessInfo): string {
  const d = new Date(p.startedAt)
  return `${d.toLocaleDateString()} ${d.toLocaleTimeString()}`
}

async function onKill(p: ProcessInfo) {
  const confirmed = await confirmDialog({
    title: `终止进程「${p.name}」？`,
    description: '该操作会终止进程及其子进程。',
    confirmText: '终止',
  })
  if (!confirmed) return
  try {
    await kill(p.id)
    toast.success(`已请求终止「${p.name}」`)
  } catch {
    toast.error(`终止「${p.name}」失败`)
  }
}

const historyCount = computed(() => history.value.length)
</script>

<template>
  <div class="border-t border-border">
    <div class="flex items-center justify-between px-2 py-1.5">
      <div class="flex items-center gap-1.5 text-xs text-muted-foreground">
        <RiCpuLine size="14" />
        <span> {{ running.length }} 个进程运行中 </span>
        <span class="size-1.5 rounded-full" :class="connected ? 'bg-emerald-500' : 'bg-red-500'"
          :title="connected ? '已连接' : '连接断开'" />
      </div>
      <div class="flex items-center gap-1">
        <button class="relative rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          :title="`历史记录（${historyCount}）`" :disabled="historyCount === 0"
          :class="{ 'cursor-not-allowed opacity-40': historyCount === 0 }" @click="historyOpen = true">
          <RiHistoryLine size="15" />
          <span v-if="historyCount"
            class="absolute -right-0.5 -top-0.5 flex min-w-3.5 items-center justify-center rounded-full bg-primary px-0.5 text-[9px] leading-4 text-primary-foreground">
            {{ historyCount > 99 ? '99+' : historyCount }}
          </span>
        </button>
      </div>
    </div>

    <template v-if="hasProcesses">
      <!-- 运行中进程 -->
      <div v-if="running.length" class="space-y-1 px-2 pb-1.5">
        <div v-for="p in running" :key="p.id"
          class="group flex items-center gap-1.5 rounded px-1 py-0.5 text-xs hover:bg-muted">
          <span class="size-1.5 shrink-0 rounded-full" :class="statusDot(p.status)" />
          <span class="min-w-0 flex-1 truncate" :title="p.cmd">{{ p.name }}</span>
          <span class="shrink-0 tabular-nums text-muted-foreground">{{ fmtMem(p.memBytes) }}</span>
          <button class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            :title="`终止 ${p.name}`" @click="onKill(p)">
            <RiCloseLine size="13" />
          </button>
        </div>
      </div>
    </template>
    <p v-else class="px-2 pb-1.5 text-[11px] text-muted-foreground">暂无后台进程</p>

    <!-- 历史记录 Dialog -->
    <Dialog v-model:open="historyOpen">
      <DialogContent class="sm:max-w-lg!">
        <DialogHeader>
          <DialogTitle>历史进程</DialogTitle>
          <DialogDescription v-if="historyCount === 0">暂无历史记录。</DialogDescription>
        </DialogHeader>
        <div v-if="historyCount" class="max-h-[60vh] space-y-2 overflow-y-auto pr-1">
          <div v-for="p in history" :key="p.id" class="rounded-md border border-border p-2 text-sm">
            <div class="flex items-center gap-2">
              <span class="size-2 shrink-0 rounded-full" :class="statusDot(p.status)" />
              <span class="min-w-0 flex-1 truncate font-medium" :title="p.cmd">{{ p.name }}</span>
              <span class="shrink-0 text-xs" :class="p.status === 'error' ? 'text-red-500' : 'text-muted-foreground'">
                {{ statusLabel(p.status) }}
              </span>
              <span class="shrink-0 tabular-nums text-xs text-muted-foreground">
                {{ fmtDuration(p) }}
              </span>
            </div>
            <div class="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-muted-foreground">
              <span class="truncate">{{ fmtStartAt(p) }}</span>
              <span class="shrink-0">内存 {{ fmtMem(p.memBytes) }}</span>
              <span class="shrink-0">PID {{ p.pid || '—' }}</span>
              <span v-if="p.exitCode !== undefined" class="shrink-0">
                退出码 {{ p.exitCode }}
              </span>
            </div>
            <div v-if="p.cmd" class="mt-0.5 truncate font-mono text-xs text-muted-foreground" :title="p.cmd">
              {{ p.cmd }}
            </div>
            <details v-if="p.log.length" class="mt-1.5">
              <summary class="cursor-pointer text-xs text-muted-foreground hover:text-foreground">
                执行日志（{{ p.log.length }} 行）
              </summary>
              <div
                class="mt-1 max-h-40 overflow-y-auto rounded bg-muted/40 p-1.5 font-mono text-[11px] leading-tight text-muted-foreground">
                <div
                  v-for="(line, i) in p.log"
                  :key="i"
                  class="whitespace-pre-wrap break-all"
                  v-html="ansiToHtml(line)"
                />
              </div>
            </details>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  </div>
</template>
