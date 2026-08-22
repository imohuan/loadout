<script setup lang="ts">
import { RiLineChartLine, RiRefreshLine } from '@remixicon/vue'
import { useMcpStats } from '@/composables/useMcpStats'
import TrendChart from './TrendChart.vue'
import AggregateRank from './AggregateRank.vue'
import ToolRank from './ToolRank.vue'

const { stats, loading, error, refresh } = useMcpStats()
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"
        ><RiLineChartLine size="18" />MCP 调用统计</CardTitle
      >
      <CardDescription>聚合网关近 30 天使用情况</CardDescription>
      <template #actions>
        <Button variant="ghost" size="sm" :disabled="loading" @click="refresh">
          <RiRefreshLine :class="{ 'animate-spin': loading }" size="15" />刷新
        </Button>
      </template>
    </CardHeader>
    <CardContent>
      <div
        v-if="error"
        class="rounded-md border border-destructive/40 bg-destructive/5 px-4 py-6 text-center text-sm text-destructive"
      >
        统计接口不可用：{{ error }}
        <Button variant="outline" size="sm" class="mt-2" @click="refresh">重试</Button>
      </div>
      <div v-else class="space-y-6">
        <TrendChart :data="stats?.trend ?? []" :loading="loading" />
        <div class="grid gap-4 lg:grid-cols-2">
          <AggregateRank :items="stats?.rank_aggregates ?? []" :loading="loading" />
          <ToolRank :items="stats?.rank_tools ?? []" :loading="loading" />
        </div>
      </div>
    </CardContent>
  </Card>
</template>
