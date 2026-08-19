<script setup lang="ts">
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import EmptyState from '@/components/EmptyState.vue'
import ModelStatusChannel from './ModelStatusChannel.vue'

defineProps<{ items: ChannelStatus[]; mode: 'table' | 'tags'; pending?: boolean }>()
const emit = defineEmits<{
  channelToggle: [item: ChannelStatus, enabled: boolean]
  modelToggle: [item: ChannelStatus, model: ModelStatus, enabled: boolean]
  recoverChannel: [item: ChannelStatus]
  recoverModel: [item: ChannelStatus, model: ModelStatus]
  recoverAllModels: [item: ChannelStatus]
  batchModelToggle: [item: ChannelStatus, models: ModelStatus[], enabled: boolean]
  batchRecoverModel: [item: ChannelStatus, models: ModelStatus[]]
  batchDeleteModel: [item: ChannelStatus, models: ModelStatus[]]
  deleteModel: [item: ChannelStatus, model: ModelStatus]
}>()
</script>

<template>
  <TooltipProvider>
    <div v-if="items.length" class="space-y-3">
    <ModelStatusChannel
      v-for="item in items"
      :key="item.channel.id"
      :item="item"
      :mode="mode"
      :pending="pending"
      @channel-toggle="(enabled) => emit('channelToggle', item, enabled)"
      @model-toggle="(m, enabled) => emit('modelToggle', item, m, enabled)"
      @recover-channel="emit('recoverChannel', item)"
      @recover-model="(m) => emit('recoverModel', item, m)"
      @recover-all-models="emit('recoverAllModels', item)"
      @batch-model-toggle="(models, enabled) => emit('batchModelToggle', item, models, enabled)"
      @batch-recover-model="(models) => emit('batchRecoverModel', item, models)"
      @batch-delete-model="(models) => emit('batchDeleteModel', item, models)"
      @delete-model="(m) => emit('deleteModel', item, m)"
    />
  </div>
  <EmptyState
    v-else
    title="没有模型状态记录"
    description="渠道探测到模型，或发生过路由请求后，这里会显示模型状态。"
  />
  </TooltipProvider>
</template>
