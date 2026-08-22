<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{
  status?: string
  available?: boolean
  hideWhenAvailable?: boolean
  /** 可选计数：当徽章既要展示状态又要展示数量时，渲染为「label count」（如「已关闭 3」） */
  count?: number
}>()
const label = computed(() => {
  if (props.available === false) return '已关闭'
  return (
    {
      available: '可用',
      cooling: '冷却中',
      disabled: '已禁用',
      success: '成功',
      failed: '失败',
      running: '进行中',
      skipped: '已跳过',
    }[props.status || ''] ||
    props.status ||
    '未知'
  )
})
const variant = computed(() =>
  props.available === false || ['disabled', 'failed'].includes(props.status || '')
    ? 'destructive'
    : ['cooling', 'running', 'skipped'].includes(props.status || '')
      ? 'secondary'
      : 'default',
)
const visible = computed(
  () => !(props.hideWhenAvailable && props.available !== false && props.status === 'available'),
)
</script>

<template>
  <Badge v-if="visible" :variant="variant">
    {{ label }}

    <template v-if="count !== undefined">
      <span class="ml-2">{{ count }}个模型</span>
    </template>
  </Badge>
</template>
