<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()

function formatTokens(n: number) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(2) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}

// 5 张内嵌卡：第 1 张是"消耗积分"（强调色，对应总 Token），后 4 张是常规指标。
type Card = {
  label: string
  value: string
  sub?: string
  accent?: boolean
}

const cards = computed<Card[]>(() => {
  const s = props.stats?.summary
  if (!s) return []
  return [
    {
      label: '消耗积分',
      value: formatTokens(s.total_tokens),
      sub: '以 Token 计',
      accent: true,
    },
    { label: '输入', value: formatTokens(s.prompt_tokens) },
    { label: '输出', value: formatTokens(s.completion_tokens) },
    { label: '总 Token', value: formatTokens(s.total_tokens) },
    { label: '请求数量', value: s.requests.toLocaleString() },
  ]
})
</script>

<template>
  <div class="grid grid-cols-5 gap-2">
    <div
      v-for="c in cards"
      :key="c.label"
      class="min-w-0 rounded-md border px-3 py-2.5"
      :class="
        c.accent
          ? 'border-amber-500/30 bg-amber-500/10 text-amber-700 dark:text-amber-300'
          : 'bg-card'
      "
    >
      <div
        class="truncate text-xs"
        :class="c.accent ? 'text-amber-700/90 dark:text-amber-300/90' : 'text-muted-foreground'"
      >
        {{ c.label }}
      </div>
      <div
        class="mt-0.5 text-lg font-semibold leading-tight"
        :class="c.accent ? 'text-amber-700 dark:text-amber-300' : ''"
      >
        {{ c.value }}
      </div>
      <div
        v-if="c.sub"
        class="mt-0.5 truncate text-[11px]"
        :class="c.accent ? 'text-amber-700/70 dark:text-amber-300/70' : 'text-muted-foreground'"
      >
        {{ c.sub }}
      </div>
    </div>
  </div>
</template>
