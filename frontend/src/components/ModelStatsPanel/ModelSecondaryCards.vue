<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()
const cards = computed(() => {
  const s = props.stats?.summary
  if (!s) return []
  return [
    { label: '总请求数', value: s.requests.toLocaleString() },
    { label: '总 Token', value: formatTokens(s.total_tokens) },
    { label: '总消耗', value: formatTokens(s.total_tokens), sub: '以 Token 计' },
    { label: '成功率', value: (s.success_rate * 100).toFixed(1) + '%' },
    { label: '平均耗时', value: s.avg_duration_ms.toFixed(0) + 'ms' },
  ]
})
function formatTokens(n: number) {
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}
</script>

<template>
  <div class="grid grid-cols-5 gap-2">
    <div v-for="c in cards" :key="c.label" class="min-w-0 rounded-md border bg-card px-3 py-2.5">
      <div class="truncate text-xs text-muted-foreground">{{ c.label }}</div>
      <div class="mt-0.5 text-lg font-semibold leading-tight">{{ c.value }}</div>
      <div v-if="c.sub" class="mt-0.5 truncate text-[11px] text-muted-foreground">{{ c.sub }}</div>
    </div>
  </div>
</template>
