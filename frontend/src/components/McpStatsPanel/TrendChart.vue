<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { LineChart } from 'echarts/charts'
import { GridComponent, TooltipComponent } from 'echarts/components'
import type { McpTrendPoint } from '@/lib/types'
import { useChartTheme, tooltipStyle } from '@/composables/useChartTheme'

use([CanvasRenderer, LineChart, GridComponent, TooltipComponent])

const props = defineProps<{ data: McpTrendPoint[]; loading?: boolean }>()

const { colors } = useChartTheme()

const option = computed(() => {
  const c = colors.value
  return {
    grid: { left: 40, right: 16, top: 16, bottom: 32 },
    tooltip: { trigger: 'axis' as const, ...tooltipStyle(c) },
    xAxis: {
      type: 'category' as const,
      data: props.data.map((p) => p.date.slice(5)),
      axisLine: { lineStyle: { color: c.axis } },
      axisLabel: { color: c.text, fontSize: 11 },
    },
    yAxis: {
      type: 'value' as const,
      splitLine: { lineStyle: { color: c.split } },
      axisLabel: { color: c.text, fontSize: 11 },
    },
    series: [
      {
        type: 'line' as const,
        data: props.data.map((p) => p.count),
        smooth: true,
        symbol: 'circle',
        symbolSize: 6,
        lineStyle: { color: '#f59e0b', width: 2 },
        itemStyle: { color: '#f59e0b' },
        areaStyle: {
          color: {
            type: 'linear' as const,
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(245, 158, 11, 0.45)' },
              { offset: 1, color: 'rgba(245, 158, 11, 0.05)' },
            ],
          },
        },
      },
    ],
  }
})
</script>

<template>
  <div class="h-64">
    <div
      v-if="loading && data.length === 0"
      class="flex h-full items-center justify-center text-sm text-muted-foreground"
    >
      加载中…
    </div>
    <div
      v-else-if="data.length === 0"
      class="flex h-full items-center justify-center text-sm text-muted-foreground"
    >
      暂无数据
    </div>
    <VChart v-else :option="option" autoresize />
  </div>
</template>
