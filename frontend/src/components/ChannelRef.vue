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
    /** 是否继承父级文字颜色（用于异常状态整段着色；true 时去掉默认的 muted 灰色） */
    inheritColor?: boolean
  }>(),
  { atPrefix: true },
)
const text = computed(() => formatChannelRef(props.channels, props.target))
</script>

<template>
  <span
    v-if="text"
    class="inline"
    :class="inheritColor ? '' : 'text-muted-foreground'"
  >
    <span v-if="atPrefix">@</span><span>{{ text }}</span>
  </span>
</template>
