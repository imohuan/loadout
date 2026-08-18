<script setup lang="ts">
import {
  RiArrowDownLine,
  RiArrowUpLine,
  RiDeleteBinLine,
  RiEditLine,
  RiRefreshLine,
} from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import EmptyState from '@/components/EmptyState.vue'

defineProps<{ channels: Channel[]; pending?: boolean }>()
const emit = defineEmits<{
  edit: [channel: Channel]
  refresh: [channel: Channel]
  move: [channel: Channel, direction: 'up' | 'down']
  remove: [channel: Channel]
}>()
</script>

<template>
  <TooltipProvider
    ><Card class="rounded-md"
      ><CardHeader
        ><CardTitle class="text-base">渠道列表</CardTitle
        ><CardDescription>列表顺序即普通模型路由时的优先级。</CardDescription></CardHeader
      ><CardContent class="p-0"
        ><div v-if="channels.length" class="overflow-x-auto">
          <Table
            ><TableHeader
              ><TableRow
                ><TableHead>名称</TableHead><TableHead>Base URL</TableHead
                ><TableHead>模型</TableHead><TableHead>费用同步</TableHead
                ><TableHead class="text-right">操作</TableHead></TableRow
              ></TableHeader
            ><TableBody
              ><TableRow v-for="(channel, index) in channels" :key="channel.id"
                ><TableCell class="font-medium">{{ channel.name }}</TableCell
                ><TableCell class="max-w-64 truncate font-mono text-xs">{{
                  channel.base_url
                }}</TableCell
                ><TableCell>{{
                  channel.models?.length
                    ? `${channel.models.length} 个`
                    : channel.models_error
                      ? '探测失败'
                      : '未知'
                }}</TableCell
                ><TableCell
                  ><Badge :variant="channel.sync_billing ? 'default' : 'secondary'">{{
                    channel.sync_billing ? '开启' : '关闭'
                  }}</Badge></TableCell
                ><TableCell
                  ><div class="flex justify-end gap-1">
                    <Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="刷新模型"
                          :disabled="pending"
                          @click="emit('refresh', channel)"
                          ><RiRefreshLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>刷新模型</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="提高优先级"
                          :disabled="pending || index === 0"
                          @click="emit('move', channel, 'up')"
                          ><RiArrowUpLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>提高优先级</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="降低优先级"
                          :disabled="pending || index === channels.length - 1"
                          @click="emit('move', channel, 'down')"
                          ><RiArrowDownLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>降低优先级</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="编辑"
                          :disabled="pending"
                          @click="emit('edit', channel)"
                          ><RiEditLine size="16" /></Button></TooltipTrigger
                      ><TooltipContent>编辑</TooltipContent></Tooltip
                    ><Tooltip
                      ><TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="删除"
                          :disabled="pending"
                          @click="emit('remove', channel)"
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
          title="还没有渠道"
          description="先添加一个上游服务，再探测可用模型。" /></CardContent></Card
  ></TooltipProvider>
</template>
