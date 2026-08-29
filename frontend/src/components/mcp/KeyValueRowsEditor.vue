<script setup lang="ts">
import { RiAddLine, RiDeleteBinLine } from '@remixicon/vue'
import { addRow, removeRow, type KeyValueRow } from '@/composables/useEnvRows'

/**
 * 键值行编辑器：环境变量 / Header 等「key + value + 增删」行的通用 UI。
 * 直接把 `rows` 数组传进来（引用共享），组件内部通过 useEnvRows 的 addRow/removeRow
 * 原位增删，行为与原先各面板内联实现完全一致。
 */
defineProps<{
  rows: KeyValueRow[]
  keyPlaceholder?: string
  valuePlaceholder?: string
  addLabel: string
  removeAriaLabel: string
}>()
</script>

<template>
  <div class="space-y-2">
    <div v-for="(row, index) in rows" :key="index" class="flex gap-2">
      <Input v-model="row.key" :placeholder="keyPlaceholder || 'KEY'" />
      <Input v-model="row.value" :placeholder="valuePlaceholder || '值'" />
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              :aria-label="removeAriaLabel"
              @click="removeRow(rows, index)"
            >
              <RiDeleteBinLine size="16" />
            </Button>
          </TooltipTrigger>
          <TooltipContent>{{ removeAriaLabel }}</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
    <Button variant="outline" size="sm" @click="addRow(rows)">
      <RiAddLine size="16" />{{ addLabel }}
    </Button>
  </div>
</template>
