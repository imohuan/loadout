<script setup lang="ts">
// McpLogsTab：MCP 会话日志查看器。
// 数据来自 admin-api 三个端点：
//   GET /api/mcp-servers/logs              全部有日志的 server（下拉）
//   GET /api/mcp-servers/{name}/log/files  段文件列表
//   GET /api/mcp-servers/{name}/log?file=&offset=  增量读（offset 为段内字节偏移）
// 策略：尾部 512KB 加载 + 「加载更早」向上翻页（跨段回溯）+ 1s 轮询只追最新段。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RiArrowUpLine, RiClipboardLine, RiCloseLine, RiLoader4Line } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { api } from '@/lib/api'
import { ansiToHtml } from '@/lib/ansi'
import { useMcpNavStore } from '@/stores/mcpNavigation'
import AxJsonViewer from '@/components/ui/AxJsonViewer.vue'
import {
  parseLogLine,
  isFrameLine,
  decodeJson,
  frameId,
  normalizeBody,
  splitCompleteLines,
  stripLeadingPartialLine,
  type LogLine,
} from '@/lib/mcpLogs'

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
  /** 本次实际读到的末尾字节偏移：游标必须用它推进，不能用 size（首屏只读尾部）。 */
  next_offset: number
  size: number
  eof: boolean
  content: string
}


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

// 完整内容弹窗：点哪一条就只显示哪一条。
// 曾经尝试按 JSON-RPC id 自动配对「请求→响应」，但重连后 id 会重复，
// 而且一次调用可能收到多条结果（进度通知、双份输出），配对结果并不可靠，
// 反而误导人。这里改成只展示被点中的这一条，完全不做推断。
const detailOpen = ref(false)
const detailLine = ref<LogLine | null>(null)

function openDetail(line: LogLine) {
  detailLine.value = line
  detailOpen.value = true
}

// frameLabel 给帧行一个人类可读的名字：tools/call → tools/call。
// 通知（没有 method）退回日志类型名。
function frameLabel(line: LogLine | null | undefined): string {
  if (!line) return ''
  const msg = decodeJson(line) as { method?: string } | null
  if (msg && typeof msg.method === 'string') return msg.method
  return line.kind || '日志'
}

// detailJson 把选中的这一条解析成对象，供 AxJsonViewer 渲染。
const detailJson = computed(() => (detailLine.value ? decodeJson(detailLine.value) : null))

// detailId 当前这条日志的 JSON-RPC id（没有则为空串）。
const detailId = computed(() => frameId(detailJson.value))

// displayBody 列表里显示的正文：去掉 msg="..." 外壳与 \" 转义，
// 让每行直接是可读 JSON（比如 {"jsonrpc":"2.0",...}）。
function displayBody(line: LogLine): string {
  const text = normalizeBody(line.body)
  // 超长帧在列表里只占一行，这里仍截断显示，完整内容点开看。
  return isFrameLine(line) && text.length > 400 ? text.slice(0, 400) + '…' : text
}

// copyDetail 复制弹窗里的完整正文（与弹窗看到的一致：已去掉转义、缩进展开）。
async function copyDetail() {
  try {
    await navigator.clipboard.writeText(prettyBody(detailLine.value))
    toast.success('已复制完整内容')
  } catch (e) {
    toast.error('复制失败', { description: e instanceof Error ? e.message : String(e) })
  }
}

// prettyBody 弹窗正文：先去掉 msg="..." 外壳与转义，能解析成 JSON 就缩进展开，
// 否则原样显示（stderr / connect 等非 JSON 行）。这样界面上看不到 \" 这类噪音。
function prettyBody(line: LogLine | null): string {
  if (!line) return ''
  const decoded = normalizeBody(line.body)
  try {
    return JSON.stringify(JSON.parse(decoded), null, 2)
  } catch {
    return decoded
  }
}

const lines = computed<LogLine[]>(() => {
  if (!content.value) return []
  return content.value
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map(parseLogLine)
})

// 跨块读取时尾部可能是被切断的半行。日志行都以 \n 结束，所以没有换行符的
// 尾段一定还没写完；直接渲染就会看到半截 JSON（用户说的「被截取」）。
// 未完成的行留在 pendingTail，等下一块字节补齐再显示（切行逻辑见 lib/mcpLogs）。
const pendingTail = ref('')
function appendChunk(chunk: string) {
  if (!chunk) return
  const { complete, rest } = splitCompleteLines(pendingTail.value, chunk)
  content.value += complete
  pendingTail.value = rest
}

