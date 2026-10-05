<script setup lang="ts">
import { RouterView } from 'vue-router'
import Toaster from '@/components/ui/sonner/Sonner.vue'
import ConfirmDialog from '@/components/ConfirmDialog.vue'
import BootSplash from '@/components/BootSplash.vue'
import { backendReady } from '@/lib/boot'
</script>

<template>
  <!-- 后端（内嵌 Loadout Server）就绪前显示启动加载页，避免直接落到登录页
       或控制台时打到尚未监听的端口。 -->
  <BootSplash v-if="!backendReady" />
  <RouterView v-else />
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
