<script setup lang="ts">
import { RiCpuLine, RiRefreshLine } from '@remixicon/vue'
import { useModelStats, type ModelStatsDays } from '@/composables/useModelStats'
import ModelSummaryCards from './ModelSummaryCards.vue'
import ModelSecondaryCards from './ModelSecondaryCards.vue'
import ModelTrendChart from './ModelTrendChart.vue'
import ModelCalendar from './ModelCalendar.vue'
import ModelDistChart from './ModelDistChart.vue'

const { stats, loading, error, days, setDays, refresh } = useModelStats()

const tabs: { label: string; value: ModelStatsDays }[] = [
  { label: '近 7 天', value: 7 },
  { label: '近 15 天', value: 15 },
  { label: '近 30 天', value: 30 },
  { label: '总计', value: 365 },
]
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"
        ><RiCpuLine size="18" />模型使用情况</CardTitle
      >
      <CardDescription>模型网关近 {{ days }} 天使用统计</CardDescription>
    </CardHeader>
    <CardContent>
      <div
        v-if="error"
        class="rounded-md border border-destructive/40 bg-destructive/5 px-4 py-6 text-center text-sm text-destructive"
      >
        统计接口不可用：{{ error }}
        <Button variant="outline" size="sm" class="mt-2" @click="refresh">重试</Button>
      </div>
      <div v-else class="space-y-3">
        <div class="flex items-center justify-between gap-2">
          <div class="inline-flex items-center rounded-md bg-muted p-0.5 text-xs">
            <button
              v-for="t in tabs"
              :key="t.value"
              type="button"
              :disabled="loading && days === t.value"
              class="rounded px-2.5 py-1 font-medium transition-colors disabled:cursor-not-allowed"
              :class="[
                days === t.value
                  ? 'bg-primary text-primary-foreground shadow'
                  : 'text-muted-foreground hover:text-foreground',
              ]"
              @click="setDays(t.value)"
            >
              {{ t.label }}
            </button>
          </div>
          <Button variant="ghost" size="sm" :disabled="loading" @click="refresh">
            <RiRefreshLine :class="{ 'animate-spin': loading }" size="15" />刷新
          </Button>
        </div>
        <ModelSummaryCards :stats="stats" />
        <ModelSecondaryCards :stats="stats" />
        <ModelTrendChart :stats="stats" />
        <div class="grid grid-cols-1 gap-3 lg:grid-cols-2">
          <ModelCalendar :calendar="stats?.calendar ?? []" :days="days" />
          <ModelDistChart :items="stats?.model_dist ?? []" />
        </div>
      </div>
    </CardContent>
  </Card>
</template>
