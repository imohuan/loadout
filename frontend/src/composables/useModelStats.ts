import { onMounted, ref } from 'vue'
import { getModelStats } from '@/lib/api'
import type { ModelStats } from '@/lib/types'

export type ModelStatsDays = 7 | 15 | 30 | 365

export function useModelStats(initialDays: ModelStatsDays = 30) {
  const stats = ref<ModelStats | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const days = ref<ModelStatsDays>(initialDays)

  async function refresh(opts: { days?: ModelStatsDays } = {}) {
    if (opts.days !== undefined) days.value = opts.days
    loading.value = true
    error.value = null
    try {
      stats.value = await getModelStats({ days: days.value })
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
      stats.value = null
    } finally {
      loading.value = false
    }
  }

  async function setDays(d: ModelStatsDays) {
    if (days.value === d) return
    await refresh({ days: d })
  }

  onMounted(() => {
    void refresh()
  })

  return { stats, loading, error, days, refresh, setDays }
}
