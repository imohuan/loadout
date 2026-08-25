<script setup lang="ts">
// 侧边栏底部全局进程面板：实时显示 procreg 统一执行器托管的进程。
// 运行中的进程可终止；历史记录可展开查看日志、耗时、退出码。
import { onMounted, ref } from 'vue'
import { RiCloseLine, RiCpuLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useProcessMonitor } from '@/composables/useProcessMonitor'
import type { ProcessInfo } from '@/lib/types'

const {
  running,
  history,
  connected,
  hasProcesses,
  start,
  kill,
} = useProcessMonitor()

const showHistory = ref(false)

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

function fmtDuration(p: ProcessInfo): string {
  const start = new Date(p.startedAt).getTime()
  const end = p.endedAt ? new Date(p.endedAt).getTime() : Date.now()
  const sec = Math.max(0, Math.round((end - start) / 1000))
  if (sec < 60) return `${sec}s`
  const m = Math.floor(sec / 60)
  const s = sec % 60
  return `${m}m${s}s`
}

async function onKill(p: ProcessInfo) {
  if (!window.confirm(`终止进程「${p.name}」？`)) return
  try {
    await kill(p.id)
    toast.success(`已请求终止「${p.name}」`)
  } catch {
    toast.error(`终止「${p.name}」失败`)
  }
}
</script>

<template>
  <div class="border-t border-border">
    <div class="flex items-center justify-between px-2 py-1.5">
      <div class="flex items-center gap-1.5 text-xs text-muted-foreground">
        <RiCpuLine size="14" />
        <span>进程</span>
        <span v-if="running.length" class="rounded bg-primary/10 px-1 text-primary">
          {{ running.length }} 运行中
        </span>
      </div>
      <span
        class="size-1.5 rounded-full"
        :class="connected ? 'bg-emerald-500' : 'bg-red-500'"
        :title="connected ? '已连接' : '连接断开'"
      />
    </div>

    <template v-if="hasProcesses">
      <!-- 运行中进程 -->
      <div v-if="running.length" class="space-y-1 px-2 pb-1">
        <div
          v-for="p in running"
          :key="p.id"
          class="group flex items-center gap-1.5 rounded px-1 py-0.5 text-xs hover:bg-muted"
        >
          <span class="size-1.5 shrink-0 rounded-full" :class="statusDot(p.status)" />
          <span class="min-w-0 flex-1 truncate" :title="p.cmd">{{ p.name }}</span>
          <span class="shrink-0 tabular-nums text-muted-foreground">{{ fmtMem(p.memBytes) }}</span>
          <button
            class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            :title="`终止 ${p.name}`"
            @click="onKill(p)"
          >
            <RiCloseLine size="13" />
          </button>
        </div>
      </div>

      <!-- 历史记录 -->
      <div v-if="history.length" class="pb-1">
        <button
          class="flex w-full items-center justify-between px-2 py-0.5 text-[11px] text-muted-foreground hover:text-foreground"
          @click="showHistory = !showHistory"
        >
          <span>历史（{{ history.length }}）</span>
          <span :class="showHistory ? 'rotate-180' : ''" class="transition-transform">▾</span>
        </button>
        <div v-if="showHistory" class="space-y-0.5 px-2">
          <div
            v-for="p in history"
            :key="p.id"
            class="rounded px-1 py-0.5 text-[11px] hover:bg-muted"
          >
            <div class="flex items-center gap-1.5">
              <span class="size-1.5 shrink-0 rounded-full" :class="statusDot(p.status)" />
              <span class="min-w-0 flex-1 truncate" :title="p.cmd">{{ p.name }}</span>
              <span class="shrink-0 tabular-nums text-muted-foreground">
                {{ fmtDuration(p) }}
              </span>
            </div>
            <div
              v-if="p.log.length"
              class="mt-0.5 max-h-24 overflow-y-auto rounded bg-muted/40 p-1 font-mono text-[10px] leading-tight text-muted-foreground"
            >
              <div v-for="(line, i) in p.log" :key="i" class="whitespace-pre-wrap break-all">
                {{ line }}
              </div>
            </div>
          </div>
        </div>
      </div>
    </template>
    <p v-else class="px-2 pb-1.5 text-[11px] text-muted-foreground">暂无后台进程</p>
  </div>
</template>
