<script setup lang="ts">
import { computed, onMounted, onScopeDispose, reactive, ref } from 'vue'
import { RiDeleteBinLine, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
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
const page = ref(1)
const pageSize = ref(20)
const {
  data: logsData,
  loading,
  refreshing,
  refresh,
} = useListLoader(() => service.list(filters.value, { page: page.value, pageSize: pageSize.value }))
// 后端真分页：logs 为当前页记录，total 为满足过滤条件的全量条数
const logs = computed(() => logsData.value?.items ?? [])
const total = computed(() => logsData.value?.total ?? 0)
// 翻页/改每页条数：更新分页状态后重新拉取当前页（3s 定时刷新同样带当前 page/pageSize）
function onPageChange(nextPage: number, nextSize: number) {
  page.value = nextPage
  pageSize.value = nextSize
  void refresh()
}
const { data: channels } = useListLoader(channelService.list)
const { run, isPending } = useAsyncTask()
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
  // 缓存里有 running 的 attempt：上次拉详情时还在流式传输中、此后请求转终态但缓存未更新。
  // 强制重拉一次（不计入 retry 计数），避免 UI 长期显示"attempt 进行中 + request 已成功"的撕裂。
  const cached = detailsMap.get(log.request_id)
  if (cached?.attempts?.some((a) => a.result === 'running')) return true
  // 终态：详情完整（attempts 非空）则不再刷新；否则按次数重试。
  if (cached && (cached.attempts?.length ?? 0) > 0) {
    detailRetryCount.delete(log.request_id)
    return false
  }
  const tries = detailRetryCount.get(log.request_id) ?? 0
  if (tries >= DETAIL_RETRY_MAX) return false
  return true
}

// 自我修复触发器：前端把"看起来卡死的 running"行主动告诉后端，让详情接口命中补全。
// 转发日志的列表数据是后端主导，前端只是展示；detailsMap 仅缓存 attempts/error_message，
// 顶层字段（result/duration_ms/final_model）只能等 list 刷新。这里调 detail 不只是为了前端，
// 主要是给后端 SelfHeal 一个触发点：后端配置项 LOADOUT_ROUTE_LOG_SELF_HEAL_TIMEOUT>0 时，
// 会把卡死记录就地补 finished_at/duration/result。用户不必展开行也能享受修复。
// 60 秒阈值与后端默认值对齐；通过 SET-SCHEDULE 去重避免短时间内反复打 detail。
// ⚠️ 联动：SELF_HEAL_AGE_MS 必须与后端 config.RouteLogSelfHealTimeout（默认 60s）保持一致，
// 改一处记得同步另一处（前端在此先触发 repair=1，后端再按自己的阈值做最终判定）。
const SELF_HEAL_AGE_MS = 60_000
const SELF_HEAL_DEDUPE_MS = 60_000
const selfHealScheduled = new Set<string>()

function shouldSelfHeal(log: RouteLog): boolean {
  if (log.result !== 'running') return false
  const startMs = new Date(log.started_at).getTime()
  if (!Number.isFinite(startMs)) return false
  return Date.now() - startMs > SELF_HEAL_AGE_MS
}

async function selfHealStuckLogs() {
  const list = logs.value || []
  for (const log of list) {
    if (!shouldSelfHeal(log)) continue
    if (selfHealScheduled.has(log.request_id)) continue
    selfHealScheduled.add(log.request_id)
    service
      .detail(log.request_id, { repair: true }) // 带 repair=1：后端先按登记表+时间兜底自愈，再返回详情
      .then((detail) => {
        // 把补完后的顶层字段同步进 detailsMap，displayLogs 渲染时合并。
        // 顶层 result/duration_ms 要等下一次 list 刷新覆盖（这里是 attempts/error_message 视角）。
        detailsMap.set(log.request_id, detail)
      })
      .catch(() => {
        // 静默：失败不阻塞；下个 tick 仍可能命中
      })
      .finally(() => {
        // 防抖：60s 内不再对同一 request_id 重复触发；旧的卡死记录可能在多次自愈前一直处于 running。
        setTimeout(() => selfHealScheduled.delete(log.request_id), SELF_HEAL_DEDUPE_MS)
      })
  }
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
      if (log.result !== 'running' && !detail.attempts?.length) {
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
  void run('refresh', async () => {
    await refresh()
    await refreshActiveDetails()
  })
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
      await selfHealStuckLogs()
    })()
  }, AUTO_REFRESH_INTERVAL)
}
onMounted(startAutoRefresh)
// 组件卸载与 HMR reload 都会触发 scope dispose，比 onUnmounted 覆盖更全
onScopeDispose(stopAutoRefresh)

async function apply(next: RouteLogFilters) {
  await run('apply', async () => {
    filters.value = next
    page.value = 1 // 过滤条件变化回到第一页，避免高页码下先拉到空页
    await refresh()
    await refreshActiveDetails()
  })
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
  await run(
    'clear',
    async () => {
      await service.clear()
      detailsMap.clear()
      expandedIds.clear()
      page.value = 1 // 清空后回到第一页
      await refresh()
    },
    '转发日志已清理',
  )
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
          :disabled="loading || refreshing || isPending('refresh')"
          @click="manualRefresh"
          ><RiLoader4Line
            v-if="isPending('refresh')"
            class="animate-spin"
            size="16"
          /><RiRefreshLine v-else :class="{ 'animate-spin': refreshing }" size="16" />刷新 </Button
        ><Button variant="destructive" :disabled="isPending('clear')" @click="clear">
          <RiLoader4Line v-if="isPending('clear')" class="animate-spin" size="16" /><RiDeleteBinLine
            v-else
            size="16"
          />清空日志
        </Button></template
      >
    </PageHeader>
    <RouteLogFiltersForm
      :channels="channels || []"
      :is-pending="isPending"
      @apply="apply"
      @reset="apply({})"
    />
    <LoadingBlock v-if="loading" />
    <RouteLogTable
      v-else
      :logs="displayLogs"
      :channels="channels || []"
      :loading-detail="loadingDetail"
      :total="total"
      @expand="expand"
      @page-change="onPageChange"
    />
  </div>
</template>
