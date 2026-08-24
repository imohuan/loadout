<script setup lang="ts">
// McpLogsTab：MCP 会话日志查看器。
// 数据来自 admin-api 三个端点：
//   GET /api/mcp-servers/logs              全部有日志的 server（下拉）
//   GET /api/mcp-servers/{name}/log/files  段文件列表
//   GET /api/mcp-servers/{name}/log?file=&offset=  增量读（offset 为段内字节偏移）
// 策略：尾部 512KB 加载 + 「加载更早」向上翻页（跨段回溯）+ 1s 轮询只追最新段。
import { nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RiArrowUpLine, RiLoader4Line } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { api } from '@/lib/api'

interface LogFileInfo {
  name: string
  size: number
  first_ts?: string
  last_ts?: string
  active?: boolean
}
interface LogServerItem {
  name: string
  transport?: string
  files: LogFileInfo[]
}
interface LogReadResp {
  name: string
  file: string
  offset: number
  size: number
  eof: boolean
  content: string
}

const TAIL_BYTES = 512 * 1024 // 首次加载尾部大小
const PAGE_BYTES = 64 * 1024 // 「加载更早」每次前翻块大小
const POLL_MS = 1000

const servers = ref<LogServerItem[]>([])
const selected = ref('')
const files = ref<LogFileInfo[]>([])
const content = ref('')
const follow = ref(true)
const loading = ref(false)
const errorMsg = ref('')
const preRef = ref<HTMLPreElement | null>(null)

// 最新段（轮询目标）与正文最早处游标（「加载更早」回溯用）
const latestFile = ref('')
let tailOffset = 0
let earliestFile = ''
let earliestOffset = 0
let timer: ReturnType<typeof setInterval> | undefined

const latestInfo = () => files.value[files.value.length - 1]
const canLoadEarlier = () => {
  const i = files.value.findIndex((f) => f.name === earliestFile)
  return earliestOffset > 0 || i > 0
}

async function loadServers() {
  try {
    const resp = await api<{ items: LogServerItem[] }>('/api/mcp-servers/logs')
    servers.value = resp.items || []
    // 默认选中第一个有日志的 server
    if (!selected.value && servers.value.length) {
      selectServer(servers.value[0].name)
    }
  } catch {
    servers.value = []
  }
}

async function selectServer(name: string) {
  selected.value = name
  content.value = ''
  files.value = []
  latestFile.value = ''
  tailOffset = 0
  earliestFile = ''
  earliestOffset = 0
  errorMsg.value = ''
  if (!name) return
  loading.value = true
  try {
    const resp = await api<{ items: LogFileInfo[] }>(
      `/api/mcp-servers/${encodeURIComponent(name)}/log/files`,
    )
    files.value = resp.items || []
    if (!files.value.length) {
      loading.value = false
      return
    }
    const latest = files.value[files.value.length - 1]
    latestFile.value = latest.name
    // 读最新段尾部 TAIL_BYTES
    const start = Math.max(0, latest.size - TAIL_BYTES)
    const data = await readLog(name, latest.name, start)
    content.value = data.content
    tailOffset = data.size
    earliestFile = latest.name
    earliestOffset = start
  } catch (e) {
    errorMsg.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
  }
}

async function readLog(name: string, file: string, offset: number): Promise<LogReadResp> {
  const q = new URLSearchParams({ file, offset: String(offset) })
  return api<LogReadResp>(
    `/api/mcp-servers/${encodeURIComponent(name)}/log?${q.toString()}`,
  )
}

// 「加载更早」：向前翻 64KB；当前段读完（offset=0）则切换到更早一段。
async function loadEarlier() {
  if (!selected.value || loading.value) return
  const i = files.value.findIndex((f) => f.name === earliestFile)
  if (earliestOffset === 0 && i <= 0) return
  loading.value = true
  try {
    let file = earliestFile
    let offset = earliestOffset
    if (offset === 0) {
      // 切到更早一段的尾部
      const prev = files.value[i - 1]
      if (!prev) return
      file = prev.name
      offset = Math.max(0, prev.size - PAGE_BYTES)
    } else {
      offset = Math.max(0, offset - PAGE_BYTES)
    }
    const data = await readLog(selected.value, file, offset)
    const chunk = data.content
    if (chunk) {
      // prepend：正文最早处插入更早内容
      content.value = chunk + content.value
    }
    earliestFile = file
    earliestOffset = offset
    if (content.value && preRef.value && !follow.value) {
      // 向上翻页时保持查看位置（不强制跟随滚动）
      const el = preRef.value
      el.scrollTop = el.scrollHeight - el.scrollTop - el.clientHeight
    }
  } catch (e) {
    // 「加载更早」错误用 toast，不污染主区域；轮询错误也走静默
    toast.error(e instanceof Error ? e.message : String(e))
  } finally {
    loading.value = false
  }
}

