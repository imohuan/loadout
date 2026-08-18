<script setup lang="ts">
import { computed } from 'vue'
import type { ModelStats } from '@/lib/types'

const props = defineProps<{ stats: ModelStats | null }>()
const rate = computed(() => Math.min(1, Math.max(0, props.stats?.summary.success_rate ?? 0)))
const failed = computed(() => props.stats?.summary.failed ?? 0)
const total = computed(() => props.stats?.summary.requests ?? 0)
</script>

<template>
  <div class="rounded-md border bg-card px-3 py-2.5">
    <div class="mb-1.5 flex items-center justify-between">
      <span class="text-sm font-medium">请求结果</span>
      <span class="text-xs text-muted-foreground">成功率 {{ ((rate) * 100).toFixed(1) }}%</span>
    </div>
    <div class="flex h-2 w-full overflow-hidden rounded-full bg-muted">
      <div class="bg-green-500 transition-all" :style="{ width: `${rate * 100}%` }" />
      <div class="bg-red-500 transition-all" :style="{ width: `${(1 - rate) * 100}%` }" />
    </div>
    <div class="mt-1 flex justify-between text-[11px] text-muted-foreground">
      <span>成功 {{ total - failed }}</span>
      <span>失败 {{ failed }}</span>
    </div>
  </div>
</template>
