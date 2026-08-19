<script setup lang="ts">
defineProps<{ label: string; value: string | number }>()
</script>

<template>
  <Card class="rounded-md"
    ><CardContent class="p-4"
      ><p class="text-xs font-medium text-muted-foreground">{{ label }}</p>
      <p class="mt-2 truncate text-2xl font-semibold tabular-nums text-foreground">
        {{ value }}
      </p></CardContent
    ></Card
  >
</template>
