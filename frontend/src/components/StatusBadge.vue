<script setup lang="ts">
import { computed } from 'vue'

const props = defineProps<{ status?: string; available?: boolean }>()
const label = computed(
  () =>
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
    '未知',
)
const variant = computed(() =>
  props.available === false || ['disabled', 'failed'].includes(props.status || '')
    ? 'destructive'
    : ['cooling', 'running', 'skipped'].includes(props.status || '')
      ? 'secondary'
      : 'default',
)
</script>

<template>
  <Badge :variant="variant">{{ label }}</Badge>
</template>
