import { onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'

export function useListLoader<T>(loader: () => Promise<T>) {
  const data = ref<T>()
  // 仅首次加载（尚无数据）时为 true，用于占位；已有数据后的刷新保持静默，避免整表闪烁
  const loading = ref(false)
  // 已有数据时的静默刷新中为 true，可用于按钮旋转/禁用等轻量反馈
  const refreshing = ref(false)

  let seq = 0

  async function refresh(options?: { silentError?: boolean }) {
    const current = ++seq
    const silent = data.value !== undefined
    if (silent) {
      refreshing.value = true
    } else {
      loading.value = true
    }
    try {
      const result = await loader()
      if (current !== seq) return // 丢弃过期响应，防止快速连点导致数据乱序
      data.value = result
    } catch (error) {
      if (current !== seq) return
      // 定时器等后台刷新失败时静默，避免重复弹错刷屏；手动刷新保留提示
      if (!options?.silentError) {
        toast.error(error instanceof Error ? error.message : '加载失败')
      }
    } finally {
      if (current !== seq) return
      loading.value = false
      refreshing.value = false
    }
  }

  onMounted(refresh)
  return { data, loading, refreshing, refresh }
}
