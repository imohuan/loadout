import { computed, onBeforeUnmount, ref } from 'vue'
import { request } from '@/lib/api'
import { getLoadoutBase, getSseToken } from '@/lib/base'
import type { ProcessEvent, ProcessInfo } from '@/lib/types'

// useProcessMonitor 全局进程 SSE 客户端：连接 /api/processes/stream，
// 维护「运行中 + 历史」进程数组，提供终止指定进程的能力。
// snapshot 事件全量替换，update 事件按 id upsert。
//
// 断线重连策略：
//  - 依赖 EventSource 的 readyState 主动识别异常终止，而非只看 onerror。
//  - 网络闪断 / 服务端重启 / HTTP 错误(非 200)时主动关闭并用指数退避重连。
//  - 每次重连前重新换取短期 sse_token（有效期 5 分钟），避免用过期的 token 撞服务端被拒。
//  - 超过最大重试次数后停止自动重连，把 connected 置 false，交由上层提示「重连失败，请手动刷新」。
export function useProcessMonitor() {
  const processes = ref<ProcessInfo[]>([])
  const connected = ref(false)
  const reconnectCount = ref(0)

  let es: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  const MAX_RECONNECT_ATTEMPTS = 5
  const BASE_RECONNECT_DELAY_MS = 1000
  // 指数退避：1s -> 2s -> 4s -> 8s -> 16s（封顶，避免无限打爆服务端）
  const MAX_RECONNECT_DELAY_MS = 16000

  function upsert(p: ProcessInfo) {
    const i = processes.value.findIndex((x) => x.id === p.id)
    if (i >= 0) processes.value[i] = p
    else processes.value.unshift(p)
  }

  function clearReconnectTimer() {
    if (reconnectTimer) {
      clearTimeout(reconnectTimer)
      reconnectTimer = null
    }
  }

  function closeEs() {
    es?.close()
    es = null
  }

  // 真正建立连接；每次调用前都会重新取 base + token，保证重连用新鲜凭证。
  async function connect() {
    clearReconnectTimer()
    closeEs()
    try {
      const base = await getLoadoutBase()
      const token = await getSseToken()
      const q = token ? '?sse_token=' + encodeURIComponent(token) : ''
      const url = base + '/api/processes/stream' + q

      es = new EventSource(url)
      es.onopen = () => {
        connected.value = true
        // 连接成功即复位退避计数，下次闪断从 1s 重新开始。
        reconnectAttempts = 0
        reconnectCount.value = 0
      }
      es.onmessage = (e) => {
        let ev: ProcessEvent
        try {
          ev = JSON.parse(e.data as string)
        } catch {
          return
        }
        if (ev.type === 'snapshot') processes.value = ev.data || []
        else if (ev.type === 'update' && ev.data?.[0]) upsert(ev.data[0])
      }
      es.onerror = () => {
        // EventSource 在 CONNECTING(0) 且非主动 close 时，说明连接失败/异常终止，需主动重连。
        // 这里不强依赖 readyState，因为部分环境 onerror 触发时 readyState 可能仍为 OPEN，
        // 统一走到 scheduleReconnect 由它判断是否还有重试额度。
        scheduleReconnect()
      }
    } catch {
      scheduleReconnect()
    }
  }

  // 断线后的统一调度：指数退避 + 限量重试。
  function scheduleReconnect() {
    connected.value = false
    if (reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      // 达到上限：停止自动重连，暴露失败次数，由上层决定是否手动重连。
      closeEs()
      reconnectCount.value = reconnectAttempts
      return
    }
    const delay = Math.min(BASE_RECONNECT_DELAY_MS * 2 ** reconnectAttempts, MAX_RECONNECT_DELAY_MS)
    reconnectAttempts += 1
    reconnectCount.value = reconnectAttempts
    clearReconnectTimer()
    reconnectTimer = setTimeout(() => {
      // 重新获取 base/token 后重连
      connect()
    }, delay)
  }

  async function start() {
    // 手动启动：复位退避状态，避免用户主动重连时被上次的上限卡死。
    reconnectAttempts = 0
    reconnectCount.value = 0
    await connect()
  }

  function stop() {
    clearReconnectTimer()
    closeEs()
    reconnectAttempts = 0
    reconnectCount.value = 0
    processes.value = []
    connected.value = false
  }

  async function kill(id: string) {
    await request<void>(`/api/processes/${encodeURIComponent(id)}/kill`, 'POST')
  }

  const running = computed(() => processes.value.filter((p) => p.status === 'running'))
  const history = computed(() => processes.value.filter((p) => p.status !== 'running'))
  const hasProcesses = computed(() => processes.value.length > 0)
  const runningCount = computed(() => running.value.length)
  const reconnectFailed = computed(() => reconnectCount.value >= MAX_RECONNECT_ATTEMPTS)

  onBeforeUnmount(stop)

  return {
    processes,
    running,
    history,
    connected,
    hasProcesses,
    runningCount,
    reconnectCount,
    reconnectFailed,
    start,
    kill,
  }
}
