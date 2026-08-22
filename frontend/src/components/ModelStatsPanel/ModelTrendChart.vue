<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import type { ModelStats } from '@/lib/types'

use([CanvasRenderer, LineChart, GridComponent, TooltipComponent])

const props = defineProps<{ stats: ModelStats | null }>()

function fmt(n: number) {
  if (n >= 1e6) return (n / 1e6).toFixed(1) + 'M'
  if (n >= 1e3) return (n / 1e3).toFixed(1) + 'K'
  return String(n)
}

const option = computed(() => {
  const trend = props.stats?.trend ?? []
  return {
    grid: { left: 40, right: 12, top: 10, bottom: 28 },
    tooltip: {
      trigger: 'axis' as const,
      valueFormatter: (v: number) => fmt(v) + ' tokens',
    },
    xAxis: {
      type: 'category' as const,
      data: trend.map((p) => p.date.slice(5)),
      axisLine: { lineStyle: { color: '#cbd5e1' } },
      axisLabel: { color: '#64748b', fontSize: 11 },
    },
    yAxis: {
      type: 'value' as const,
      splitLine: { lineStyle: { color: '#e2e8f0' } },
      axisLabel: { color: '#64748b', fontSize: 11, formatter: (v: number) => fmt(v) },
    },
    series: [
      {
        type: 'line' as const,
        data: trend.map((p) => p.total_tokens),
        smooth: true,
        symbol: 'circle',
        symbolSize: 5,
        lineStyle: { color: '#6366f1', width: 2 },
        itemStyle: { color: '#6366f1' },
        areaStyle: {
          color: {
            type: 'linear' as const,
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(99, 102, 241, 0.4)' },
              { offset: 1, color: 'rgba(99, 102, 241, 0.05)' },
            ],
          },
        },
      },
    ],
  }
})
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">每日 Token 消耗</CardTitle>
      <CardDescription>近 30 天</CardDescription>
    </CardHeader>
    <CardContent>
      <div class="h-44">
        <div
          v-if="!stats?.trend?.length"
          class="flex h-full items-center justify-center text-sm text-muted-foreground"
        >
          暂无数据
        </div>
        <VChart v-else :option="option" autoresize />
      </div>
    </CardContent>
  </Card>
</template>
