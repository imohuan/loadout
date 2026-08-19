<script setup lang="ts">
import { computed, onMounted, onScopeDispose, reactive, ref } from 'vue'
import { RiDeleteBinLine, RiRefreshLine } from '@remixicon/vue'
import { useRouteLogs } from '@/composables/useRouteLogs'
import type { RouteLogFilters } from '@/composables/useRouteLogs'
import { useChannels } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { RouteLog } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import RouteLogFiltersForm from '@/components/route-logs/RouteLogFilters.vue'
import RouteLogTable from '@/components/route-logs/RouteLogTable.vue'

const service = useRouteLogs()
const channelService = useChannels()
const filters = ref<RouteLogFilters>({})
const {
  data: logs,
  loading,
  refreshing,
  refresh,
} = useListLoader(() => service.list(filters.value))
const { data: channels } = useListLoader(channelService.list)
const { pending, run } = useAsyncTask()
const { confirmDialog } = useConfirm()

// 详情缓存：定时/手动刷新时新列表会替换原数组，列表对象上的 attempts 会丢失，
// 用 Map 按 request_id 保留已展开的 attempts/error_message，重新挂回展示对象。
const detailsMap = reactive(new Map<string, RouteLog>())
// 记录"曾经展开过"的 request_id，用于定时刷新判断需要后台拉哪些
const expandedIds = reactive(new Set<string>())
const loadingDetail = ref('')

// 终态日志的详情重试次数：第一次拉详情可能因请求刚完成/写入中而 attempts 为空，
// 后续轮询继续重试最多 RETRY_MAX 次，避免 UI 长期显示不完整。
const detailRetryCount = reactive(new Map<string, number>())
const DETAIL_RETRY_MAX = 5
function shouldRefreshDetail(log: RouteLog): boolean {
  if (log.result === 'running') return true
  // 终态：详情完整（attempts 非空）则不再刷新；否则按次数重试。
  const cached = detailsMap.get(log.request_id)
  if (cached && (cached.attempts?.length ?? 0) > 0) {
    detailRetryCount.delete(log.request_id)
    return false
  }
  const tries = detailRetryCount.get(log.request_id) ?? 0
  if (tries >= DETAIL_RETRY_MAX) return false
  return true
}

const displayLogs = computed(() => {
  const list = logs.value || []
  return list.map((log) => {
    const detail = detailsMap.get(log.request_id)
    if (!detail) return log
    return {
      ...log,
      attempts: detail.attempts,
      error_message: detail.error_message ?? log.error_message,
    }
  })
})

// 拉取"已展开"的详情：进行中照常刷；终态但 attempts 缺失则继续重试最多 N 次。
// 成功但终态 attempts 仍为空、或请求失败，都计入重试次数；达上限即放弃并收起该行。
async function refreshActiveDetails() {
  const current = logs.value || []
  for (const log of current) {
    if (!expandedIds.has(log.request_id)) continue
    if (!shouldRefreshDetail(log)) continue
    const tries = (detailRetryCount.get(log.request_id) ?? 0) + 1
    detailRetryCount.set(log.request_id, tries)
    try {
      const detail = await service.detail(log.request_id)
      detailsMap.set(log.request_id, detail)
      // 终态 + attempts 为空：视为本次拉取仍不完整（继续计数，下次轮询重试）
      if (log.result !== 'running' && !(detail.attempts?.length)) {
        // 已达上限：收起该行，避免无限轮询一个本无 attempts 的请求
        if (tries >= DETAIL_RETRY_MAX) expandedIds.delete(log.request_id)
        continue
      }
    } catch {
      // 静默：定时刷新已在 silentError 模式，避免与列表刷新错误提示重复
      if (tries >= DETAIL_RETRY_MAX) expandedIds.delete(log.request_id)
    }
  }
}

// 手动刷新（先拉列表，再后台同步展开中的进行中详情）
function manualRefresh() {
  void (async () => {
    await refresh()
    await refreshActiveDetails()
  })()
}

// 3 秒定时刷新。
// timer 用模块级变量 + 启动前先清旧 + onScopeDispose 清理：
// 防止 Vite HMR 重跑 setup 时旧 interval 引用丢失导致定时器叠加（开发模式改代码后越叠越多）。
const AUTO_REFRESH_INTERVAL = 3_000
let autoTimer: ReturnType<typeof setInterval> | undefined
function stopAutoRefresh() {
  if (autoTimer) {
    clearInterval(autoTimer)
    autoTimer = undefined
  }
}
function startAutoRefresh() {
  stopAutoRefresh() // 防重：确保同一时刻只有一个定时器
  autoTimer = setInterval(() => {
    void (async () => {
      await refresh({ silentError: true })
      await refreshActiveDetails()
    })()
  }, AUTO_REFRESH_INTERVAL)
}
onMounted(startAutoRefresh)
// 组件卸载与 HMR reload 都会触发 scope dispose，比 onUnmounted 覆盖更全
onScopeDispose(stopAutoRefresh)

async function apply(next: RouteLogFilters) {
  filters.value = next
  await refresh()
  await refreshActiveDetails()
}
async function expand(log: RouteLog) {
  // 已有缓存直接展示（收起再展开瞬时显示，无 loading）
  if (detailsMap.has(log.request_id)) {
    expandedIds.add(log.request_id)
    return
  }
  expandedIds.add(log.request_id)
  loadingDetail.value = log.request_id
  try {
    const detail = await service.detail(log.request_id)
    detailsMap.set(log.request_id, detail)
  } catch {
    // 首次拉取失败：不阻塞展开，定时刷新（refreshActiveDetails）会按重试策略补拉
  } finally {
    loadingDetail.value = ''
  }
}
async function clear() {
  if (!(await confirmDialog('清空全部转发日志？此操作不可恢复。'))) return
  await run(async () => {
    await service.clear()
    detailsMap.clear()
    expandedIds.clear()
    await refresh()
  }, '转发日志已清理')
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="转发日志"
      description="按 request_id 还原路由尝试、跳过、切换和最终结果；不会保存请求正文或密钥。"
      ><template #actions
        ><Button
          variant="outline"
          :disabled="loading || refreshing || pending"
          @click="manualRefresh"
          ><RiRefreshLine :class="{ 'animate-spin': refreshing }" size="16" />刷新 </Button
        ><Button variant="destructive" :disabled="pending" @click="clear">
          <RiDeleteBinLine size="16" />清空日志
        </Button></template
      >
    </PageHeader>
    <RouteLogFiltersForm
      :channels="channels || []"
      :pending="loading || pending"
      @apply="apply"
      @reset="apply({})"
    />
    <LoadingBlock v-if="loading" />
    <RouteLogTable
      v-else
      :logs="displayLogs"
      :channels="channels || []"
      :loading-detail="loadingDetail"
      @expand="expand"
    />
  </div>
</template>
