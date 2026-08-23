<template>
  <CollapsibleBlock :collapsed-title="collapsed" :expanded-title="expanded" :open="openState"
    @update:open="toggle(openKey)">
    <template #icon>
      <svg class="w-3.5 h-3.5 text-gray-400 shrink-0 stroke-[1.8]" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" d="M8.288 15.038a5.25 5.25 0 017.424 0M5.106 11.856c3.807-3.808 9.98-3.808 13.788 0M1.924 8.674c5.565-5.565 14.587-5.565 20.152 0M12.53 18.22a.75.75 0 11-1.06 0 .75.75 0 011.06 0z" />
      </svg>
    </template>
    <template #title="{ open: o }">
      <span class="truncate text-xs font-mix text-gray-500">
        {{ o ? expanded : collapsed }}
      </span>
    </template>

    <div class="w-full bg-white border border-gray-200/80 rounded-2xl px-4 py-3 flex items-center gap-3 shadow-[0_1px_2px_rgba(0,0,0,0.02)] transition-all hover:border-gray-300 mt-2">
      <svg class="w-[18px] h-[18px] text-gray-700 shrink-0 stroke-[1.8]" xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor">
        <path stroke-linecap="round" stroke-linejoin="round" d="M12 9v3.75m9-.75a9 9 0 11-18 0 9 9 0 0118 0zm-9 3.75h.008v.008H12v-.008z" />
      </svg>
      <span class="text-sm text-gray-700 font-normal tracking-wide">{{ displayMessage }}</span>
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

const openKey = 'error-' + (props.toolCall.id || 'unknown');
const openState = computed(() => isOpen(openKey));

interface ErrorData {
  message?: string;
  retry?: boolean;
  retryCount?: { current: number; total: number } | null;
  additionalDetails?: string | null;
}

const errorData = computed<ErrorData>(() => {
  const input = props.toolCall.toolInput;
  if (!input) return {};
  if (typeof input === 'string') {
    try { return JSON.parse(input); } catch { return {}; }
  }
  if (typeof input === 'object') return input as ErrorData;
  return {};
});

const retryText = computed(() => {
  const rc = errorData.value.retryCount;
  if (rc && rc.current && rc.total) {
    return `正在重新连接 ${rc.current}/${rc.total}`;
  }
  if (errorData.value.retry) {
    return '正在重新连接';
  }
  return '连接已断开';
});

const displayMessage = computed(() => {
  if (errorData.value.additionalDetails) {
    return errorData.value.additionalDetails;
  }
  return errorData.value.message || '发生未知错误';
});

const collapsed = computed(() => retryText.value);
const expanded = computed(() => retryText.value);
</script>
