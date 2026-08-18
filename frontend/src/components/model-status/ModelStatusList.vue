<script setup lang="ts">
import { ref } from 'vue'
import { RiArrowDownSLine, RiArrowRightSLine, RiRefreshLine, RiRestartLine } from '@remixicon/vue'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import StatusBadge from '@/components/StatusBadge.vue'
import EmptyState from '@/components/EmptyState.vue'
import { formatDate } from '@/lib/format'

defineProps<{ items: ChannelStatus[]; pending?: boolean }>()
const emit = defineEmits<{
  channelToggle: [item: ChannelStatus, enabled: boolean]
  modelToggle: [item: ChannelStatus, model: ModelStatus, enabled: boolean]
  recoverChannel: [item: ChannelStatus]
  recoverModel: [item: ChannelStatus, model: ModelStatus]
  recoverAllModels: [item: ChannelStatus]
}>()
const expanded = ref(new Set<string>())
function toggle(id: string) {
  expanded.value.has(id) ? expanded.value.delete(id) : expanded.value.add(id)
}
</script>

<template>
  <div v-if="items.length" class="space-y-3">
    <Card v-for="item in items" :key="item.channel.id" class="rounded-md py-0">
      <CardContent class="p-0"><button class="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-muted/50"
          type="button" :aria-expanded="expanded.has(item.channel.id)"
          :aria-label="`${expanded.has(item.channel.id) ? '收起' : '展开'} ${item.channel.name} 的模型状态`"
          @click="toggle(item.channel.id)">
          <component :is="expanded.has(item.channel.id) ? RiArrowDownSLine : RiArrowRightSLine" size="18"
            class="shrink-0 text-muted-foreground" />
          <div class="min-w-0 flex-1">
            <p class="font-medium text-foreground">{{ item.channel.name }}</p>
            <p class="truncate font-mono text-xs text-muted-foreground">
              {{ item.channel.base_url }}
            </p>
          </div>
          <StatusBadge :status="item.health_status" :available="item.effective_available" /><span
            class="hidden text-sm text-muted-foreground md:block">{{ item.models.length }} 个模型</span>
        </button>
        <div class="flex flex-wrap items-center gap-3 border-t border-border px-4 py-2 text-sm">
          <div class="flex items-center gap-2">
            <Switch :id="`channel-${item.channel.id}`" :model-value="item.manual_enabled" :disabled="pending"
              @update:model-value="emit('channelToggle', item, Boolean($event))" /><Label
              :for="`channel-${item.channel.id}`">手动启用</Label>
          </div>
          <span v-if="item.reason" class="min-w-48 flex-1 text-muted-foreground">{{
            item.reason
            }}</span><Button variant="outline" size="sm" :disabled="pending" @click="emit('recoverChannel', item)">
            <RiRefreshLine size="16" />恢复渠道
          </Button>
          <Button variant="outline" size="sm" :disabled="pending" :title="'清空当前渠道的自动熔断 + 强制打开该渠道所有手动开关；范围仅限当前渠道'"
            @click="emit('recoverAllModels', item)">
            <RiRestartLine size="16" />恢复全部异常
          </Button>
        </div>
        <div v-if="expanded.has(item.channel.id)" class="border-t border-border">
          <div class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>模型</TableHead>
                  <TableHead>手动开关</TableHead>
                  <TableHead>自动状态</TableHead>
                  <TableHead>最后错误</TableHead>
                  <TableHead>失败</TableHead>
                  <TableHead>最近成功</TableHead>
                  <TableHead class="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="model in item.models" :key="model.model">
                  <TableCell class="font-mono text-xs font-medium select-all">{{ model.model }}</TableCell>
                  <TableCell>
                    <Switch :id="`model-${item.channel.id}-${model.model}`" :model-value="model.manual_enabled"
                      :disabled="pending" @update:model-value="
                        emit('modelToggle', item, model, Boolean($event))
                        " />
                  </TableCell>
                  <TableCell>
                    <StatusBadge :status="model.health_status" :available="model.effective_available" />
                  </TableCell>
                  <TableCell class="max-w-64 whitespace-normal text-xs text-muted-foreground">{{
                    model.last_error || model.reason || '-'
                    }}</TableCell>
                  <TableCell class="tabular-nums">{{ model.fail_count || 0 }}</TableCell>
                  <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{
                    formatDate(model.last_success_at)
                    }}</TableCell>
                  <TableCell class="text-right"><Button variant="outline" size="sm" :disabled="pending"
                      @click="emit('recoverModel', item, model)">
                      <RiRefreshLine size="14" />恢复
                    </Button></TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
        </div>
      </CardContent>
    </Card>
  </div>
  <EmptyState v-else title="没有模型状态记录" description="渠道探测到模型，或发生过路由请求后，这里会显示模型状态。" />
</template>