const TAIL_BYTES = 512 * 1024 // 首次加载尾部大小
const PAGE_BYTES = 64 * 1024 // 「加载更早」每次前翻块大小
// 首次加载最多读多少字节（服务端单次上限 1MB）：首屏一次读满，避免被 64KB 默认值截断。
const READ_BYTES = 1024 * 1024
const POLL_MS = 1000

const servers = ref<LogServerItem[]>([])
const selected = ref('')
const files = ref<LogFileInfo[]>([])
const content = ref('')
const follow = ref(true)
const loading = ref(false)
const errorMsg = ref('')
const preRef = ref<HTMLElement | null>(null)

// 外部跳转信号（ProcessFooter 点击 MCP 进程）：选中对应 server 并消费请求。
const mcpNav = useMcpNavStore()

// 消费待选中的 server 名：servers 已加载且名字存在时才 selectServer，随后清空防止重复触发。
// 返回是否已消费跳转请求（供 loadServers 决定是否回退到默认第一个 server）。
function applyPending(): boolean {
  const target = mcpNav.pendingServer
  if (!target) return false
  const exists = servers.value.some((s) => s.name === target)
  if (!exists) return false
  mcpNav.consume()
  if (selected.value !== target) selectServer(target)
  return true
}

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
    applyPending()
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
    // 首屏从段中间开始读，第一行可能只有后半截：丢掉它（前面用「加载更早」补）。
    const initial = stripLeadingPartialLine(data.content, start === 0)
    pendingTail.value = ''
    content.value = ''
    appendChunk(initial)
    // 游标用 next_offset（本次真正读到的末尾），不能用 size：
    // 段文件可能远大于单次读取上限，直接跳 size 会跳过中间内容。
    tailOffset = data.next_offset
    earliestFile = latest.name
    earliestOffset = start
  } catch (e) {
    errorMsg.value = e instanceof Error ? e.message : String(e)
  } finally {
    loading.value = false
    // 加载完成自动滚动到底部（若加载了内容）。
    if (content.value) scrollFollow(true)
  }
}

