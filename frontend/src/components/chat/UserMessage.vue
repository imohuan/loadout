<template>
  <!-- 展示态 -->
  <div v-if="!isEditing" class="flex flex-col items-end group">
    <!-- 图片在气泡外部 -->
    <div v-if="message.images?.length" class="flex justify-end mb-2 gap-2">
      <div
        v-for="(img, idx) in message.images"
        :key="idx"
        class="w-20 h-16 bg-white border border-gray-200 rounded-lg overflow-hidden shadow-sm flex items-center justify-center cursor-pointer"
        @click="openPreview(idx)"
      >
        <img :src="toDisplayUrl(img.data || img.path)" class="w-full h-full object-cover opacity-90 hover:opacity-100 transition-opacity" alt="attachment" />
      </div>
    </div>
    <!-- 文字气泡 -->
    <div class="max-w-[75%] bg-[#f4f4f5] rounded-2xl px-4 py-2.5 text-sm text-gray-800 leading-relaxed shadow-sm">
      <p class="whitespace-pre-wrap break-words">{{ displayContent }}</p>
    </div>
    <div class="flex items-center gap-1 mt-1 text-[11px] text-gray-400">
      <span class="opacity-0 group-hover:opacity-100 transition-opacity">{{ formattedTime }}</span>
      <button
        class="hover:text-gray-600 p-0.5 rounded inline-flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
        title="复制"
        @click="copyContent"
      >
        <svg v-if="copied" class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
        </svg>
        <svg v-else class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
        </svg>
      </button>
      <!-- <button
        class="hover:text-gray-600 p-0.5 rounded inline-flex items-center justify-center opacity-0 group-hover:opacity-100 transition-opacity"
        title="编辑"
        @click="enterEdit"
      >
        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M11 5H6a2 2 0 00-2 2v11a2 2 0 002 2h11a2 2 0 002-2v-5m-1.414-9.414a2 2 0 112.828 2.828L11.828 15H9v-2.828l8.586-8.586z" />
        </svg>
      </button> -->
      <button
        v-if="message.isGoal"
        type="button"
        class="flex items-center gap-1 text-gray-500 hover:text-gray-600 p-0.5 rounded"
        title="已设置目标"
      >
        <svg xmlns="http://www.w3.org/2000/svg" class="w-3.5 h-3.5" viewBox="0 0 24 24" fill="none"
          stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="12" cy="12" r="10" />
          <circle cx="12" cy="12" r="6" />
          <circle cx="12" cy="12" r="2" />
        </svg>
        <span>设为目标</span>
      </button>
    </div>
  </div>

  <!-- 编辑态 -->
  <div v-else class="flex flex-col items-end w-full">
    <div class="w-10/12 bg-white border border-gray-200 rounded-2xl p-3 shadow-sm">
      <textarea
        ref="editTextarea"
        v-model="draftText"
        rows="3"
        class="um-edit-textarea w-full resize-none bg-transparent text-sm text-gray-800 leading-relaxed outline-none placeholder:text-gray-400"
        placeholder="输入消息…"
      />
      <div class="flex items-center justify-end gap-2 mt-3">
        <button
          class="px-3 py-1.5 text-xs text-gray-600 hover:bg-gray-100 rounded-lg transition-colors"
          @click="cancelEdit"
        >
          取消
        </button>
        <button
          class="px-3 py-1.5 text-xs text-white bg-blue-600 hover:bg-blue-700 rounded-lg transition-colors disabled:bg-blue-300 disabled:cursor-not-allowed"
          :disabled="!canSave"
          @click="saveEdit"
        >
          保存
        </button>
      </div>
    </div>
  </div>

  <!-- 全屏预览 -->
  <AxImageViewer
    v-model:visible="viewerVisible"
    :images="viewerImages"
    :initial-index="viewerIndex"
  />
</template>

<script setup lang="ts">
import { ref, computed } from 'vue';
import type { NormalizedMessage } from '@/lib/chatTypes';
import AxImageViewer from '../ui/AxImageViewer.vue';

const props = defineProps<{
  message: NormalizedMessage;
}>();

const emit = defineEmits<{
  update: [payload: { id?: string; text: string; images: string[] }];
}>();

const isEditing = ref(false);
const draftText = ref('');
const draftImages = ref<string[]>([]);
// editTextarea 仅被 enterEdit 使用，随编辑入口一并禁用（2026-08-23）
// const editTextarea = ref<HTMLTextAreaElement | null>(null);
const copied = ref(false);
let copyTimer: ReturnType<typeof setTimeout> | null = null;

const viewerVisible = ref(false);
const viewerImages = ref<string[]>([]);
const viewerIndex = ref(0);

const canSave = computed(
  () => draftText.value.trim().length > 0 || draftImages.value.length > 0,
);

const displayContent = computed(() => {
  let text = props.message.content || '';
  if (props.message.images?.length) {
    text = text.replace(/\[Image\s*#?\d+\]/gi, '').trim();
  }
  return text;
});

const formattedTime = computed(() => {
  const ts = props.message.timestamp;
  if (!ts) return '';
  const d = new Date(ts);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
});

function toDisplayUrl(imgPath: string): string {
  if (!imgPath) return '';
  if (/^https?:\/\//.test(imgPath) || imgPath.startsWith('data:') || imgPath.startsWith('blob:')) {
    return imgPath;
  }
  return `/api/image?path=${encodeURIComponent(imgPath)}`;
}

// 编辑入口已随模板按钮一并禁用（2026-08-23），需要恢复编辑功能时取消本注释并取消上方按钮注释
// function enterEdit() {
//   draftText.value = props.message.content ?? '';
//   draftImages.value = (props.message.images || []).map((img) => img.data || img.path);
//   isEditing.value = true;
//   nextTick(() => {
//     editTextarea.value?.focus();
//   });
// }

function cancelEdit() {
  isEditing.value = false;
}

function saveEdit() {
  emit('update', {
    id: props.message.id,
    text: draftText.value,
    images: [...draftImages.value],
  });
  isEditing.value = false;
}

function openPreview(idx: number) {
  viewerImages.value = (props.message.images || []).map((img) => toDisplayUrl(img.data || img.path));
  viewerIndex.value = idx;
  viewerVisible.value = true;
}

async function copyContent() {
  try {
    await navigator.clipboard.writeText(displayContent.value);
    copied.value = true;
    if (copyTimer) clearTimeout(copyTimer);
    copyTimer = setTimeout(() => {
      copied.value = false;
    }, 2000);
  } catch {}
}
</script>

<style scoped>
.um-edit-textarea {
  field-sizing: content;
  min-height: 3rem;
  max-height: 16rem;
}
</style>
