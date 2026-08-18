import { onMounted, ref } from 'vue'
import { getMcpStats } from '@/lib/api'
import type { McpStats } from '@/lib/types'

export function useMcpStats() {
  const stats = ref<McpStats | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)

  async function refresh(opts: { days?: number; top?: number } = {}) {
    loading.value = true
    error.value = null
    try {
      stats.value = await getMcpStats(opts)
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      stats.value = null
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    void refresh()
  })

  return { stats, loading, error, refresh }
}
