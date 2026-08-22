<script setup lang="ts">
import { RiDeleteBinLine, RiEditLine, RiFileCopyLine, RiLoader4Line } from '@remixicon/vue'
import type { Aggregate, Channel } from '@/lib/types'
import ModelChannelRef from '@/components/ModelChannelRef.vue'
import EmptyState from '@/components/EmptyState.vue'
import { modelChannelStatus } from '@/composables/useChannelRef'

const props = defineProps<{
  aggregates: Aggregate[]
  channels: Channel[]
  isPending?: (key: string) => boolean
}>()
const emit = defineEmits<{
  edit: [aggregate: Aggregate]
  remove: [aggregate: Aggregate]
  duplicate: [aggregate: Aggregate]
  pruneAbnormal: [removed: number]
}>()
// 与 AggregatesView.remove() 的 key 规则一致：aggregate:{name}:remove
function busy(aggregate: Aggregate, action: string) {
  return props.isPending ? props.isPending(`aggregate:${aggregate.name}:${action}`) : false
}

// 统计所有聚合目标中的异常数量（model_missing / channel_missing / key_missing / model_not_in_channel）。
function abnormalTargetCount(): number {
  let count = 0
  for (const aggregate of props.aggregates) {
    for (const target of aggregate.targets) {
      const { status } = modelChannelStatus(props.channels, target.model, target)
      if (status !== 'ok') count++
    }
  }
  return count
}
</script>

<template>
 <TooltipProvider
  ><Card class="rounded-md"
    ><CardHeader class="flex flex-row items-center justify-between gap-3 space-y-0"
         ><div
           ><CardTitle class="text-base">聚合模型列表</CardTitle
           ><CardDescription
             >聚合模型只使用指定渠道，不会跨到同名模型的其他渠道。</CardDescription
           ></div
         ><Button
           v-if="abnormalTargetCount() > 0"
           variant="outline"
           size="sm"
           class="shrink-0"
           :disabled="isPending?.('prune-abnormal')"
           @click="emit('pruneAbnormal', abnormalTargetCount())"
           ><RiLoader4Line v-if="isPending?.('prune-abnormal')" class="animate-spin" size="14" /><RiDeleteBinLine v-else size="14" />清除异常模型（{{ abnormalTargetCount() }}）</Button
         ></CardHeader
      ><CardContent class="p-0"
        ><div v-if="aggregates.length" class="overflow-x-auto">
          <Table
            ><TableHeader
              ><TableRow
                ><TableHead>虚拟模型</TableHead><TableHead>目标优先级</TableHead
                ><TableHead class="text-right">操作</TableHead></TableRow
              ></TableHeader
            ><TableBody
              ><TableRow v-for="aggregate in aggregates" :key="aggregate.name"
                ><TableCell class="font-mono font-medium">{{ aggregate.name }}</TableCell
                ><TableCell
                  ><ol class="space-y-1 text-sm text-muted-foreground">
                    <li
                      v-for="(target, index) in aggregate.targets"
                      :key="`${target.model}-${target.channel_base_url || target.channel_ids?.join(',') || target.channel_id}`"
                    >
                      <span class="mr-2 tabular-nums">{{ index + 1 }}.</span
                      ><ModelChannelRef :model="target.model" :target="target" :channels="channels" />
                    </li></ol></TableCell
                ><TableCell
                  ><div class="flex justify-end gap-1">
                    <Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="编辑"
                          @click="emit('edit', aggregate)"
                          ><RiEditLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>编辑</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="复制"
                          @click="emit('duplicate', aggregate)"
                          ><RiFileCopyLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>复制</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="删除"
                          :disabled="busy(aggregate, 'remove')"
                          @click="emit('remove', aggregate)"
                          ><RiLoader4Line
                            v-if="busy(aggregate, 'remove')"
                            class="animate-spin"
                            size="16" /><RiDeleteBinLine
                            v-else
                            size="16" /></Button></TooltipTrigger
                      ><TooltipContent>删除</TooltipContent></Tooltip
                    >
                  </div></TableCell
                ></TableRow
              ></TableBody
            ></Table
          >
        </div>
        <EmptyState
          v-else
          title="还没有聚合模型"
          description="创建一个虚拟模型，按固定顺序路由到多个真实目标。" /></CardContent></Card
  ></TooltipProvider>
</template>
