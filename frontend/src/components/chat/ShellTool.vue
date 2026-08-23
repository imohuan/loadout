<template>
  <CollapsibleBlock :collapsed-title="collapsed" :expanded-title="expanded" :open="openState" :disabled="isEmptyOutput"
    @update:open="toggle(openKey)">
    <template #title="{ open: o }">
      <span class="truncate min-w-0 flex-1 text-xs font-mix">
        {{ o ? expanded : collapsed }}
      </span>
    </template>

    <div class="mt-1 border border-gray-200/80 rounded-xl bg-[#f8f9fa] p-3 text-xs font-mono">
      <div class="text-[11px] text-gray-400 font-sans mb-1.5">
        {{ label }}
      </div>
      <template v-if="commandText">
        <div class="line-clamp-2 overflow-hidden text-gray-600 mb-2 select-all">
          <template v-if="isMcp">调用 {{ commandText }}</template>
          <template v-else>$ {{ commandText }}</template>
        </div>
      </template>
      <div v-if="outputText"
        class="overflow-auto no-scrollbar pb-1 text-gray-700 whitespace-pre-wrap break-all border-t border-gray-200/50 pt-2 max-h-80">
        {{ outputText }}
      </div>
      <div v-if="toolCall.status === 'done'" class="flex items-center justify-end text-[11px] pt-1">
        <div class="flex items-center gap-1 text-gray-500 font-sans font-medium">
          <svg class="w-3.5 h-3.5 text-gray-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
          </svg>
          <span>成功</span>
        </div>
      </div>
      <div v-else-if="toolCall.status === 'running'" class="flex items-center justify-end text-[11px] pt-1">
        <div class="flex items-center gap-1 text-yellow-500 font-sans font-medium">
          <span>运行中...</span>
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

const isCodegraph = computed(() =>
  (props.toolCall.toolName || '').toLowerCase().includes('codegraph')
);

const isMcp = computed(() => {
  const name = (props.toolCall.toolName || '').toLowerCase();
  // MCP 工具命名惯例：mcp__server__tool 或 mcp.server/tool
  return name.includes('mcp') && (name.includes('__') || name.includes('.') || name.includes('/'));
});

const isShell = computed(() => {
  const name = (props.toolCall.toolName || '').toLowerCase();
  return name.includes('bash') || name.includes('shell') || name.includes('command');
});

const label = computed(() => {
  if (isCodegraph.value) return 'Codegraph';
  if (isMcp.value) return 'MCP 工具';
  if (isShell.value) return 'Shell';
  return props.toolCall.toolName || 'Tool';
});

const commandText = computed(() => {
  if (isShell.value || isCodegraph.value) {
    if (typeof props.toolCall.toolInput?.command === 'string') return props.toolCall.toolInput.command;
    if (typeof props.toolCall.toolInput === 'string') {
      try {
        return JSON.parse(props.toolCall.toolInput).command;
      } catch {
        return props.toolCall.toolInput;
      }
    }
    if (props.toolCall.commandActions?.[0]?.command) return props.toolCall.commandActions[0].command;
    return null;
  }
  const input = props.toolCall.toolInput;
  if (!input) return null;
  if (typeof input === 'string') return input;
  try {
    return JSON.stringify(input, null, 2);
  } catch {
    return null;
  }
});

const outputText = computed(() => {
  const raw = props.toolCall.toolResult?.content;
  if (!raw) return null;
  return raw;
});

const isEmptyOutput = computed(() => !outputText.value);

const collapsed = computed(() => {
  if (isMcp.value) return props.toolCall.toolName || 'MCP 工具';
  if (isShell.value || isCodegraph.value) {
    return commandText.value || props.toolCall.toolName || '';
  }
  return props.toolCall.toolName || 'Tool';
});

const expanded = computed(() => {
  if (isCodegraph.value) return props.toolCall.toolName || 'Codegraph';
  if (isMcp.value) return props.toolCall.toolName || 'MCP 工具';
  if (isShell.value) return 'Ran command';
  return props.toolCall.toolName || 'Tool';
});
</script>
