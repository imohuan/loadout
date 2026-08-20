import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'

// 默认全局 key：不传 key 时所有任务共用一个锁（向后兼容旧调用方）。
const GLOBAL_KEY = '__global__'
// 最小 loading 展示时长：本地写操作毫秒级完成，spinner 一闪而过用户看不到，
// 统一补齐到该时长再解除按钮 loading（不阻塞任务本身，仅延迟解锁 UI）。
const MIN_PENDING_MS = 300

function delay(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

export function useAsyncTask() {
  const pendingKeys = ref(new Set<string>())
  /** 是否有任意任务在跑（兼容旧用法：整个页面共用一个 loading） */
  const pending = computed(() => pendingKeys.value.size > 0)
  /** 指定 key 的任务是否在跑（按钮级 loading 用） */
  const isPending = (key: string) => pendingKeys.value.has(key)
  const isAnyPending = () => pendingKeys.value.size > 0

  async function run<T>(task: () => Promise<T>, success?: string): Promise<T>
  async function run<T>(key: string, task: () => Promise<T>, success?: string): Promise<T>
  async function run<T>(
    keyOrTask: string | (() => Promise<T>),
    taskOrSuccess?: (() => Promise<T>) | string,
    success?: string,
  ): Promise<T> {
    const key = typeof keyOrTask === 'string' ? keyOrTask : GLOBAL_KEY
    const task = typeof keyOrTask === 'string' ? (taskOrSuccess as () => Promise<T>) : keyOrTask
    const msg = typeof keyOrTask === 'string' ? success : (taskOrSuccess as string | undefined)
    const startedAt = Date.now()
    pendingKeys.value.add(key)
    try {
      const result = await task()
      const elapsed = Date.now() - startedAt
      if (elapsed < MIN_PENDING_MS) await delay(MIN_PENDING_MS - elapsed)
      if (msg) toast.success(msg)
      return result
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '操作失败')
      throw error
    } finally {
      pendingKeys.value.delete(key)
    }
  }

  return { pending, run, isPending, isAnyPending }
}
