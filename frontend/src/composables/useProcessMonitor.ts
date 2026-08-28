import { computed } from 'vue'
import { useProcessStore } from '@/stores/processes'

// useProcessMonitor 全局进程状态薄封装：直接消费 Pinia 单例 store（useProcessStore）。
// 保持历史 API 形态（所有返回值是 Ref，组件用 .value 访问），
// 让 ProcessFooter / UnifyaiPanel 等既有组件无需改动即可共享同一条 SSE 连接。
export function useProcessMonitor() {
  const store = useProcessStore()

  return {
    processes: computed(() => store.processes),
    running: computed(() => store.running),
    history: computed(() => store.history),
    connected: computed(() => store.connected),
    hasProcesses: computed(() => store.hasProcesses),
    runningCount: computed(() => store.runningCount),
    reconnectCount: computed(() => store.reconnectCount),
    reconnectFailed: computed(() => store.reconnectFailed),
    start: () => store.ensureStarted(),
    stop: () => store.stop(),
    kill: (id: string) => store.kill(id),
  }
}
