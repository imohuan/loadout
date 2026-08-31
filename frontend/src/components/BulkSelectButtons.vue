<script setup lang="ts">
import { RiCheckboxBlankLine, RiCheckboxLine, RiListCheck } from '@remixicon/vue'

const props = defineProps<{
  /** 当前列表是否已全部选中（控制全选按钮的切换态） */
  allSelected: boolean
  /** 是否可操作（列表无数据时禁用全选/反选） */
  canOperate: boolean
  /** 是否已有选中项（无选中时禁用清空） */
  hasSelection: boolean
}>()

const emit = defineEmits<{
  (e: 'select-all'): void
  (e: 'invert'): void
  (e: 'clear'): void
}>()

function onSelectAll() {
  if (props.allSelected) emit('clear')
  else emit('select-all')
}
</script>

<template>
  <div class="inline-flex shrink-0 items-center gap-2">
    <button
      type="button"
      class="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
      :aria-label="allSelected ? '取消全选' : '全选'"
      title="全选 / 取消全选"
      :disabled="!canOperate"
      @click="onSelectAll"
    >
      <RiCheckboxLine v-if="allSelected" size="14" />
      <RiCheckboxBlankLine v-else size="14" />
      {{ allSelected ? '取消全选' : '全选' }}
    </button>
    <button
      type="button"
      class="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
      aria-label="反选"
      title="反选（在当前已选中换未选）"
      :disabled="!canOperate"
      @click="emit('invert')"
    >
      <RiListCheck size="14" />
      反选
    </button>
    <button
      type="button"
      class="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 text-xs"
      aria-label="清空"
      title="清空已选"
      :disabled="!hasSelection"
      @click="emit('clear')"
    >
      <RiCheckboxBlankLine size="14" />
      清空
    </button>
  </div>
</template>
