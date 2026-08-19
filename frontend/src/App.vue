<script setup lang="ts">
import { RouterView } from 'vue-router'
import Toaster from '@/components/ui/sonner/Sonner.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
</script>

<template>
  <RouterView />
  <Teleport to="body">
    <Toaster
      rich-colors
      position="bottom-right"
      :duration="3000"
      close-button-aria-label="关闭通知"
    />
    <ConfirmDialog />
  </Teleport>
</template>
