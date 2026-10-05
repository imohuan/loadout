<script setup lang="ts">
import { computed } from 'vue'
import { RiLoader4Line, RiRobot2Line, RiRestartLine } from '@remixicon/vue'
import { bootElapsedMs, bootSlow } from '@/lib/boot'

const seconds = computed(() => Math.max(1, Math.floor(bootElapsedMs.value / 1000)))

// reload 重新加载整个 webview：即使后端仍在启动也能重新走一遍探测流程。
function reload() {
  window.location.reload()
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-muted/40 px-4">
    <section class="w-full max-w-sm" aria-live="polite" aria-busy="true">
      <Card class="rounded-md shadow-sm">
        <CardHeader class="space-y-3">
          <div class="grid size-10 place-items-center bg-primary text-primary-foreground">
            <RiRobot2Line size="22" aria-hidden="true" />
          </div>
          <div class="space-y-1">
            <CardTitle>正在启动 Loadout</CardTitle>
            <CardDescription>正在启动本地服务，就绪后将自动进入登录页面</CardDescription>
          </div>
        </CardHeader>
        <CardContent class="space-y-4">
          <div class="flex items-center gap-2 text-sm text-muted-foreground">
            <RiLoader4Line class="animate-spin" size="18" aria-hidden="true" />
            <span>请稍候，正在准备服务…</span>
          </div>
          <p v-if="bootSlow" class="text-xs text-muted-foreground">
            已等待 {{ seconds }} 秒，启动时间超出预期。
          </p>
          <Button v-if="bootSlow" variant="outline" class="w-full" @click="reload">
            <RiRestartLine size="16" aria-hidden="true" />
            重新加载
          </Button>
        </CardContent>
      </Card>
    </section>
  </main>
</template>
