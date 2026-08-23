<template>
  <CollapsibleBlock :collapsed-title="collapsedTitle" :expanded-title="'任务列表'" :open="openState"
    @update:open="toggle(openKey)">
    <template #icon>
      <svg class="w-3.5 h-3.5 text-gray-400 shrink-0" fill="none" stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
          d="M9 5H7a2 2 0 00-2 2v12a2 2 0 002 2h10a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2m-6 9l2 2 4-4" />
      </svg>
    </template>

    <template #title="{ open: o }">
      <span class="truncate min-w-0 flex-1 text-xs font-mix">
        {{ o ? '任务列表' : collapsedTitle }}
      </span>
    </template>

    <div class="w-80 flex flex-col gap-1 mt-1 border border-gray-200/80 rounded-xl bg-[#f8f9fa] p-3">
      <div class="w-full flex flex-col gap-0.5 max-h-64 overflow-y-auto overflow-hidden">
        <TaskItem v-for="task in taskItems" :key="task.id" :task="task" />
      </div>
    </div>
  </CollapsibleBlock>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import CollapsibleBlock from './CollapsibleBlock.vue';
import TaskItem from './TaskItem.vue';
import { useCollapse } from '@/composables/useCollapse';
import type { PlanStep } from '@/lib/chatTypes';

const props = defineProps<{
  plan: PlanStep[];
}>();

const { isOpen, toggle } = useCollapse();

const openKey = computed(() => {
  const hash = props.plan.map(s => `${s.step}:${s.status}`).join('|');
  return `todo-${hash}`;
});
const openState = computed(() => isOpen(openKey.value));

const statusMap: Record<string, string> = {
  inProgress: 'running',
  completed: 'done',
};

const taskItems = computed(() =>
  props.plan.map((s, idx) => ({
    id: idx,
    step: s.step,
    status: statusMap[s.status] || 'pending',
  }))
);

const doneCount = computed(() => props.plan.filter(s => s.status === 'completed').length);

const collapsedTitle = computed(() => {
  const inProgress = props.plan.find(s => s.status === 'inProgress');
  const nextPending = props.plan.find(s => s.status === 'pending');
  const countStr = `${doneCount.value}/${props.plan.length}`;
  if (inProgress) {
    return `任务列表 ${countStr} · 执行 ${inProgress.step}`;
  }
  if (doneCount.value === props.plan.length) {
    return `任务列表 ${countStr} · 全部完成`;
  }
  return `任务列表 ${countStr} · 等待开始 — ${nextPending ? nextPending.step : ''}`;
});
</script>
