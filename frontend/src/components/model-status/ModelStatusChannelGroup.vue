<script setup lang="ts">
import { ref, computed } from 'vue'
import { RiArrowDownSLine, RiArrowRightSLine } from '@remixicon/vue'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import ModelStatusChannel from './ModelStatusChannel.vue'

const props = defineProps<{
  baseUrl: string
  keys: ChannelStatus[]
  mode: 'table' | 'tags'
  isPending?: (key: string) => boolean
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

// 组默认折叠：仅显示组标题，点开后展示组内所有 Key 的头部（每个 Key 自身也默认收起，点开看模型明细）。
const expanded = ref(false)

const totalModels = computed(() => props.keys.reduce((sum, key) => sum + key.models.length, 0))
// 组内可用模型（model.effective_available）。任一 Key 关闭都会拉低这个数字，
// 但仍允许用户在「开启 Key」前看到当前 Key 下到底有多少模型在可用状态。
const availableModels = computed(() =>
  props.keys.reduce((sum, key) => sum + key.models.filter((m) => m.effective_available).length, 0),
)
const availableKeys = computed(() => props.keys.filter((key) => key.effective_available).length)
// 组标题：渠道名称（channel_name 兜底首个 Key 名/账号名）。
const groupTitle = computed(() => {
  const first = props.keys[0]?.channel
  return first?.channel_name || first?.name || props.baseUrl
})
</script>

<template>
  <Card class="rounded-md py-0">
    <CardContent class="p-0">
      <button
        class="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-muted/50"
        type="button"
        :aria-expanded="expanded"
        :aria-label="`${expanded ? '收起' : '展开'} ${baseUrl} 的 Key 状态`"
        @click="expanded = !expanded"
      >
        <component
          :is="expanded ? RiArrowDownSLine : RiArrowRightSLine"
          size="18"
          class="shrink-0 text-muted-foreground"
        />
        <div class="min-w-0 flex-1">
          <div class="flex items-baseline gap-3">
            <p class="truncate text-sm font-medium">{{ groupTitle }}</p>
            <p class="shrink-0 truncate font-mono text-xs text-muted-foreground">{{ baseUrl }}</p>
          </div>
          <p class="text-xs text-muted-foreground tabular-nums">
            {{ keys.length }} 个 Key · {{ availableKeys }}/{{ keys.length }} 可用 ·
            {{ availableModels }} / {{ totalModels }} 个模型可用
          </p>
        </div>
      </button>
      <div v-if="expanded" class="space-y-2 border-t border-border p-2">
        <ModelStatusChannel
          v-for="item in keys"
          :key="item.channel.id"
          :item="item"
          :mode="mode"
          :is-pending="isPending"
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
    </CardContent>
  </Card>
</template>
