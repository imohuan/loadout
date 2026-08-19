import { ref } from 'vue'
import { toast } from 'vue-sonner'

export function useAsyncTask() {
  const pending = ref(false)

  async function run<T>(task: () => Promise<T>, success?: string) {
    pending.value = true
    try {
      const result = await task()
      if (success) toast.success(success)
      return result
    } catch (error) {
      toast.error(error instanceof Error ? error.message : '操作失败')
      throw error
    } finally {
      pending.value = false
    }
  }

  return { pending, run }
}
