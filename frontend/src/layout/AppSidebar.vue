<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'
import {
  RiApps2Line,
  RiCpuLine,
  RiDashboardLine,
  RiEyeLine,
  RiFileList3Line,
  RiFlaskLine,
  RiLinkM,
  RiRobot2Line,
  RiSettings3Line,
  RiShieldCheckLine,
  RiSwapLine,
} from '@remixicon/vue'

const route = useRoute()
const groups = [
  {
    label: '工作台',
    items: [
      { to: '/', label: '概览', icon: RiDashboardLine },
      { to: '/channels', label: '渠道与模型', icon: RiLinkM },
      { to: '/model-test', label: '模型测试', icon: RiFlaskLine },
      { to: '/aggregates', label: '聚合模型', icon: RiRobot2Line },
      { to: '/capability-routes', label: '能力路由', icon: RiEyeLine },
    ],
  },
  {
    label: '运行状态',
    items: [
      { to: '/model-status', label: '模型状态', icon: RiShieldCheckLine },
      { to: '/route-logs', label: '转发日志', icon: RiFileList3Line },
    ],
  },
  {
    label: '配置',
    items: [
      { to: '/integrations', label: 'MCP 管理', icon: RiApps2Line },
      { to: '/unifyai', label: 'UnifyAI 同步', icon: RiSwapLine },
      { to: '/skills', label: 'Skills', icon: RiCpuLine },
      { to: '/settings', label: '设置', icon: RiSettings3Line },
    ],
  },
]
function isActive(to: string) {
  return computed(() => route.path === to || (to !== '/' && route.path.startsWith(to)))
}
</script>

<template>
  <Sidebar collapsible="icon">
    <SidebarHeader>
      <RouterLink
        to="/"
        class="flex h-10 items-center gap-2 px-2 text-foreground transition-[padding,gap] group-data-[collapsible=icon]:justify-center group-data-[collapsible=icon]:gap-0 group-data-[collapsible=icon]:px-0"
        ><span class="grid size-8 shrink-0 place-items-center bg-primary text-primary-foreground">
          <RiRobot2Line size="17" /> </span
        ><span class="truncate font-semibold group-data-[collapsible=icon]:hidden"
          >Loadout</span
        ></RouterLink
      >
    </SidebarHeader>
    <SidebarContent>
      <SidebarGroup v-for="group in groups" :key="group.label" class="py-1">
        <SidebarGroupLabel>{{ group.label }}</SidebarGroupLabel>
        <SidebarGroupContent>
          <SidebarMenu>
            <SidebarMenuItem v-for="item in group.items" :key="item.to">
              <SidebarMenuButton
                as-child
                :is-active="isActive(item.to).value"
                :tooltip="item.label"
              >
                <RouterLink :to="item.to">
                  <component :is="item.icon" size="18" /><span>{{ item.label }}</span>
                </RouterLink>
              </SidebarMenuButton>
            </SidebarMenuItem>
          </SidebarMenu>
        </SidebarGroupContent>
      </SidebarGroup>
    </SidebarContent>
    <SidebarFooter class="group-data-[collapsible=icon]:hidden">
      <p class="px-2 text-xs text-muted-foreground group-data-[collapsible=icon]:hidden">
        模型路由控制台
      </p>
    </SidebarFooter>
  </Sidebar>
</template>
