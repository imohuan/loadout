import { computed, onBeforeUnmount, ref } from 'vue'
import { request } from '@/lib/api'
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

  function start() {
    if (es) return
    es = new EventSource('/api/processes/stream')
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
