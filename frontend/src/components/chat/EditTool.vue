<template>
  <CollapsibleBlock
    :collapsed-title="collapsed"
    :expanded-title="expanded"
    :open="openState"
    @update:open="toggle(openKey)"
  >
    <template #icon>
      <svg class="w-3.5 h-3.5 text-gray-400 group-hover:text-gray-500 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
      </svg>
    </template>

    <template #title>
      <span class="truncate text-xs font-mix">
        <span>已编辑</span>
        <span class="underline underline-offset-2 px-2 text-gray-600">{{ fileName }}</span>
        <span v-if="added > 0" class="text-emerald-600">+{{ added }}</span>
        <span v-if="removed > 0" class="text-rose-500">-{{ removed }}</span>
      </span>
    </template>

    <div class="mt-1 border border-gray-200 rounded-xl overflow-hidden shadow-sm text-xs font-mono">
      <div class="bg-[#f8f9fa] px-3.5 py-2 border-b border-gray-200/80 flex items-center justify-between text-gray-500 text-[11px]">
        <div class="flex items-center gap-2">
          <span class="font-semibold text-gray-700">{{ fileName }}</span>
          <span v-if="added > 0" class="text-emerald-600">+{{ added }}</span>
          <span v-if="removed > 0" class="text-rose-500">-{{ removed }}</span>
        </div>
        <button class="hover:text-gray-700">
          <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
          </svg>
        </button>
      </div>
      <div class="bg-white divide-y divide-transparent">
        <div
          v-for="(line, lIdx) in diffLines"
          :key="lIdx"
          :class="[
            'flex',
            line.type === 'add' ? 'bg-emerald-50/70 text-emerald-900 font-medium' :
            line.type === 'remove' ? 'bg-rose-50/70 text-rose-800 font-medium' : 'text-gray-600 hover:bg-gray-50'
          ]"
        >
          <span :class="[
            'w-10 select-none text-right pr-3 py-0.5',
            line.type === 'add' ? 'text-emerald-500 bg-emerald-100/50' :
            line.type === 'remove' ? 'text-rose-400 bg-rose-100/50' : 'text-gray-400'
          ]">{{ line.num }}</span>
          <span class="py-0.5 px-2">{{ line.content }}</span>
        </div>
      </div>
    </div>
  </CollapsibleBlock>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import CollapsibleBlock from './CollapsibleBlock.vue';
import { useCollapse } from '@/composables/useCollapse';
import type { ToolCall } from '@/lib/chatTypes';

const props = defineProps<{
  toolCall: ToolCall;
}>();

const { isOpen, toggle } = useCollapse();

const openKey = 'tool-' + (props.toolCall.id || 'unknown');

const openState = computed(() => isOpen(openKey));

interface DiffLine {
  num: number;
  type: 'add' | 'remove' | 'normal';
  content: string;
}

const fileName = computed(() => {
  const input = props.toolCall.toolInput;
  if (input && typeof input === 'object' && 'filePath' in input) {
    return String((input as Record<string, unknown>).filePath);
  }
  if (input && typeof input === 'object' && 'path' in input) {
    return String((input as Record<string, unknown>).path);
  }
  return 'unknown file';
});

const added = computed(() => {
  if (props.toolCall.toolResult?.content) {
    const m = props.toolCall.toolResult.content.match(/\+(\d+)/);
    return m ? parseInt(m[1]) : 0;
  }
  return 0;
});

const removed = computed(() => {
  if (props.toolCall.toolResult?.content) {
    const m = props.toolCall.toolResult.content.match(/-(\d+)/);
    return m ? parseInt(m[1]) : 0;
  }
  return 0;
});

function parseDiff(raw: string): DiffLine[] {
  const lines: DiffLine[] = [];
  let num = 0;
  for (const line of raw.split('\n')) {
    num++;
    if (line.startsWith('+')) {
      lines.push({ num, type: 'add', content: line.slice(1) });
    } else if (line.startsWith('-')) {
      lines.push({ num, type: 'remove', content: line.slice(1) });
    } else {
      lines.push({ num, type: 'normal', content: line });
    }
  }
  return lines;
}

const diffLines = computed(() => {
  const content = props.toolCall.toolResult?.content || '';
  return parseDiff(content);
});

const collapsed = computed(() => {
  return '已编辑 ' + fileName.value;
});

const expanded = computed(() => {
  return '已编辑 ' + fileName.value;
});
</script>