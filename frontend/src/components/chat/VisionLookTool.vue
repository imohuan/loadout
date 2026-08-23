<template>
  <CollapsibleBlock
    :collapsed-title="collapsedTitle"
    :expanded-title="expandedTitle"
    :open="openState"
    @update:open="toggle(openKey)"
  >
    <template #icon>
      <svg class="w-3.5 h-3.5 text-gray-400 group-hover:text-gray-500 shrink-0" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" stroke-linecap="round" stroke-linejoin="round">
        <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7-10-7-10-7z" />
        <circle cx="12" cy="12" r="3" />
      </svg>
    </template>

    <template #title="{ open: o }">
      <span class="truncate min-w-0 flex-1 text-xs font-mix">
        {{ o ? expandedTitle : collapsedTitle }}
      </span>
    </template>

    <div class="mt-1 border border-gray-200/80 rounded-xl bg-[#f8f9fa] p-3 text-xs font-mono">
      <template v-if="promptText">
        <div class="text-[11px] text-gray-400 font-sans mb-1.5">识别方向</div>
        <div class="line-clamp-2 overflow-hidden text-gray-600 mb-2 select-all">
          {{ promptText }}
        </div>
      </template>

      <div v-if="resultText"
        :class="['overflow-auto no-scrollbar pb-1 text-gray-700 whitespace-pre-wrap break-all pt-2 max-h-80',
                 promptText ? 'border-t border-gray-200/50' : '']">
        <div class="text-[11px] text-gray-400 font-sans mb-1.5">识别结果</div>
        {{ resultText }}
      </div>

      <div v-if="toolCall.status === 'done'" class="flex items-center justify-end text-[11px] pt-1">
        <div class="flex items-center gap-1 font-sans font-medium"
          :class="isError ? 'text-red-500' : 'text-gray-500'">
          <svg v-if="!isError" class="w-3.5 h-3.5 text-gray-500" fill="none" stroke="currentColor" viewBox="0 0 24 24">
            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
          </svg>
          <span>{{ isError ? '失败' : '成功' }}</span>
        </div>
      </div>
      <div v-else-if="toolCall.status === 'running'" class="flex items-center justify-end text-[11px] pt-1">
        <div class="flex items-center gap-1 text-yellow-500 font-sans font-medium">
          <span>识别中...</span>
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

/**
 * look_at_image 专用渲染：vision 插件把图片替换成 <vision_img_xxx> 标记后，
 * AI 通过本工具查看图片。输入是 { image_id, prompt }，输出是纯文本描述，
 * 不属于 ImageTool（那按 OpenAI 多模态 URL 格式设计，没有 URL 会走空占位）。
 * 视觉对齐 ShellTool 的折叠卡片风格。
 */
const props = defineProps<{
  toolCall: ToolCall;
}>();

const { isOpen, toggle } = useCollapse();

const openKey = 'tool-' + (props.toolCall.id || 'unknown');
const openState = computed(() => isOpen(openKey));

const parsedInput = computed<Record<string, unknown> | null>(() => {
  const input = props.toolCall.toolInput;
  if (!input) return null;
  if (typeof input === 'string') {
    try {
      return JSON.parse(input);
    } catch {
      return null;
    }
  }
  if (typeof input === 'object') return input as Record<string, unknown>;
  return null;
});

const imageId = computed(() => {
  const v = parsedInput.value?.image_id;
  return typeof v === 'string' ? v : null;
});

const imageIds = computed<string[]>(() => {
  const v = parsedInput.value?.image_ids;
  if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string');
  if (imageId.value) return [imageId.value];
  return [];
});

const promptText = computed(() => {
  const v = parsedInput.value?.prompt;
  return typeof v === 'string' && v.trim() ? v.trim() : null;
});

const resultText = computed(() => props.toolCall.toolResult?.content ?? null);

const isError = computed(() => props.toolCall.toolResult?.isError === true);

const title = computed(() => {
  const n = imageIds.value.length;
  if (n > 1) return `查看图片 (${n})`;
  if (imageId.value) return `查看图片 ${imageId.value}`;
  return '查看图片';
});

const collapsedTitle = computed(() => title.value);
const expandedTitle = computed(() => title.value);
</script>
