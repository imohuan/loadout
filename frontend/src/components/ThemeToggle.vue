<script setup lang="ts">
import { useDark, useToggle } from '@vueuse/core'
import { RiMoonLine, RiSunLine } from '@remixicon/vue'

const isDark = useDark()
const toggleDark = useToggle(isDark)
</script>

<template>
  <Button variant="ghost" size="icon" aria-label="切换明暗主题" @click="toggleDark()">
    <RiSunLine v-if="isDark" size="18" />
    <RiMoonLine v-else size="18" />
  </Button>
</template>
