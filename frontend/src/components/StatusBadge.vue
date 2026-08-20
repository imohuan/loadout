<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ status?: string; available?: boolean; hideWhenAvailable?: boolean }>()
// 有效不可用时优先显示「已关闭」——避免「红底可用」这种自相矛盾：
// 手动关闭后 health.status 还没刷成 disabled 时的过渡态。
const label = computed(() => {
  if (props.available === false) return '已关闭'
  return (
    ({
      available: '可用',
      cooling: '冷却中',
      disabled: '已禁用',
      success: '成功',
      failed: '失败',
      running: '进行中',
      skipped: '已跳过',
    })[props.status || ''] ||
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
// hideWhenAvailable：常态「可用」不渲染徽章，只在异常/关闭等状态时显示。
const visible = computed(
  () => !(props.hideWhenAvailable && props.available !== false && props.status === 'available'),
)
</script>

<template>
  <Badge v-if="visible" :variant="variant">{{ label }}</Badge>
</template>
