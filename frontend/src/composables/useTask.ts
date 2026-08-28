import { computed, onBeforeUnmount, watch } from 'vue'
import { useProcessStore } from '@/stores/processes'

// useTask —— 统一后台任务 hooks。
//
// 背景：skill 更新 / unifyai 同步 / deps 安装等「经 procreg 启动的第三方命令」结束后，
// 前端需要即时恢复按钮加载态并执行收尾（如刷新列表）。本 hooks 用「注册 + 触发 + 监听」
// 三件套统一这套逻辑，所有页面共用：
//
//   1. 注册（页面挂载时）：useTask(id, spec) 把「跑完要干嘛」（onDone/onError）登记到全局注册表。
//   2. 触发（点击按钮）：startTask(id, run) 生成 id、写 localStorage、POST 启动。
//   3. 监听（全局 SSE）：进程流按 id 匹配，done/error 时自动调注册表的收尾。
//
// 刷新兜底：localStorage 保存未结束的 task id；页面重新挂载会重新注册（回调回到内存），
// 监听器在收到 snapshot 时按 id 对账——已在快照里结束的任务立即补跑收尾。

export interface TaskSpec {
  /** 匹配 procreg 进程的 kind（skill | unifyai | dep） */
  kind: string
  /** 进程名（展示用，可空） */
  name?: string
  /** 任务成功结束时的收尾动作（如刷新列表） */
  onDone?: () => void
  /** 任务失败结束时的收尾动作 */
  onError?: (err: unknown) => void
}

// 全局任务注册表：taskId -> spec。回调不能序列化，放内存；由页面重新注册恢复。
const registry = new Map<string, TaskSpec>()

const PENDING_KEY = 'loadout.pendingTasks'

interface PendingTask {
  id: string
  kind: string
  name?: string
  startedAt: number
}

// ---------- localStorage 待办任务持久化 ----------
function readPending(): PendingTask[] {
  try {
    const raw = localStorage.getItem(PENDING_KEY)
    if (!raw) return []
    const list = JSON.parse(raw) as PendingTask[]
    return Array.isArray(list) ? list : []
  } catch {
    return []
  }
}

function writePending(list: PendingTask[]) {
  try {
    if (list.length) localStorage.setItem(PENDING_KEY, JSON.stringify(list))
    else localStorage.removeItem(PENDING_KEY)
  } catch {
    /* localStorage 不可用（隐私模式等）时静默降级 */
  }
}

function markPending(id: string, kind: string, name?: string) {
  const list = readPending().filter((p) => p.id !== id)
  list.push({ id, kind, name, startedAt: Date.now() })
  writePending(list)
}

function clearPending(id: string) {
  writePending(readPending().filter((p) => p.id !== id))
}

/** 全局已注册的任务 id 集合（供监听器遍历）。 */
export function registeredTaskIds(): string[] {
  return [...registry.keys()]
}

export function getTaskSpec(id: string): TaskSpec | undefined {
  return registry.get(id)
}

/** 全局注册任务收尾（无需组件上下文）。重复注册覆盖，刷新后由页面重新注册恢复。 */
export function registerTask(id: string, spec: TaskSpec) {
  const prev = registry.get(id)
  registry.set(id, { ...(prev ?? {}), ...spec, kind: spec.kind })
  return registry.get(id)!
}

/** 立即结束一个任务（POST 已返回终态时调用，避免监听器重复回调）。 */
export function clearTask(id: string) {
  clearPending(id)
  registry.delete(id)
}

// ---------- 触发 ----------
let started = false

/**
 * 启动一个后台任务。点击按钮时调用：
 *   await startTask({ id, kind, run })
 * run 负责实际发起后端任务（POST），并把生成的 task id 一并带给后端进程。
 */
export async function startTask<T = unknown>(opts: {
  id: string
  kind: string
  name?: string
  run: () => Promise<T>
}): Promise<T> {
  initTaskListener()
  markPending(opts.id, opts.kind, opts.name)
  return opts.run()
}

/** 全局监听器：确保只挂一次，进程流结束事件驱动注册表回调。 */
export function initTaskListener() {
  if (started) return
  started = true
  const store = useProcessStore()
  store.ensureStarted()

  watch(
    () => store.processes,
    () => {
      // ① 实时：遍历已注册任务，按 id 对账
      for (const id of [...registry.keys()]) {
        const spec = registry.get(id)
        if (!spec) continue
        const st = store.settledOf(id)
        if (st === 'done' || st === 'error') {
          clearPending(id)
          registry.delete(id)
          if (st === 'done') spec.onDone?.()
          else spec.onError?.(st)
        }
      }
      // ② 对账 localStorage 里未结束的任务（刷新后快照判断）
      for (const p of readPending()) {
        const st = store.settledOf(p.id)
        if (st === 'running') continue // 还在跑
        clearPending(p.id)
        const spec = registry.get(p.id)
        if (spec) {
          registry.delete(p.id)
          if (st === 'done') spec.onDone?.()
          else if (st === 'error') spec.onError?.(st)
          // 'missing'：历史已清空/后端重启，视为已结束，无回调
        }
      }
    },
    { deep: true },
  )
}

// ---------- 组件级注册 ----------
/**
 * 在组件中注册一个任务并暴露其运行状态：
 *   const { isRunning } = useTask('skill-update', { kind: 'skill', onDone: refresh })
 * spec 为空时只查询状态不注册（供只读场景用）。
 */
export function useTask(taskId: string, spec?: TaskSpec) {
  const store = useProcessStore()
  store.ensureStarted()
  initTaskListener()

  if (spec) {
    // 合并注册（同 id 重复注册覆盖，刷新后重新注册恢复回调）。
    const prev = registry.get(taskId)
    registry.set(taskId, { ...(prev ?? {}), ...spec, kind: spec.kind })
  }

  const isRunning = computed(() => store.isRunning(taskId))
  const settled = computed(() => store.settledOf(taskId))

  onBeforeUnmount(() => {
    // 任务若仍在 localStorage 待办（未结束），保留注册让回调在结束后仍能触发；
    // 否则移除注册避免内存泄漏（重新挂载时 useTask 会重新注册）。
    if (!readPending().some((p) => p.id === taskId)) {
      registry.delete(taskId)
    }
  })

  return { isRunning, settled }
}

/** 便利函数：判断某个 task id 是否在跑（供按钮 disabled/loading 使用）。 */
export function useTaskRunning(taskId: string) {
  return useTask(taskId).isRunning
}
