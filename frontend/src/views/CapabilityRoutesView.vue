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
import {
  formatChannelGroupLabel,
  channelLevelSegments,
  groupSegmentsFor,
  mergeSegments,
} from '@/composables/useChannelRef'
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

// 路由唯一键：capability + models + channel_ids + channel_base_urls 精确匹配（数组顺序敏感）。
function key(route: CapabilityRoute) {
  return (
    route.capability +
    '|' +
    (route.models || []).join(',') +
    '|' +
    (route.channel_ids || []).join(',') +
    '|' +
    (route.channel_base_urls || []).join(',')
  )
}

// 渠道展示：空 / 含 `*` = 全渠道；否则按 base_url 分组聚合「渠道名(Key1, Key2)」
// （渠道级段无括号、Key 级段带括号——与 ChannelRef 组件规范一致）。
function channelScopeLabel(route: CapabilityRoute) {
  const ids = route.channel_ids || []
  const baseURLs = route.channel_base_urls || []
  if ((!ids.length && !baseURLs.length) || ids.includes('*')) return '全渠道'
  return mergeSegments(
    channelLevelSegments(channels.value || [], baseURLs),
    groupSegmentsFor(channels.value || [], ids),
  )
    .map(formatChannelGroupLabel)
    .join(', ')
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
  await run(
    'save',
    async () => {
      const next = [...(routes.value || []).filter((item) => key(item) !== editingKey), value]
      await routeService.replaceAll(next)
      editing.value = undefined
      editorOpen.value = false
      await refreshRoutes()
    },
    '能力路由已保存',
  )
}
async function remove(value: CapabilityRoute) {
  if (!(await confirmDialog(`删除路由「${routeTitle(value)}」？`))) return
  await run(
    `route:${key(value)}:remove`,
    async () => {
      await routeService.replaceAll((routes.value || []).filter((item) => key(item) !== key(value)))
      await refreshRoutes()
    },
    '能力路由已删除',
  )
}
// 上下移动（排序）：调整数组顺序后整体替换，后端按 position 持久化顺序。
async function moveRoute(index: number, direction: -1 | 1) {
  const list = routes.value || []
  const target = index + direction
  if (target < 0 || target >= list.length) return
  const next = [...list]
  const [item] = next.splice(index, 1)
  next.splice(target, 0, item)
  await run(
    `route:${key(item)}:move`,
    async () => {
      await routeService.replaceAll(next)
      await refreshRoutes()
    },
    '能力路由顺序已更新',
  )
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
      @move="moveRoute"
    />
  </div>
</template>
