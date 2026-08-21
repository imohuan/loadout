<script setup lang="ts">
defineProps<{ title: string; description: string }>()
</script>

<template>
  <div
    class="flex min-h-52 flex-col items-center justify-center border border-dashed border-border px-6 text-center"
  >
    <p class="font-medium text-foreground">{{ title }}</p>
    <p class="mt-1 max-w-md text-sm leading-6 text-muted-foreground">{{ description }}</p>
    <div v-if="$slots.action" class="mt-4"><slot name="action" /></div>
  </div>
</template>
