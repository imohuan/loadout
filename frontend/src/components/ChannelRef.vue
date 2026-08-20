<script setup lang="ts">
import { computed } from 'vue'
import type { Channel } from '@/lib/types'
import { formatChannelRef, type ChannelRef } from '@/composables/useChannelRef'

const props = withDefaults(
  defineProps<{
    ref: ChannelRef | undefined | null
    channels: Channel[]
    /** 渠道名前是否加 `@`（默认 true） */
    atPrefix?: boolean
    /** 自定义类名 */
    class?: string
  }>(),
  { atPrefix: true },
)
const text = computed(() => formatChannelRef(props.channels, props.ref))
</script>

<template>
  <span v-if="text" :class="props.class">{{ atPrefix ? '@ ' + text : text }}</span>
</template>
