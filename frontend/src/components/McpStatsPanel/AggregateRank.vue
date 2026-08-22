<script setup lang="ts">
import { computed } from 'vue'
import { RiFlowChart } from '@remixicon/vue'
import type { McpAggregateRank } from '@/lib/types'

const props = defineProps<{ items: McpAggregateRank[]; loading?: boolean }>()
const rows = computed(() =>
  props.items.slice(0, 5).map((it, i) => ({
    rank: i + 1,
    name: it.target ?? '$smart',
    kind: it.kind,
    calls: it.calls,
    badgeLabel: `${it.calls} 次调用`,
  })),
)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"
        ><RiFlowChart size="18" />聚合服务调用排行</CardTitle
      >
      <CardDescription>近 30 天</CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <div
        v-if="loading && rows.length === 0"
        class="py-8 text-center text-sm text-muted-foreground"
      >
        加载中…
      </div>
      <div v-else-if="rows.length === 0" class="py-8 text-center text-sm text-muted-foreground">
        暂无调用
      </div>
      <div
        v-for="row in rows"
        :key="`${row.kind}-${row.name}`"
        class="flex items-center gap-3 rounded-md border px-3 py-2"
      >
        <span class="w-12 shrink-0 text-xs uppercase text-muted-foreground"
          >TOP {{ row.rank }}</span
        >
        <div class="min-w-0 flex-1">
          <div class="truncate font-medium">{{ row.name }}</div>
          <div class="text-xs text-muted-foreground">
            {{ row.kind === '$smart' ? '智能路由' : '上游服务' }}
          </div>
        </div>
        <Badge
          variant="secondary"
          class="shrink-0 rounded-md bg-orange-500 text-white hover:bg-orange-500"
          >{{ row.badgeLabel }}</Badge
        >
      </div>
    </CardContent>
  </Card>
</template>
