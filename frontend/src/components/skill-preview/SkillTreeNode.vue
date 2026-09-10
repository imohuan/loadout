<script setup lang="ts">
// 技能文件树节点（递归组件）。文件夹与文件共用一种节点形状，children 为已过滤的子节点；
// 展开/折叠与选中状态由父组件（SkillPreviewDialog）统一持有，本组件只负责渲染与派发事件。
import { computed } from 'vue'
import { RiArrowDownSLine, RiArrowRightSLine } from '@remixicon/vue'
import type { SkillTreeNode } from '@/lib/skillTree'

const props = defineProps<{
  node: SkillTreeNode
  depth: number
  expanded: string[]
  selected: string
}>()

const emit = defineEmits<{
  toggle: [path: string]
  select: [path: string]
}>()

const isDir = computed(() => props.node.dir)
const isOpen = computed(() => props.expanded.includes(props.node.path))
const isSelected = computed(() => !isDir.value && props.selected === props.node.path)

// 缩进：每层 12px，基准 6px。
const indent = computed(() => `${props.depth * 12 + 6}px`)

function onClick() {
  if (isDir.value) emit('toggle', props.node.path)
  else emit('select', props.node.path)
}

// 文件大小：B / KB / MB，保留一位小数。
function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}
</script>

<template>
  <div>
    <button
      type="button"
      class="flex w-full items-center gap-1.5 rounded-sm py-1 pr-2 text-left text-sm transition-colors hover:bg-muted"
      :class="isSelected ? 'bg-primary/10 text-primary' : ''"
      :style="{ paddingLeft: indent }"
      :title="node.path"
      @click="onClick"
    >
      <!-- 目录：展开箭头；文件：占位保持对齐 -->
      <template v-if="isDir">
        <RiArrowDownSLine v-if="isOpen" class="size-3.5 shrink-0 text-muted-foreground" />
        <RiArrowRightSLine v-else class="size-3.5 shrink-0 text-muted-foreground" />
      </template>
      <span v-else class="size-3.5 shrink-0" />
      <span class="min-w-0 flex-1 truncate">{{ node.name }}</span>
      <span v-if="!isDir" class="shrink-0 text-[11px] tabular-nums text-muted-foreground">
        {{ formatSize(node.size) }}
      </span>
    </button>

    <template v-if="isDir && isOpen">
      <SkillTreeNode
        v-for="child in node.children"
        :key="child.path"
        :node="child"
        :depth="depth + 1"
        :expanded="expanded"
        :selected="selected"
        @toggle="emit('toggle', $event)"
        @select="emit('select', $event)"
      />
    </template>
  </div>
</template>
