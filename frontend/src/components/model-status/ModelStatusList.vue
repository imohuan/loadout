<script setup lang="ts">
import { computed } from 'vue'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import EmptyState from '@/components/EmptyState.vue'
import ModelStatusChannelGroup from './ModelStatusChannelGroup.vue'

const props = defineProps<{
  items: ChannelStatus[]
  mode: 'table' | 'tags'
  isPending?: (key: string) => boolean
  /** 顶层「全部折叠/展开」联动控制，传给所有子组件用于统一展开/收起。 */
  expandAll?: 'expanded' | 'collapsed' | null
}>()
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

// 同一 Base URL 聚合为一个渠道组（尾斜杠差异视为同组）；组内保持原顺序（= position 顺序）。
function normalizeBaseURL(url: string) {
  return url.replace(/\/+$/, '')
}
const groups = computed(() => {
  const map = new Map<string, ChannelStatus[]>()
  for (const item of props.items) {
    const key = normalizeBaseURL(item.channel.base_url)
    const list = map.get(key) || []
    list.push(item)
    map.set(key, list)
  }
  return [...map.entries()].map(([baseUrl, keys]) => ({ baseUrl, keys }))
})
</script>

<template>
  <TooltipProvider>
    <div v-if="groups.length" class="space-y-3">
      <ModelStatusChannelGroup
        v-for="group in groups"
        :key="group.baseUrl"
        :base-url="group.baseUrl"
        :keys="group.keys"
        :mode="mode"
        :is-pending="isPending"
        :expand-all="expandAll"
        @channel-toggle="(item, enabled) => emit('channelToggle', item, enabled)"
        @model-toggle="(item, m, enabled) => emit('modelToggle', item, m, enabled)"
        @recover-channel="(item) => emit('recoverChannel', item)"
        @recover-model="(item, m) => emit('recoverModel', item, m)"
        @recover-all-models="(item) => emit('recoverAllModels', item)"
        @batch-model-toggle="(item, models, enabled) => emit('batchModelToggle', item, models, enabled)"
        @batch-recover-model="(item, models) => emit('batchRecoverModel', item, models)"
        @batch-delete-model="(item, models) => emit('batchDeleteModel', item, models)"
        @delete-model="(item, m) => emit('deleteModel', item, m)"
      />
    </div>
    <EmptyState
      v-else
      title="没有模型状态记录"
      description="渠道探测到模型，或发生过路由请求后，这里会显示模型状态。"
    />
  </TooltipProvider>
</template>
