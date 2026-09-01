<script setup lang="ts">
import { computed } from 'vue'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { CanvasRenderer } from 'echarts/renderers'
import { PieChart } from 'echarts/charts'
import { LegendComponent, TooltipComponent } from 'echarts/components'
import type { ModelDistPoint } from '@/lib/types'

use([CanvasRenderer, PieChart, TooltipComponent, LegendComponent])

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

// 饼图与列表都只显示前 5 名；超出部分在饼图中合并成「其他」一项（灰色），
// 避免长尾挤成一团看不清，token 占比信息不丢。
const TOP_N = 5
const OTHERS_COLOR = '#94a3b8' // slate-400

const chartData = computed(() => {
  const top = props.items.slice(0, TOP_N)
  const rest = props.items.slice(TOP_N)
  const othersTokens = rest.reduce((sum, d) => sum + d.tokens, 0)
  const slices = top.map((d, i) => ({
    name: d.model,
    value: d.tokens,
    itemStyle: { color: palette[i % palette.length] },
  }))
  if (othersTokens > 0) {
    slices.push({
      name: `其他（${rest.length}）`,
      value: othersTokens,
      itemStyle: { color: OTHERS_COLOR },
    })
  }
  return slices
})

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
      data: chartData.value,
    },
  ],
}))

// 前 5 名行；如存在「其他」切片，则追加一行汇总项。
const rows = computed(() => {
  const out = props.items.slice(0, TOP_N).map((d, i) => ({
    ...d,
    rank: i + 1,
    color: palette[i % palette.length],
  }))
  const rest = props.items.slice(TOP_N)
  if (rest.length === 0) return out
  const sum = (k: 'tokens' | 'prompt_tokens' | 'completion_tokens' | 'cached_tokens' | 'calls') =>
    rest.reduce((acc, d) => acc + d[k], 0)
  out.push({
    model: `其他（${rest.length}）`,
    calls: sum('calls'),
    tokens: sum('tokens'),
    prompt_tokens: sum('prompt_tokens'),
    completion_tokens: sum('completion_tokens'),
    cached_tokens: sum('cached_tokens'),
    rank: TOP_N + 1,
    color: OTHERS_COLOR,
  })
  return out
})
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">模型消耗分布</CardTitle>
      <CardDescription>按 Token 计 · 调用次数最多的模型</CardDescription>
    </CardHeader>
    <CardContent>
      <div v-if="items.length === 0" class="py-8 text-center text-sm text-muted-foreground">
        暂无数据
      </div>
      <template v-else>
        <!-- 饼图居中放大 -->
        <div class="flex justify-center">
          <div class="h-56 w-full max-w-[280px]">
            <VChart :option="option" autoresize />
          </div>
        </div>
        <!-- 模型列表(模型名不再 truncate,完整显示) -->
        <div class="mt-4 space-y-1.5">
          <div v-for="r in rows" :key="r.model" class="flex items-center gap-2 text-sm">
            <span class="w-6 shrink-0 text-xs text-muted-foreground tabular-nums"
              >#{{ r.rank }}</span
            >
            <span
              class="inline-block h-2.5 w-2.5 shrink-0 rounded-full"
              :style="{ backgroundColor: r.color }"
            />
            <span class="min-w-0 flex-1 break-all">{{ r.model }}</span>
            <span class="shrink-0 text-xs text-muted-foreground tabular-nums"
              >{{ r.calls }} 次</span
            >
            <span class="shrink-0 font-medium tabular-nums"
              >{{ (r.tokens / 1000).toFixed(1) }}K</span
            >
          </div>
        </div>
      </template>
    </CardContent>
  </Card>
</template>
