<script setup lang="ts">
// McpLogsTab：MCP 会话日志查看器。
// 数据来自 admin-api 三个端点：
//   GET /api/mcp-servers/logs              全部有日志的 server（下拉）
//   GET /api/mcp-servers/{name}/log/files  段文件列表
//   GET /api/mcp-servers/{name}/log?file=&offset=  增量读（offset 为段内字节偏移）
// 策略：尾部 512KB 加载 + 「加载更早」向上翻页（跨段回溯）+ 1s 轮询只追最新段。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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

// 日志行（解析后）：时间戳 + [KIND] + 正文（正文原样纯文本，不解析 JSON）。
interface LogLine {
  ts: string
  kind: string
  body: string
}

// 卡片视图一行：一次工具调用（method=tools/call 的请求帧）。
interface ToolCallCard {
  tool: string // 工具名（卡片标题）
  args: string // 格式化后的 arguments JSON（卡片主体）
  raw: string // 完整原始行（弹框展示）
  msgJson?: string // 完整 msg JSON 格式化串（弹框可选展示）
  ts: string
  kind: string
  id?: string
}

// 视图模式：text=纯文本（当前模式）/ cards=卡片可视化。
const viewMode = ref<'text' | 'cards'>('text')

// [KIND] 染色映射（Tailwind token，与 Badge tint 风格一致）。
const KIND_CLASS: Record<string, string> = {
  CONNECT: 'text-blue-600 dark:text-blue-400',
  CONNECT_OK: 'text-emerald-600 dark:text-emerald-400',
  CONNECT_FAIL: 'text-red-600 dark:text-red-400',
  DISCONNECT: 'text-slate-500',
  FRAME_IN: 'text-violet-600 dark:text-violet-400',
  FRAME_OUT: 'text-amber-600 dark:text-amber-400',
  STDERR: 'text-orange-600 dark:text-orange-400',
}

// Badge 低饱和 tint（与全局 Badge 规范一致）。
const KIND_BADGE: Record<string, string> = {
  CONNECT: 'border-blue-500/20 bg-blue-500/15 text-blue-700 dark:text-blue-300',
  CONNECT_OK: 'border-emerald-500/20 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  CONNECT_FAIL: 'border-red-500/20 bg-red-500/15 text-red-700 dark:text-red-300',
  DISCONNECT: 'border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300',
  FRAME_IN: 'border-violet-500/20 bg-violet-500/15 text-violet-700 dark:text-violet-300',
  FRAME_OUT: 'border-amber-500/20 bg-amber-500/15 text-amber-700 dark:text-amber-300',
  STDERR: 'border-orange-500/20 bg-orange-500/15 text-orange-700 dark:text-orange-300',
}

// goUnquote 把 Go strconv.Quote 转义的字符串还原（仅用于卡片提取 msg JSON）。
function goUnquote(s: string): string {
  return s
    .replace(/\\u[0-9a-fA-F]{4}/g, (u) => String.fromCharCode(parseInt(u.slice(2), 16)))
    .replace(/\\(["\\/bfnrt])/g, (_m, c: string) =>
      c === 'b' ? '\b' : c === 'f' ? '\f' : c === 'n' ? '\n' : c === 'r' ? '\r' : c === 't' ? '\t' : c,
    )
}

// parseToolCall 从正文提取工具调用帧（method=tools/call）：非调用帧返回 null。
// 返回工具名（卡片标题）+ 格式化后的 arguments JSON（卡片主体）。
function parseToolCall(body: string, ts: string, kind: string): ToolCallCard | null {
  const m = body.match(/ msg="((?:[^"\\]|\\.)*)"/)
  if (!m) return null
  let obj: Record<string, any>
  try {
    obj = JSON.parse(goUnquote(m[1]))
  } catch {
    return null
  }
  if (obj.method !== 'tools/call' || !obj.params || typeof obj.params.name !== 'string') {
    return null
  }
  return {
    tool: obj.params.name,
    args: JSON.stringify(obj.params.arguments ?? {}, null, 2),
    raw: [ts, kind ? `[${kind}]` : '', body].join(' ').replace(/\s+/g, ' ').trim(),
    msgJson: JSON.stringify(obj, null, 2),
    ts,
    kind,
    id: obj.id !== undefined ? String(obj.id) : undefined,
  }
}

// parseLogLine 用正则拆一行：`2026-08-24T18:35:25.645+08:00 [FRAME_IN] msg="..."`。
// 只提取时间戳与 [KIND] 用于染色，正文原样保留（纯文本，不解析 JSON）。
function parseLogLine(line: string): LogLine {
  const m = line.match(/^(\S+) \[([^\]]+)\] (.*)$/)
  if (!m) {
    return { ts: '', kind: '', body: line }
  }
  return { ts: m[1], kind: m[2], body: m[3] }
}

const lines = computed<LogLine[]>(() => {
  if (!content.value) return []
  return content.value
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map(parseLogLine)
})

// 卡片模式：只展示工具调用帧（tools/call 请求），其余日志行过滤。
const cardLines = computed<ToolCallCard[]>(() =>
  lines.value
    .map((l) => parseToolCall(l.body, l.ts, l.kind))
    .filter((c): c is ToolCallCard => c !== null),
)

// 详情弹框状态
const detailOpen = ref(false)
const detailLine = ref<ToolCallCard | null>(null)
const showFormatted = ref(false)

