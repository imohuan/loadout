<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import type { ModelStats } from '@/lib/types'

use([CanvasRenderer, PieChart, TooltipComponent])

const props = defineProps<{ stats: ModelStats | null }>()

const option = computed(() => {
  const h = props.stats?.hit_rate
  const total = h?.total ?? 0
  return {
    tooltip: { trigger: 'item' as const },
    series: [
      {
        type: 'pie' as const,
        radius: ['62%', '82%'],
        avoidLabelOverlap: false,
        label: { show: false },
        data: [
          {
            value: Math.max(0, Math.round(total * 100)),
            name: '命中',
            itemStyle: { color: '#22c55e' },
          },
          {
            value: Math.max(0, Math.round((1 - total) * 100)),
            name: '未命中',
            itemStyle: { color: '#e2e8f0' },
          },
        ],
      },
    ],
  }
})
const detail = computed(() => {
  const h = props.stats?.hit_rate
  if (!h) return []
  return [
    { label: '输入命中', rate: h.input },
    { label: '输出命中', rate: h.output },
    { label: '总命中', rate: h.total },
  ]
})
</script>

<template>
  <div class="flex items-center gap-4 rounded-md border bg-card px-3 py-2.5">
    <div class="h-20 w-20 shrink-0"><VChart :option="option" autoresize /></div>
    <div class="flex-1 space-y-1">
      <div v-for="d in detail" :key="d.label" class="flex items-center justify-between text-sm">
        <span class="text-muted-foreground">{{ d.label }}</span>
        <span class="font-medium">{{ (d.rate * 100).toFixed(1) }}%</span>
      </div>
    </div>
  </div>
</template>
