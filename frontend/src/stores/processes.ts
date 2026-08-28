import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { request } from '@/lib/api'
import { getLoadoutBase, getSseToken } from '@/lib/base'
import type { ProcessEvent, ProcessInfo } from '@/lib/types'

// useProcessStore 全局进程状态：单例维护一条 /api/processes/stream SSE 连接，
// 所有组件（ProcessFooter、UnifyaiPanel、SkillsView、依赖卡等）共享同一份进程状态。
// 连接建立后：snapshot 事件全量替换，update 事件按 id upsert。
//
// 断线重连策略（沿用原 useProcessMonitor）：
//  - 依赖 EventSource readyState 识别异常终止，主动关闭并用指数退避重连。
//  - 重连前重新换取短期 sse_token，避免用过期 token 撞服务端。
//  - 超过最大次数停止重连，connected 置 false，交由上层提示。
export const useProcessStore = defineStore('processes', () => {
  const processes = ref<ProcessInfo[]>([])
  const connected = ref(false)
  const reconnectCount = ref(0)

  let es: EventSource | null = null
  let reconnectTimer: ReturnType<typeof setTimeout> | null = null
  let reconnectAttempts = 0
  let started = false

  const MAX_RECONNECT_ATTEMPTS = 5
  const BASE_RECONNECT_DELAY_MS = 1000
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
        scheduleReconnect()
      }
    } catch {
      scheduleReconnect()
    }
  }

  function scheduleReconnect() {
    connected.value = false
    if (reconnectAttempts >= MAX_RECONNECT_ATTEMPTS) {
      closeEs()
      reconnectCount.value = reconnectAttempts
      return
    }
    const delay = Math.min(BASE_RECONNECT_DELAY_MS * 2 ** reconnectAttempts, MAX_RECONNECT_DELAY_MS)
    reconnectAttempts += 1
    reconnectCount.value = reconnectAttempts
    clearReconnectTimer()
    reconnectTimer = setTimeout(() => {
      connect()
    }, delay)
  }

  /** 确保已连接：首次调用建立 SSE；后续调用幂等（已在连则忽略）。 */
  function ensureStarted() {
    if (started) return
    started = true
    reconnectAttempts = 0
    reconnectCount.value = 0
    connect()
  }

  function stop() {
    started = false
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

  /** 按进程 ID 查当前状态；不存在返回 null。 */
  function byId(id: string): ProcessInfo | undefined {
    return processes.value.find((p) => p.id === id)
  }

  /** 某进程是否仍在运行（不存在视为未运行）。 */
  function isRunning(id: string): boolean {
    return processes.value.some((p) => p.id === id && p.status === 'running')
  }

  /** 某进程是否已结束（done/error）；快照里没有该 id 时按「历史已清空」处理，返回 undefined 让调用方判断。 */
  function settledOf(id: string): 'done' | 'error' | 'missing' | 'running' {
    const p = processes.value.find((x) => x.id === id)
    if (!p) return 'missing'
    if (p.status === 'running') return 'running'
    return p.status === 'done' ? 'done' : 'error'
  }

  // ---- 全局日志查看弹窗（任何组件可 openLog(id) 触发）----
  const viewLogOpen = ref(false)
  const viewLogId = ref<string | null>(null)
  /** 从实时进程列表解析当前查看的进程；SSE 更新时自动反映新日志。 */
  const viewLogProc = computed(() => processes.value.find((p) => p.id === viewLogId.value) ?? null)
  /** 打开某个进程的日志弹窗（任务进行中日志实时更新）。 */
  function openLog(id: string) {
    ensureStarted() // 确保进程流已连接，弹窗打开后能实时收到日志
    viewLogId.value = id
    viewLogOpen.value = true
  }
  /** 关闭日志弹窗。 */
  function closeLog() {
    viewLogOpen.value = false
    viewLogId.value = null
  }

  const running = computed(() => processes.value.filter((p) => p.status === 'running'))
  const history = computed(() => processes.value.filter((p) => p.status !== 'running'))
  const hasProcesses = computed(() => processes.value.length > 0)
  const runningCount = computed(() => running.value.length)
  const reconnectFailed = computed(() => reconnectCount.value >= MAX_RECONNECT_ATTEMPTS)

  return {
    processes,
    running,
    history,
    connected,
    hasProcesses,
    runningCount,
    reconnectCount,
    reconnectFailed,
    ensureStarted,
    stop,
    kill,
    byId,
    isRunning,
    settledOf,
    viewLogOpen,
    viewLogId,
    viewLogProc,
    openLog,
    closeLog,
  }
})