function openDetail(c: ToolCallCard) {
  detailLine.value = c
  showFormatted.value = false
  detailOpen.value = true
}

async function copyDetail() {
  if (!detailLine.value) return
  try {
    await navigator.clipboard.writeText(detailLine.value.raw)
    toast.success('已复制日志行')
  } catch {
    toast.error('复制失败')
  }
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
const preRef = ref<HTMLElement | null>(null)

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
        <div class="ml-auto flex items-center gap-3">
          <Button
            v-if="canLoadEarlier()"
            size="sm"
            variant="outline"
            :disabled="loading"
            @click="loadEarlier"
          >
            <RiArrowUpLine size="14" class="mr-1" />加载更早
          </Button>
          <div class="flex items-center gap-0.5 rounded-md border p-0.5">
            <Button
              size="sm"
              variant="ghost"
              :class="viewMode === 'text' ? 'bg-muted' : ''"
              @click="viewMode = 'text'"
            >
              文本
            </Button>
            <Button
              size="sm"
              variant="ghost"
              :class="viewMode === 'cards' ? 'bg-muted' : ''"
              @click="viewMode = 'cards'"
            >
              卡片
            </Button>
          </div>
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

      <!-- 日志正文：text=纯文本行（当前模式）；cards=卡片可视化，点击弹框看详情 -->
      <div v-else>
        <!-- 纯文本模式 -->
        <div
          v-if="content && viewMode === 'text'"
          ref="preRef"
          class="log-lines max-h-[calc(100dvh-340px)] overflow-auto rounded-md border bg-muted/40 py-1 font-mono text-xs leading-relaxed"
        >
          <div
            v-for="(line, i) in lines"
            :key="i"
            class="log-line whitespace-pre-wrap break-all px-3 py-0.5 hover:bg-muted/40"
          >
            <span class="text-muted-foreground/50 tabular-nums">{{ line.ts }}</span>
            <span
              v-if="line.kind"
              class="px-2 font-semibold"
              :class="KIND_CLASS[line.kind] || 'text-muted-foreground'"
              >[{{ line.kind }}]</span
            >{{ line.body }}
          </div>
        </div>
        <!-- 卡片模式：只显示工具调用（tools/call），卡片名=工具名，下方=解析后的参数 JSON -->
        <div
          v-else-if="content && viewMode === 'cards'"
          ref="preRef"
          class="max-h-[calc(100dvh-340px)] space-y-1.5 overflow-auto rounded-md border bg-muted/40 p-2"
        >
          <div v-if="cardLines.length" class="space-y-1.5">
            <button
              v-for="(c, i) in cardLines"
              :key="i"
              type="button"
              class="w-full rounded-md border bg-card px-3 py-2 text-left hover:bg-muted/60"
              @click="openDetail(c)"
            >
              <div class="flex items-center gap-2 text-sm">
                <span class="shrink-0 font-mono font-semibold">{{ c.tool }}</span>
                <Badge variant="outline" class="shrink-0" :class="KIND_BADGE[c.kind] || ''">{{ c.kind }}</Badge>
                <span class="ml-auto shrink-0 text-xs text-muted-foreground/60 tabular-nums">{{ c.ts }}</span>
              </div>
              <pre
                class="mt-1.5 max-h-40 overflow-auto whitespace-pre rounded-md border bg-muted/30 p-2 font-mono text-xs leading-relaxed"
                >{{ c.args }}</pre
              >
            </button>
          </div>
          <div
            v-else
            class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
          >
            暂无工具调用记录
          </div>
        </div>
        <div
          v-else
          class="rounded-md border border-dashed p-8 text-center text-sm text-muted-foreground"
        >
          暂无日志内容（等待连接或活动）
        </div>
      </div>
    </CardContent>
  </Card>

  <!-- 卡片详情弹框 -->
  <Dialog v-model:open="detailOpen">
    <DialogContent
      class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl"
    >
      <DialogHeader>
        <DialogTitle class="flex items-center gap-2 text-sm">
          <span class="font-mono font-semibold">{{ detailLine?.tool }}</span>
          <Badge variant="outline" :class="KIND_BADGE[detailLine?.kind || ''] || ''">{{ detailLine?.kind }}</Badge>
          <span v-if="detailLine?.id" class="font-mono text-muted-foreground">id={{ detailLine?.id }}</span>
        </DialogTitle>
      </DialogHeader>
      <div class="space-y-3">
        <div class="flex items-center justify-between gap-2">
          <span class="text-xs text-muted-foreground tabular-nums">{{ detailLine?.ts }}</span>
          <div class="flex items-center gap-2">
            <Button size="sm" variant="outline" @click="copyDetail">复制</Button>
          </div>
        </div>
        <pre
          class="max-h-96 overflow-auto whitespace-pre-wrap break-all rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed"
          >{{ detailLine?.raw }}</pre
        >
        <template v-if="detailLine?.msgJson">
          <div class="flex items-center justify-between">
            <span class="text-xs text-muted-foreground">格式化 JSON</span>
            <Switch :model-value="showFormatted" @update:model-value="(v: boolean) => (showFormatted = v)" />
          </div>
          <pre
            v-if="showFormatted"
            class="max-h-96 overflow-auto whitespace-pre rounded-md border bg-muted/40 p-3 font-mono text-xs leading-relaxed"
            >{{ detailLine.msgJson }}</pre
          >
        </template>
      </div>
    </DialogContent>
  </Dialog>
</template>


