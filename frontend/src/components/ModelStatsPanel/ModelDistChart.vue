<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { PieChart } from 'echarts/charts'
import { TooltipComponent } from 'echarts/components'
import type { ModelDistPoint } from '@/lib/types'

use([CanvasRenderer, PieChart, TooltipComponent])

const props = defineProps<{ items: ModelDistPoint[] }>()

const palette = [
  '#6366f1', // indigo
  '#f59e0b', // amber
  '#10b981', // emerald
  '#ef4444', // red
  '#8b5cf6', // violet
  '#06b6d4', // cyan
  '#f43f5e', // rose
  '#84cc16', // lime
]

const option = computed(() => ({
  tooltip: {
    trigger: 'item' as const,
    formatter: (p: { name: string; value: number; percent: number }) =>
      `${p.name}<br/>${p.value.toLocaleString()} tokens<br/>占比 ${p.percent.toFixed(1)}%`,
  },
  legend: { show: false },
  series: [
    {
      type: 'pie' as const,
      radius: ['42%', '72%'],
      avoidLabelOverlap: true,
      label: {
        show: true,
        formatter: '{b}\n{d}%',
        fontSize: 11,
      },
      labelLine: { show: true, length: 12, length2: 8 },
      itemStyle: {
        borderColor: '#fff',
        borderWidth: 2,
      },
      data: props.items.map((d, i) => ({
        name: d.model,
        value: d.tokens,
        itemStyle: { color: palette[i % palette.length] },
      })),
    },
  ],
}))

const rows = computed(() =>
  props.items.map((d, i) => ({ ...d, rank: i + 1, color: palette[i % palette.length] })),
)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">模型消耗分布</CardTitle>
      <CardDescription>按 Token 计 · 调用次数最多的模型</CardDescription>
    </CardHeader>
    <CardContent>
      <div v-if="items.length === 0" class="py-8 text-center text-sm text-muted-foreground">暂无数据</div>
      <template v-else>
        <!-- 饼图居中放大 -->
        <div class="flex justify-center">
          <div class="h-56 w-full max-w-[280px]">
            <VChart :option="option" autoresize />
          </div>
        </div>
        <!-- 模型列表(模型名不再 truncate,完整显示) -->
        <div class="mt-4 space-y-1.5">
          <div
            v-for="r in rows"
            :key="r.model"
            class="flex items-center gap-2 text-sm"
          >
            <span class="w-6 shrink-0 text-xs text-muted-foreground tabular-nums">#{{ r.rank }}</span>
            <span
              class="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
              :style="{ backgroundColor: r.color }"
            />
            <span class="min-w-0 flex-1 break-all">{{ r.model }}</span>
            <span class="shrink-0 text-xs text-muted-foreground tabular-nums">{{ r.calls }} 次</span>
            <span class="shrink-0 font-medium tabular-nums">{{ (r.tokens / 1000).toFixed(1) }}K</span>
          </div>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
