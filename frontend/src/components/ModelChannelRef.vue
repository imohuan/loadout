<script setup lang="ts">
import { computed } from 'vue'
import type { Channel } from '@/lib/types'
import ChannelRef from '@/components/ChannelRef.vue'
import {
  modelChannelStatus,
  type ChannelRefInput,
  type ModelChannelStatus,
} from '@/composables/useChannelRef'

const props = withDefaults(
  defineProps<{
    /** 模型名 */
    model: string
    /** 渠道引用（透传给 ChannelRef） */
    target: ChannelRefInput | undefined | null
    /** 渠道列表 */
    channels: Channel[]
    /** 是否装饰异常状态（日志页传 false 保持原样） */
    decorate?: boolean
    /** 渠道名前是否加 @ 前缀（透传给 ChannelRef） */
    atPrefix?: boolean
  }>(),
  { decorate: true, atPrefix: true },
)

const status = computed(() =>
  modelChannelStatus(props.channels, props.model, props.target),
)

// 各状态对应的 Tailwind 装饰类。
const STATUS_CLASS: Record<ModelChannelStatus, string> = {
  ok: '',
  model_missing: 'line-through decoration-red-500 decoration-2 text-red-600 dark:text-red-400',
  channel_missing:
    'underline decoration-wavy decoration-red-500 decoration-2 text-red-600 dark:text-red-400 underline-offset-4',
  key_missing:
    'underline decoration-wavy decoration-amber-500 decoration-2 text-amber-600 dark:text-amber-400 underline-offset-4',
  model_not_in_channel:
    'italic underline decoration-solid decoration-amber-500 decoration-1 text-amber-600 dark:text-amber-400 underline-offset-2',
}

const statusClass = computed(() =>
  props.decorate ? STATUS_CLASS[status.value.status] : '',
)

const isAbnormal = computed(
  () => props.decorate && status.value.status !== 'ok',
)
</script>

<template>
  <span class="inline whitespace-nowrap" :class="statusClass">
    <template v-if="isAbnormal">
      <Tooltip>
        <TooltipTrigger as-child>
          <span class="font-mono">{{ model }}</span>
        </TooltipTrigger>
        <TooltipContent side="top" align="start">
          <span class="text-xs">{{ status.reason }}</span>
        </TooltipContent>
      </Tooltip>
    </template>
    <span v-else class="font-mono">{{ model }}</span>
    <ChannelRef
      :target="target"
      :channels="channels"
      :at-prefix="atPrefix"
      :inherit-color="isAbnormal"
    />
  </span>
</template>
