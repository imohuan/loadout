<template>
  <ErrorCard
    v-if="isCodexError"
    :tool-call="toolCall"
  />
  <TodoListCard
    v-else-if="isUpdatePlan && parsedPlan.length > 0"
    :plan="parsedPlan"
  />
  <VisionLookTool v-else-if="isVisionLook" :tool-call="toolCall" />
  <ImageTool v-else-if="isImage" :tool-call="toolCall" />
  <EditTool v-else-if="isEdit" :tool-call="toolCall" />
  <ShellTool v-else :tool-call="toolCall" />
</template>

<script setup lang="ts">
import { computed } from 'vue';
import type { ToolCall, PlanStep } from '@/lib/chatTypes';
import ShellTool from './ShellTool.vue';
import EditTool from './EditTool.vue';
import ImageTool from './ImageTool.vue';
import VisionLookTool from './VisionLookTool.vue';
import TodoListCard from './TodoListCard.vue';
import ErrorCard from './ErrorCard.vue';

const props = defineProps<{
  toolCall: ToolCall;
}>();

const toolNameLower = computed(() => (props.toolCall.toolName || '').toLowerCase());

const isVisionLook = computed(() => {
  const n = toolNameLower.value;
  // vision 插件：图片被替换成 <vision_img_xxx> 标记，AI 通过 look_at_image 查看。
  // 必须在 isImage 之前精确匹配，否则会被 'image' 子串误判进 ImageTool。
  return n === 'look_at_image' || n === 'lookatimage' || n === 'look-at-image';
});

const isImage = computed(() =>
  toolNameLower.value.includes('image') || toolNameLower.value.includes('view_image')
);

const isEdit = computed(() =>
  toolNameLower.value.includes('edit') ||
  toolNameLower.value.includes('write') ||
  toolNameLower.value.includes('patch') ||
  toolNameLower.value.includes('file')
);

const isUpdatePlan = computed(() =>
  toolNameLower.value.includes('update_plan')
);

const isCodexError = computed(() =>
  toolNameLower.value === 'codex_error'
);

const parsedPlan = computed<PlanStep[]>(() => {
  const input = props.toolCall.toolInput;
  if (!input) return [];

  let args: { plan?: unknown } | null = null;
  if (typeof input === 'string') {
    try { args = JSON.parse(input); } catch { return []; }
  } else if (typeof input === 'object') {
    args = input as { plan?: unknown };
  }
  if (!args || !Array.isArray(args.plan)) return [];

  return args.plan
    .filter((s): s is PlanStep => !!s && typeof s === 'object' && typeof (s as PlanStep).step === 'string')
    .map((s) => ({ step: s.step, status: normalizeStatus(s.status) }));
});

function normalizeStatus(s: string | undefined): string {
  const v = (s || '').toLowerCase();
  if (v === 'inprogress' || v === 'in_progress' || v === 'running') return 'inProgress';
  if (v === 'done' || v === 'complete' || v === 'completed') return 'completed';
  return 'pending';
}
</script>