async function retryLoad() {
  if (!selected.value) return
  await selectServer(selected.value)
}

// 1s 轮询：只追最新段增量；检测滚动后自动切新段。
async function poll() {
  if (!selected.value || !latestFile.value || loading.value) return
  try {
    const data = await readLog(selected.value, latestFile.value, tailOffset)
    if (data.content) {
      content.value += data.content
      tailOffset = data.size
      scrollFollow()
    }
    // 最新段可能已滚动：files 列表尾部出现新段时切换
    const resp = await api<{ items: LogFileInfo[] }>(
      `/api/mcp-servers/${encodeURIComponent(selected.value)}/log/files`,
    )
    const items = resp.items || []
    if (items.length && items[items.length - 1].name !== latestFile.value) {
      // 旧段已读满（tailOffset == 旧段 size）才切，避免丢内容
      if (tailOffset >= data.size) {
        files.value = items
        latestFile.value = items[items.length - 1].name
        tailOffset = 0
      }
    }
  } catch {
    // 轮询静默失败（server 被删/服务重启），下次继续
  }
}

function scrollFollow() {
  if (!follow.value) return
  nextTick(() => {
    const el = preRef.value
    if (el) el.scrollTop = el.scrollHeight
  })
}

function toggleFollow(v: boolean) {
  follow.value = v
  if (v) scrollFollow()
}

function startPolling() {
  stopPolling()
  timer = setInterval(poll, POLL_MS)
}
function stopPolling() {
  if (timer) {
    clearInterval(timer)
    timer = undefined
  }
}

watch(selected, (v) => {
  if (v) startPolling()
  else stopPolling()
})

onBeforeUnmount(stopPolling)
onMounted(loadServers)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">会话日志</CardTitle>
      <CardDescription>三种连接方式（stdio / Streamable HTTP / SSE）的完整连接与调用日志，实时写入磁盘。</CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <!-- 工具栏 -->
      <div class="flex flex-wrap items-center gap-2">
        <Select :model-value="selected" @update:model-value="selectServer">
          <SelectTrigger class="w-[260px]">
            <SelectValue placeholder="选择 MCP 服务器" />
          </SelectTrigger>
          <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
            <SelectItem v-for="s in servers" :key="s.name" :value="s.name">
              {{ s.name }} · {{ s.transport || '未知传输' }}
            </SelectItem>
          </SelectContent>
        </Select>
        <template v-if="latestInfo()">
          <Badge
            class="border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300"
            variant="outline"
          >
            {{ files.length }} 段
          </Badge>
          <Badge
            class="border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300"
            variant="outline"
          >
            {{ (latestInfo()!.size / 1024).toFixed(1) }} KB
          </Badge>
          <Badge v-if="latestInfo()!.last_ts" variant="outline">最近 {{ latestInfo()!.last_ts }}</Badge>
        </template>
        <div class="ml-auto flex items-center gap-2">
          <Button
            v-if="canLoadEarlier()"
            size="sm"
            variant="outline"
            :disabled="loading"
            @click="loadEarlier"
          >
            <RiArrowUpLine size="14" class="mr-1" />加载更早
          </Button>
          <Label class="text-xs text-muted-foreground">跟随滚动</Label>
          <Switch :model-value="follow" @update:model-value="toggleFollow" />
        </div>
      </div>

      <!-- 初次加载失败（可重试，不污染主区域） -->
      <div
        v-if="errorMsg"
        class="rounded-md border border-dashed p-8 text-center"
      >
        <p class="text-sm text-muted-foreground">加载失败</p>
        <p class="mt-1 text-xs text-muted-foreground/70">{{ errorMsg }}</p>
        <Button size="sm" variant="outline" class="mt-3" :disabled="loading" @click="retryLoad">
          重试
        </Button>
      </div>

      <!-- 空态：无日志 server -->
      <div
        v-else-if="!loading && !servers.length"
        class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
      >
        暂无 MCP 日志
      </div>
      <!-- 空态：已选 server 但无段文件 -->
      <div
        v-else-if="!loading && selected && !files.length"
        class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
      >
        等待 MCP 活动…
      </div>
      <!-- 加载中（无内容时显示，避免空白 pre 突兀） -->
      <div
        v-else-if="loading && !content"
        class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
      >
        <RiLoader4Line class="mx-auto mb-2 size-5 animate-spin" />
        加载中…
      </div>

      <!-- 日志正文 -->
      <div v-else>
        <pre
          v-if="content"
          ref="preRef"
          class="max-h-[calc(100dvh-340px)] overflow-auto whitespace-pre-wrap rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed"
          >{{ content }}</pre
        >
        <div
          v-else
          class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
        >
          暂无日志内容（等待连接或活动）
        </div>
      </div>
    </CardContent>
  </Card>
</template>
