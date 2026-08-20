<script setup lang="ts">
import { ref } from 'vue'
import { RiAddLine, RiRefreshLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useCapabilityRoutes } from '@/composables/useCapabilityRoutes'
import { useChannels } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { CapabilityRoute } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import CapabilityRouteEditor from '@/components/capability-routes/CapabilityRouteEditor.vue'
import CapabilityRouteTable from '@/components/capability-routes/CapabilityRouteTable.vue'

const routeService = useCapabilityRoutes()
const channelService = useChannels()
const {
  data: routes,
  loading: routesLoading,
  refreshing: routesRefreshing,
  refresh: refreshRoutes,
} = useListLoader(routeService.list)
const {
  data: channels,
  loading: channelLoading,
  refreshing: channelsRefreshing,
  refresh: refreshChannels,
} = useListLoader(channelService.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()
const editing = ref<CapabilityRoute>()
const editorOpen = ref(false)
const loading = () => routesLoading.value || channelLoading.value
const refreshing = () => routesRefreshing.value || channelsRefreshing.value
async function refresh() {
  await Promise.all([refreshRoutes(), refreshChannels()])
}

// 路由唯一键：capability + models + channel_ids 精确匹配（数组顺序敏感）。
function key(route: CapabilityRoute) {
  return (
    route.capability +
    '|' +
    (route.models || []).join(',') +
    '|' +
    (route.channel_ids || []).join(',')
  )
}

// 渠道展示：`*` = 通用（全匹配）；空 = 全渠道；否则渠道名列表。
function channelScopeLabel(route: CapabilityRoute) {
  const ids = route.channel_ids || []
  if (!ids.length) return '全渠道'
  if (ids.includes('*')) return '通用（全匹配）'
  return ids
    .map((id) => channels.value?.find((c) => c.id === id)?.name || id)
    .join('、')
}
function routeTitle(route: CapabilityRoute) {
  return `${route.models.join(',')}（${channelScopeLabel(route)}）× ${route.capability}`
}

function openAdd() {
  editing.value = undefined
  editorOpen.value = true
}
function openEdit(value: CapabilityRoute) {
  editing.value = value
  editorOpen.value = true
}
async function save(value: CapabilityRoute) {
  const editingKey = editing.value ? key(editing.value) : ''
  const duplicate = (routes.value || []).some(
    (item) => key(item) === key(value) && key(item) !== editingKey,
  )
  if (duplicate) {
    toast.error(`路由「${routeTitle(value)}」已存在`)
    return
  }
  await run('save', async () => {
    const next = [
      ...(routes.value || []).filter((item) => key(item) !== editingKey),
      value,
    ]
    await routeService.replaceAll(next)
    editing.value = undefined
    editorOpen.value = false
    await refreshRoutes()
  }, '能力路由已保存')
}
async function remove(value: CapabilityRoute) {
  if (!(await confirmDialog(`删除路由「${routeTitle(value)}」？`))) return
  await run(`route:${key(value)}:remove`, async () => {
    await routeService.replaceAll(
      (routes.value || []).filter((item) => key(item) !== key(value)),
    )
    await refreshRoutes()
  }, '能力路由已删除')
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="能力路由"
      description="给目标模型附加能力：视觉（图片识别替换）或敏感词过滤（请求体整体替换）。"
      ><template #actions
        ><Button variant="outline" :disabled="loading() || refreshing()" @click="refresh"
          ><RiRefreshLine :class="{ 'animate-spin': refreshing() }" size="16" />刷新 </Button
        ><Button @click="openAdd"> <RiAddLine size="16" />添加能力路由 </Button></template
      >
    </PageHeader>
    <CapabilityRouteEditor
      v-model:open="editorOpen"
      :route="editing"
      :channels="channels || []"
      :pending="isPending('save')"
      @save="save"
      @cancel="editorOpen = false"
    />
    <LoadingBlock v-if="loading()" />
    <CapabilityRouteTable
      v-else
      :routes="routes || []"
      :channels="channels || []"
      :is-pending="isPending"
      @edit="openEdit"
      @remove="remove"
    />
  </div>
</template>
