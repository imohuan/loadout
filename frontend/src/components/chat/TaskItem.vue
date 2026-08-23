<template>
  <div class="flex items-start gap-2.5 p-1.5 rounded-xl hover:bg-gray-50 transition-colors cursor-default">
    <!-- running -->
    <span v-if="task.status === 'running'" class="w-3.5 h-3.5 rounded-full border-2 border-gray-300 border-t-gray-700 inline-block mt-0.5 shrink-0 animate-spin"></span>
    <!-- done: circle with checkmark -->
    <span v-else-if="task.status === 'done'" class="w-3.5 h-3.5 rounded-full border border-gray-300 flex items-center justify-center mt-0.5 shrink-0">
      <svg class="w-2 h-2 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><polyline points="20 6 9 17 4 12"/></svg>
    </span>
    <!-- error: circle with X -->
    <span v-else-if="task.status === 'error'" class="w-3.5 h-3.5 rounded-full border border-gray-300 flex items-center justify-center mt-0.5 shrink-0">
      <svg class="w-2 h-2 text-gray-400" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="3" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
    </span>
    <!-- pending: plain hollow circle -->
    <span v-else class="w-3.5 h-3.5 rounded-full border border-gray-300 mt-0.5 shrink-0"></span>

    <span
      :class="[
        'leading-snug',
        task.status === 'done' ? 'text-gray-400 italic line-through' : 'text-gray-800',
      ]"
      style="font-size:12.5px"
    >{{ task.step }}</span>
  </div>
</template>

<script setup lang="ts">
interface TaskItemData {
  status: string
  step: string
}

defineProps<{
  task: TaskItemData
}>()
</script>
