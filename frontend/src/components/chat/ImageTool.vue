<template>
  <CollapsibleBlock
    :collapsed-title="collapsedTitle"
    :expanded-title="expandedTitle"
    :open="openState"
    @update:open="toggle(openKey)"
  >
    <template #icon>
      <svg class="w-3.5 h-3.5 text-gray-400 group-hover:text-gray-500 shrink-0" fill="none" stroke="currentColor" stroke-width="2" viewBox="0 0 24 24" stroke-linecap="round" stroke-linejoin="round">
        <rect x="3" y="3" width="18" height="18" rx="2" ry="2" />
        <circle cx="8.5" cy="8.5" r="1.5" />
        <path d="M21 15l-5-5L5 21" />
      </svg>
    </template>

    <template #title="{ open: o }">
      <span class="truncate min-w-0 flex-1 text-xs font-mix">
        {{ o ? expandedTitle : collapsedTitle }}
      </span>
    </template>

    <div v-if="images.length" class="mt-1 border border-gray-200/80 rounded-xl bg-[#f8f9fa] p-3 text-xs">
      <div class="text-[11px] text-gray-400 font-sans mb-2">
        {{ images.length }} 张图片
      </div>
      <div class="flex flex-row flex-wrap gap-2">
        <div
          v-for="(src, idx) in images"
          :key="idx"
          class="w-32 h-32 rounded-lg overflow-hidden border border-gray-200/60"
        >
          <AxImage
            :src="src"
            :preview-list="images"
            :preview-index="idx"
            object-fit="cover"
            @preview="handlePreview"
          />
        </div>
      </div>
    </div>
    <div v-else class="mt-1 border border-gray-200/80 rounded-xl bg-[#f8f9fa] p-3 text-xs text-gray-400 font-mono">
      无可预览图片
    </div>

    <AxImageViewer
      v-model:visible="viewerVisible"
      :images="images"
      :initial-index="viewerIndex"
    />
  </CollapsibleBlock>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';
import CollapsibleBlock from './CollapsibleBlock.vue';
import AxImage from '../ui/AxImage.vue';
import AxImageViewer from '../ui/AxImageViewer.vue';
import { useCollapse } from '@/composables/useCollapse';
import type { ToolCall } from '@/lib/chatTypes';

const props = defineProps<{
  toolCall: ToolCall;
}>();

const { isOpen, toggle } = useCollapse();
const openKey = 'tool-' + (props.toolCall.id || 'unknown');
const openState = computed(() => isOpen(openKey));

const viewerVisible = ref(false);
const viewerIndex = ref(0);

function handlePreview(_src: string, _list: string[], index: number) {
  viewerIndex.value = index;
  viewerVisible.value = true;
}

const images = computed<string[]>(() => {
  const collected: string[] = [];

  const fromResult = (raw: unknown) => {
    if (!raw) return;
    if (typeof raw === 'string') {
      const trimmed = raw.trim();
      if (trimmed.startsWith('data:image/') || /^https?:\/\//.test(trimmed)) {
        collected.push(trimmed);
      }
      return;
    }
    if (Array.isArray(raw)) {
      raw.forEach(fromResult);
      return;
    }
    if (typeof raw === 'object') {
      const obj = raw as Record<string, unknown>;
      if (typeof obj.image_url === 'string') collected.push(obj.image_url);
      else if (typeof obj.url === 'string') collected.push(obj.url);
      else if (typeof obj.data === 'string' && obj.data.startsWith('data:image/')) collected.push(obj.data);
      else if (Array.isArray(obj.content)) obj.content.forEach(fromResult);
    }
  };

  fromResult(props.toolCall.toolInput);
  fromResult(props.toolCall.toolResult?.content);

  return collected.filter((s) => typeof s === 'string' && (s.startsWith('data:image/') || /^https?:\/\//.test(s) || s.startsWith('blob:')));
});

const collapsedTitle = computed(() => {
  const n = images.value.length;
  return `查看图片${n ? ` (${n})` : ''}`;
});

const expandedTitle = computed(() => {
  return collapsedTitle.value;
});
</script>
