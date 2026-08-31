<script setup lang="ts">
// 侧边栏底部全局进程面板：实时显示 procreg 统一执行器托管的进程。
// 运行中的进程可终止；历史记录通过标题右侧的图标按钮打开 Dialog 查看。
import { computed, onMounted, ref } from 'vue'
import { RiCloseLine, RiCpuLine, RiHistoryLine, RiEyeLine, RiArrowRightSLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { ansiToHtml } from '@/lib/ansi'
import { useProcessMonitor } from '@/composables/useProcessMonitor'
import { storeToRefs } from 'pinia'
import { useRouter } from 'vue-router'
import { useProcessStore } from '@/stores/processes'
import { useConfirm } from '@/composables/useConfirm'
import type { ProcessInfo } from '@/lib/types'

const { running, history, connected, hasProcesses, start, kill, reconnectCount } =
  useProcessMonitor()

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

// 历史条目过滤 + 展开唯一键。
// - 过滤掉没有日志的空壳记录（无日志的卡片展开后无内容，直接不显示）。
// - 后端旧数据可能存在重复 id（跨重启复用 proc-N），Accordion 用 id 作展开值
//   会因 value 相同导致同名条目同步展开，故用 id+endedAt 组合成唯一键。
function historyKey(p: ProcessInfo): string {
  return p.endedAt ? `${p.id}:${new Date(p.endedAt).getTime()}` : `${p.id}:${p.startedAt}`
}

const historyItems = computed(() => history.value.filter((p) => p.log && p.log.length))

// 角标/空态提示用过滤后的条目数，保证与列表一致。
const historyCount = computed(() => historyItems.value.length)

// 运行中进程日志弹窗（全局）：状态在 processStore，任何组件可 openLog(id) 触发。
const processStore = useProcessStore()
const { viewLogOpen: viewOpen, viewLogProc: viewProc } = storeToRefs(processStore)
// MCP 进程跳转日志面板：用 query 传递目标 server，McpPanel 挂载后消费。
const router = useRouter()

// 从进程名剥离 MCP 前缀。后端 MCP 进程 name 形如 "MCP: github"，日志 API 用 "github"。
function mcpServerName(p: ProcessInfo): string | null {
  if (p.kind !== 'mcp') return null
  const m = p.name.match(/^MCP:\s*(.+)$/)
  return m ? m[1].trim() : null
}

// 点击进程：MCP 进程跳转到日志面板并选中对应 server；其他进程打开全局日志弹窗。
function openView(p: ProcessInfo) {
  const serverName = mcpServerName(p)
  if (serverName) {
    // 用 query 作为跨路由初始信号：McpPanel 挂载后读 query 切 tab 并选中 server。
    // 相比纯 store 信号，query 在「已在 MCP 页 / 从其他页跳来」两种场景都可靠。
    router.push({ name: 'integrations', query: { log: serverName } })
    return
  }
  processStore.openLog(p.id)
}
</script>

<template>
  <div class="border-t border-border">
    <div class="flex items-center justify-between px-2 py-1.5">
      <div class="flex items-center gap-1.5 text-xs text-muted-foreground">
        <RiCpuLine size="14" />
        <span> {{ running.length }} 个进程运行中 </span>
        <span
          class="size-1.5 rounded-full"
          :class="connected ? 'bg-emerald-500' : 'bg-red-500'"
          :title="connected ? '已连接' : '重连中 (' + reconnectCount + ')'"
        />
      </div>
      <div class="flex items-center gap-1">
        <button
          class="relative rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          :title="`历史记录（${historyCount}）`"
          :disabled="historyCount === 0"
          :class="{ 'cursor-not-allowed opacity-40': historyCount === 0 }"
          @click="historyOpen = true"
        >
          <RiHistoryLine size="15" />
          <!-- <span v-if="historyCount"
            class="absolute -right-0.5 -top-0.5 flex min-w-3.5 items-center justify-center rounded-full bg-primary px-0.5 text-[9px] leading-4 text-primary-foreground">
            {{ historyCount > 99 ? '99+' : historyCount }}
          </span> -->
        </button>
      </div>
    </div>

    <template v-if="hasProcesses">
      <!-- 运行中进程 -->
      <div v-if="running.length" class="space-y-1 px-2 pb-1.5">
        <div
          v-for="p in running"
          :key="p.id"
          class="group flex items-center gap-1.5 rounded px-1 py-0.5 text-xs hover:bg-muted"
        >
          <span class="size-1.5 shrink-0 rounded-full" :class="statusDot(p.status)" />
          <button
            class="min-w-0 flex-1 truncate text-left"
            :title="`查看 ${p.name} 日志`"
            @click="openView(p)"
          >
            {{ p.name }}
          </button>
          <span class="shrink-0 tabular-nums text-muted-foreground">{{ fmtMem(p.memBytes) }}</span>
          <button
            class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-muted"
            :title="`查看日志`"
            @click="openView(p)"
          >
            <RiEyeLine size="13" />
          </button>
          <button
            class="shrink-0 rounded p-0.5 text-muted-foreground hover:bg-destructive/10 hover:text-destructive"
            :title="`终止 ${p.name}`"
            @click="onKill(p)"
          >
            <RiCloseLine size="13" />
          </button>
        </div>
      </div>
    </template>
    <p v-else class="px-2 pb-1.5 text-[11px] text-muted-foreground">暂无后台进程</p>

    <!-- 历史记录 Dialog -->
    <Dialog v-model:open="historyOpen">
      <DialogContent class="max-w-5xl!">
        <DialogHeader>
          <DialogTitle>历史进程</DialogTitle>
          <DialogDescription v-if="historyCount === 0">暂无历史记录。</DialogDescription>
        </DialogHeader>
        <div v-if="historyItems.length" class="h-[70vh] flex items-start overflow-y-auto pr-1">
          <Accordion type="multiple" class="w-full">
            <AccordionItem
              v-for="p in historyItems"
              :key="historyKey(p)"
              :value="historyKey(p)"
              class="mb-2 rounded-md !border-b-0 last:mb-0"
            >
              <AccordionTrigger>
                <div class="w-full flex justify-between items-center">
                  <span class="inline-flex items-center">
                    <RiArrowRightSLine
                      class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90"
                    />
                    <span class="inline-flex items-center gap-2">
                      <span class="size-2 shrink-0 rounded-full" :class="statusDot(p.status)" />
                      {{ p.name }}
                    </span>
                    <span
                      class="ml-2 text-xs font-normal"
                      :class="p.status === 'error' ? 'text-red-500' : 'text-muted-foreground'"
                    >
                      {{ statusLabel(p.status) }}
                    </span>
                    <span class="ml-2 text-xs font-normal tabular-nums text-muted-foreground">
                      {{ fmtDuration(p) }}
                    </span>
                  </span>
                  <div class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                    <Badge variant="secondary" class="tabular-nums">{{ fmtStartAt(p) }}</Badge>
                    <Badge variant="secondary" class="tabular-nums"
                      >内存 {{ fmtMem(p.memBytes) }}</Badge
                    >
                    <Badge variant="secondary" class="tabular-nums">PID {{ p.pid || '—' }}</Badge>
                    <Badge v-if="p.exitCode !== undefined" variant="secondary" class="tabular-nums"
                      >退出码 {{ p.exitCode }}</Badge
                    >
                  </div>
                </div>
                <template #icon><span class="hidden" /></template>
              </AccordionTrigger>
              <AccordionContent>
                <div class="space-y-1 text-xs text-muted-foreground">
                  <Badge
                    v-if="p.cmd"
                    variant="outline"
                    class="block w-fit truncate font-mono text-[11px]"
                    :title="p.cmd"
                  >
                    {{ p.cmd }}
                  </Badge>
                  <div
                    v-if="p.log.length"
                    class="mt-1 max-h-60 overflow-y-auto rounded border border-border bg-muted/30 p-2 font-mono text-[11px] leading-relaxed text-muted-foreground"
                  >
                    <div
                      v-for="(line, i) in p.log"
                      :key="i"
                      class="whitespace-pre-wrap break-all"
                      v-html="ansiToHtml(line)"
                    />
                  </div>
                </div>
              </AccordionContent>
            </AccordionItem>
          </Accordion>
        </div>
      </DialogContent>
    </Dialog>

    <!-- 运行中进程日志 Dialog：点击进程行弹出，实时显示日志 -->
    <Dialog v-model:open="viewOpen" @update:open="(o: boolean) => o || processStore.closeLog()">
      <DialogContent class="max-w-5xl!">
        <DialogHeader v-if="viewProc">
          <DialogTitle class="flex items-center gap-2">
            <span class="size-2 rounded-full" :class="statusDot(viewProc.status)" />
            {{ viewProc.name }}
            <span class="text-xs font-normal text-muted-foreground">{{ statusLabel(viewProc.status) }}</span>
          </DialogTitle>
          <DialogDescription class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
            <Badge v-if="viewProc.cmd" variant="outline" class="max-w-full truncate font-mono text-[11px]" :title="viewProc.cmd">
              {{ viewProc.cmd }}
            </Badge>
            <Badge variant="secondary" class="tabular-nums">PID {{ viewProc.pid || '—' }}</Badge>
            <Badge variant="secondary" class="tabular-nums">内存 {{ fmtMem(viewProc.memBytes) }}</Badge>
            <span v-if="viewProc.startedAt" class="text-muted-foreground">已运行 {{ fmtDuration(viewProc) }}</span>
          </DialogDescription>
        </DialogHeader>
        <div class="h-[60vh] overflow-y-auto rounded border border-border bg-muted/30 p-3 font-mono text-[11px] leading-relaxed text-muted-foreground">
          <template v-if="viewProc && viewProc.log.length">
            <div v-for="(line, i) in viewProc.log" :key="i" class="whitespace-pre-wrap break-all" v-html="ansiToHtml(line)" />
          </template>
          <p v-else class="text-muted-foreground">暂无日志输出。</p>
        </div>
      </DialogContent>
    </Dialog>
  </div>
</template>