async function readLog(
  name: string,
  file: string,
  offset: number,
  limit = READ_BYTES,
): Promise<LogReadResp> {
  // 必须显式带 limit：服务端默认只回 64KB。首屏要读尾部 512KB 时不带 limit
  // 就只拿到一小段，而游标若直接跳到文件尾，中间内容会被整个跳过 ——
  // 这正是「日志被截断、最后一行只有半截」的根因。
  const q = new URLSearchParams({ file, offset: String(offset), limit: String(limit) })
  return api<LogReadResp>(`/api/mcp-servers/${encodeURIComponent(name)}/log?${q.toString()}`)
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
    // 向前翻页只取一块（PAGE_BYTES），不要用首屏的 1MB。
    const data = await readLog(selected.value, file, offset, PAGE_BYTES)
    // 向上翻页时本块末尾可能停在半行（下一块的起点就在它后面），
    // 这里截到最后一个换行，保证拼接后不会出现断成两截的 JSON。
    let chunk = data.content
    // 未读到段尾说明这块是按 limit 切断的，末尾可能是半行 → 截到最后一个换行。
    if (!data.eof) {
      const cut = chunk.lastIndexOf('\n')
      chunk = cut >= 0 ? chunk.slice(0, cut + 1) : ''
    }
    if (chunk) content.value = chunk + content.value
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

// 1s 轮询：只追最新段增量；同步刷新段列表（徽章的 KB/最近时间读这里）；
// 检测滚动后自动切新段。
async function poll() {
  if (!selected.value || !latestFile.value || loading.value) return
  try {
    const data = await readLog(selected.value, latestFile.value, tailOffset)
    if (data.content) {
      appendChunk(data.content)
      tailOffset = data.next_offset
      scrollFollow()
    }
    // 最新段可能已滚动：files 列表尾部出现新段时切换
    // 每次轮询刷新 files：badges（size/last_ts）依赖它，滚动/新段检测也依赖它。
    const resp = await api<{ items: LogFileInfo[] }>(
      `/api/mcp-servers/${encodeURIComponent(selected.value)}/log/files`,
    )
    const items = resp.items || []
    if (items.length) {
      const known = files.value[files.value.length - 1]
      const fresh = items[items.length - 1]
      // 同一段文件大小/时间变化 → 原地更新徽章；出现新段 → 稍后滚动检测统一处理。
      if (known && fresh.name === known.name &&
          (fresh.size !== known.size || fresh.last_ts !== known.last_ts)) {
        files.value = items
      } else if (!known || fresh.name !== known.name) {
        // 新段：列表先更新（后续分支负责切换 tailOffset）。
        files.value = items
      }
    }
    if (items.length && items[items.length - 1].name !== latestFile.value) {
      // 旧段必须读到段尾（服务端 eof）才切下一段，否则中间内容会丢。
      if (data.eof) {
        files.value = items
        latestFile.value = items[items.length - 1].name
        tailOffset = 0
      }
    }
  } catch {
    // 轮询静默失败（server 被删/服务重启），下次继续
  }
}

function scrollFollow(force = false) {
  if (!follow.value && !force) return
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

// 外部跳转请求：ProcessFooter 点击 MCP 进程时触发（pendingServer + requestId 组合监听，
// 保证同名 server 重复点击也能触发）。
watch(
  () => [mcpNav.pendingServer, mcpNav.requestId] as const,
  () => applyPending(),
)
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
      <CardDescription
        >三种连接方式（stdio / Streamable HTTP /
        SSE）的完整连接与调用日志，实时写入磁盘。</CardDescription
      >
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
          <Badge v-if="latestInfo()!.last_ts" variant="outline"
            >最近 {{ latestInfo()!.last_ts }}</Badge
          >
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
          <Label class="text-xs text-muted-foreground">跟随滚动</Label>
          <Switch :model-value="follow" @update:model-value="toggleFollow" />
        </div>
      </div>

      <!-- 初次加载失败（可重试，不污染主区域） -->
      <div v-if="errorMsg" class="rounded-md border border-dashed p-8 text-center">
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

      <!-- 日志正文：纯文本行，跟随滚动查看 -->
      <div v-else>
        <!-- 纯文本模式 -->
        <div
          v-if="content"
          ref="preRef"
          class="log-lines max-h-[calc(100dvh-340px)] overflow-auto rounded-md border bg-muted/40 py-1 font-mono text-xs leading-relaxed"
        >
          <div
            v-for="(line, i) in lines"
            :key="i"
            class="log-line px-3 py-0.5 hover:bg-muted/40"
            :class="isFrameLine(line) ? 'truncate' : 'whitespace-pre-wrap break-all'"
          >
            <span class="text-muted-foreground/50 tabular-nums">{{ line.ts }}</span>
            <span
              v-if="line.kind"
              class="px-2 font-semibold"
              :class="KIND_CLASS[line.kind] || 'text-muted-foreground'"
              >[{{ line.kind }}]</span
            ><!-- 整行文字：悬停显示下划线，点击打开完整内容（不再放行尾按钮）。
                正文先去掉 msg="..." 外壳与转义，列表里直接是可读的 JSON。 -->
            <span
              class="cursor-pointer decoration-dotted underline-offset-2 hover:underline"
              title="点击查看完整内容"
              @click="openDetail(line)"
              v-html="ansiToHtml(displayBody(line))"
            />
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

  <!-- 完整内容弹窗 -->
  <Dialog v-model:open="detailOpen">
    <DialogContent
      class="flex h-[86vh] w-[94vw] max-w-[94vw]! flex-col gap-0 overflow-hidden p-0 sm:max-w-[94vw]!"
      :show-close-button="false"
    >
      <DialogHeader
        class="flex shrink-0 flex-row items-start gap-2 border-b border-border px-4 py-2.5"
      >
        <div class="min-w-0 flex-1">
          <DialogTitle class="truncate text-base">
            {{ frameLabel(detailLine) }}
          </DialogTitle>
          <DialogDescription class="truncate font-mono text-xs">
            {{ detailLine?.ts }} {{ detailLine ? '[' + detailLine.kind + ']' : '' }}
            <template v-if="detailId"> · id={{ detailId }} </template>
          </DialogDescription>
        </div>
        <Button variant="ghost" size="icon" aria-label="复制完整内容" @click="copyDetail">
          <RiClipboardLine class="size-4" />
        </Button>
        <Button variant="ghost" size="icon" aria-label="关闭" @click="detailOpen = false">
          <RiCloseLine class="size-4" />
        </Button>
      </DialogHeader>
      <!-- 只显示被点中的这一条：不做请求/响应配对，避免顺序错乱误导阅读。 -->
      <div class="min-h-0 flex-1 overflow-auto bg-muted/20 px-4 py-3">
        <AxJsonViewer v-if="detailJson !== null" :data="detailJson" :expand-level="6" wrap-enabled />
        <pre v-else class="whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">{{
          prettyBody(detailLine)
        }}</pre>
      </div>
    </DialogContent>
  </Dialog>
</template>
