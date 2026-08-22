<script setup lang="ts">
import { computed } from 'vue'
import type { Channel } from '@/lib/types'
import { formatChannelRef, type ChannelRefInput } from '@/composables/useChannelRef'

const props = withDefaults(
  defineProps<{
    target: ChannelRefInput | undefined | null
    channels: Channel[]
    /** 渠道名前是否加 `@` 前缀（默认 true，格式统一为 `@ 渠道名` / `@ 渠道名(Key1)`） */
    atPrefix?: boolean
  }>(),
  { atPrefix: true },
)
const text = computed(() => formatChannelRef(props.channels, props.target))
</script>

<template>
  <span v-if="text" class="inline-flex items-center gap-0.5 text-muted-foreground">
    <span v-if="atPrefix" class="shrink-0">@</span><span class="whitespace-nowrap">{{ text }}</span>
  </span>
</template>
