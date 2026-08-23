<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { RiLogoutBoxRLine } from '@remixicon/vue'
import { useAuthStore } from '@/stores/auth'
import AppSidebar from './AppSidebar.vue'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const pageTitle = computed(() => String(route.meta.title || 'Loadout'))
async function logout() {
  // 后端 logout 已改为幂等（无 cookie 也返回 200），但前端再兜一层 401：
  // 会话已在别处失效时（401 unauthorized 事件已把 store 置为未登录），避免 UI 抛错卡住。
  try {
    await auth.logout()
  } catch {
    // 忽略：已登出态
  }
  await router.push({ name: 'login' })
}
</script>

<template>
  <SidebarProvider class="min-h-dvh bg-muted/40">
    <AppSidebar />
    <SidebarInset class="min-w-0 bg-background">
      <header
        class="flex h-14 shrink-0 items-center justify-between gap-3 border-b border-border px-4"
      >
        <div class="flex min-w-0 items-center gap-2">
          <SidebarTrigger aria-label="展开或收起导航" /><Separator
            orientation="vertical"
            class="h-4"
          /><Breadcrumb
            ><BreadcrumbList
              ><BreadcrumbItem class="hidden sm:block"
                ><BreadcrumbLink as-child
                  ><RouterLink to="/">Loadout</RouterLink></BreadcrumbLink
                ></BreadcrumbItem
              ><BreadcrumbSeparator class="hidden sm:block" /><BreadcrumbItem
                ><BreadcrumbPage>{{ pageTitle }}</BreadcrumbPage></BreadcrumbItem
              ></BreadcrumbList
            ></Breadcrumb
          >
        </div>
        <TooltipProvider>
          <Tooltip>
            <TooltipTrigger as-child>
              <Button variant="ghost" size="icon" aria-label="登出" @click="logout"
                ><RiLogoutBoxRLine size="18"
              /></Button>
            </TooltipTrigger>
            <TooltipContent>登出</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </header>
      <main class="mx-auto w-full max-w-screen-2xl p-4 sm:p-6 flex-1"><RouterView /></main>
    </SidebarInset>
  </SidebarProvider>
</template>
