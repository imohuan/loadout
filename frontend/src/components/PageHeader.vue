<script setup lang="ts">
defineProps<{ title: string; description: string }>()
</script>

<template>
  <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
    <div class="min-w-0">
      <h1 class="text-xl font-semibold tracking-normal text-foreground">{{ title }}</h1>
      <p class="mt-1 text-sm leading-6 text-muted-foreground">{{ description }}</p>
    </div>
    <div class="flex shrink-0 items-center gap-2"><slot name="actions" /></div>
  </div>
</template>
