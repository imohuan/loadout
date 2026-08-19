<script setup lang="ts">
import { RiDeleteBinLine, RiEditLine, RiFileCopyLine } from '@remixicon/vue'
import type { Aggregate, Channel } from '@/lib/types'
import EmptyState from '@/components/EmptyState.vue'

defineProps<{ aggregates: Aggregate[]; channels: Channel[]; pending?: boolean }>()
const emit = defineEmits<{
  edit: [aggregate: Aggregate]
  remove: [aggregate: Aggregate]
  duplicate: [aggregate: Aggregate]
}>()
function channelName(channels: Channel[], id: string) {
  return channels.find((channel) => channel.id === id)?.name || id
}
</script>

<template>
  <TooltipProvider
    ><Card class="rounded-md"
      ><CardHeader
        ><CardTitle class="text-base">聚合模型列表</CardTitle
        ><CardDescription
          >聚合模型只使用指定渠道，不会跨到同名模型的其他渠道。</CardDescription
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
                      :key="`${target.model}-${target.channel_id}`"
                    >
                      <span class="mr-2 tabular-nums">{{ index + 1 }}.</span
                      ><span class="font-mono text-foreground">{{ target.model }}</span> @
                      {{ channelName(channels, target.channel_id) }}
                    </li>
                  </ol></TableCell
                ><TableCell
                  ><div class="flex justify-end gap-1">
                    <Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="编辑"
                          :disabled="pending"
                          @click="emit('edit', aggregate)"
                          ><RiEditLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>编辑</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="复制"
                          :disabled="pending"
                          @click="emit('duplicate', aggregate)"
                          ><RiFileCopyLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>复制</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="删除"
                          :disabled="pending"
                          @click="emit('remove', aggregate)"
                          ><RiDeleteBinLine size="16" /></Button></TooltipTrigger
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
