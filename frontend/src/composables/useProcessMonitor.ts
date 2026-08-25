import { computed, onBeforeUnmount, ref } from 'vue'
import { request } from '@/lib/api'
import { getLoadoutBase, getSseToken } from '@/lib/base'
import type { ProcessEvent, ProcessInfo } from '@/lib/types'

// useProcessMonitor 全局进程 SSE 客户端：连接 /api/processes/stream，
// 维护「运行中 + 历史」进程数组，提供终止指定进程的能力。
// snapshot 事件全量替换，update 事件按 id upsert。
export function useProcessMonitor() {
  const processes = ref<ProcessInfo[]>([])
  const connected = ref(false)
  let es: EventSource | null = null

  function upsert(p: ProcessInfo) {
    const i = processes.value.findIndex((x) => x.id === p.id)
    if (i >= 0) processes.value[i] = p
    else processes.value.unshift(p)
  }

  async function start() {
    if (es) return
    // SSE 必须直连真实 TCP 地址(http://127.0.0.1:<port>)，不能走 wails.localhost 伪 host：
    // Wails 的 WebResourceRequested 拦截会缓冲响应体到完成才回传，而 SSE 永不结束，
    // 导致 EventSource 一直 pending、进程面板显示 0 个进程。
    // 直连时不带 wails.localhost 的登录 Cookie，故用同源换取的短期 sse_token 携带鉴权。
    const base = await getLoadoutBase()
    const token = await getSseToken()
    const q = token ? '?sse_token=' + encodeURIComponent(token) : ''
    es = new EventSource(base + '/api/processes/stream' + q)
    es.onopen = () => {
      connected.value = true
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
    // EventSource 断线自动重连；重连后服务端会再推 snapshot 全量。
    es.onerror = () => {
      connected.value = false
    }
  }

  function stop() {
    es?.close()
    es = null
    processes.value = []
  }

  async function kill(id: string) {
    await request<void>(`/api/processes/${encodeURIComponent(id)}/kill`, 'POST')
  }

  const running = computed(() => processes.value.filter((p) => p.status === 'running'))
  const history = computed(() =>
    processes.value.filter((p) => p.status !== 'running'),
  )
  const hasProcesses = computed(() => processes.value.length > 0)
  const runningCount = computed(() => running.value.length)

  onBeforeUnmount(stop)

  return { processes, running, history, connected, hasProcesses, runningCount, start, kill }
}
