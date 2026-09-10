<script setup lang="ts">
import { computed, onMounted, onScopeDispose, reactive, ref, watch } from 'vue'
import { RiDatabase2Line, RiDeleteBinLine, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
import { useRouteLogs, routeLogValidity } from '@/composables/useRouteLogs'
import type { RouteLogFilters } from '@/composables/useRouteLogs'
import { useRequestLogs } from '@/composables/useRequestLogs'
import { useChannels } from '@/composables/useChannels'
import { groupChannelNames } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import { readAutoRefreshEnabled, writeAutoRefreshEnabled } from '@/lib/autoRefresh'
import { formatBytes, formatDate } from '@/lib/format'
import type { RouteLog, RequestLogStats } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import RouteLogFiltersForm from '@/components/route-logs/RouteLogFilters.vue'
import RouteLogTable from '@/components/route-logs/RouteLogTable.vue'
import RouteLogAutoRefreshSwitch from '@/components/route-logs/RouteLogAutoRefreshSwitch.vue'

const service = useRouteLogs()
const requestLogService = useRequestLogs()
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
/** 完整日志存活性：表格据此隐藏已被保留策略/清空删掉的「进入日志」入口。
 *  undefined = 后端未提供可信集合，表格退化为只判空。 */
const validRequestLogIds = computed(() => routeLogValidity(logsData.value))
// 翻页/改每页条数：更新分页状态后重新拉取当前页。
// 定时刷新不是一直跑：只有处在第 1 页才开（见下方 AUTO_REFRESH_INTERVAL 段），
// 所以这里额外挂一次「回落到第 1 页立刻刷一次」（reloadOnFirstPage）。
// 分页状态受控传入 RouteLogTable（:page/:page-size），保证过滤/清空重置 page=1 时表格同步。
function onPageChange(nextPage: number) {
  page.value = nextPage
  void refresh()
}
async function refreshPage() {
  await refresh()
  await refreshActiveDetails()
}
function onPageSizeChange(nextSize: number) {
  pageSize.value = nextSize
  page.value = 1 // 切每页条数回到第一页（DataPagination 内部也会 emit update:page=1，这里双保险）
  void refresh()
}
const { data: channels } = useListLoader(channelService.list)
// 渠道筛选下拉：按渠道名（channel_name）去重归组，展示「渠道名」而非单个 Key。
// 与表格展示（ModelChannelRef 按 base_url 分组）维度一致：一个渠道名 = 一组 Key。
const channelOptions = computed(() => groupChannelNames(channels.value || []))
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

// 手动刷新：先拉列表，再后台同步展开中的进行中详情，顺带把日志库大小也重取。
function manualRefresh() {
  void run('refresh', async () => {
    await refreshPage()
    refreshLogStats()
  })
}

// ===== 自动刷新（只在第 1 页跑）=====
// 规则：定时刷新只在「第 1 页 + 开关开启」时存在。
//   - 第 1 页是最新请求的落点：盯在这里才有「数字自己在跳」的价值；
//   - 翻到第 2 页及以后，列表本就是历史快照，每 3 秒重取一次既没用、又会把用户
//     正在看的行整表换掉（展开态靠 detailsMap 兜着，但列表对象每轮都是新的）；
//   - 因此翻页离开第 1 页即停表，回到第 1 页再立刻刷一次并重新开始计时。
// 开关持久化在 localStorage，就在本页筛选行里改（见 readAutoRefreshEnabled）。
// 只放本页、不上设置页：这是「这块列表怎么动」的显示偏好，跟日志保留策略无关。
const AUTO_REFRESH_INTERVAL = 3_000
const autoRefreshEnabled = ref(readAutoRefreshEnabled())
let autoTimer: ReturnType<typeof setInterval> | undefined

/** 当前是否应该跑定时刷新：第 1 页 + 开关开启。 */
function autoRefreshActive() {
  return autoRefreshEnabled.value && page.value === 1
}

function stopAutoRefresh() {
  if (autoTimer) {
    clearInterval(autoTimer)
    autoTimer = undefined
  }
}

/** 开表；已在跑则先停掉再重建，避免 HMR 重跑 setup 时定时器叠加。 */
function startAutoRefresh() {
  stopAutoRefresh()
  if (!autoRefreshActive()) return
  autoTimer = setInterval(() => {
    void (async () => {
      await refresh({ silentError: true })
      await refreshActiveDetails()
      await selfHealStuckLogs()
    })()
  }, AUTO_REFRESH_INTERVAL)
}

/** 按当前页码/开关同步定时器状态：该跑就开、不该跑就停。 */
function syncAutoRefresh() {
  if (autoRefreshActive()) {
    if (!autoTimer) startAutoRefresh()
    return
  }
  stopAutoRefresh()
}

function setAutoRefreshEnabled(enabled: boolean) {
  autoRefreshEnabled.value = enabled
  writeAutoRefreshEnabled(enabled)
  syncAutoRefresh()
}

// 页码变化（flush: 'post'，等页码更新渲染完再回调）：
//   1. 从后面某页回落到第 1 页 → 立刻刷一次，否则要干等下一次 tick；
//      同一页内的刷新页码没变，不重复触发。
//   2. 同步定时器：在第 1 页开、离开第 1 页停。
watch(
  page,
  (next, prev) => {
    if (next === 1 && prev !== 1) {
      void refreshPage()
      refreshLogStats()
    }
    syncAutoRefresh()
  },
  { flush: 'post' },
)

onMounted(() => {
  startAutoRefresh()
  // 首次进入立刻取一次大小（别等 3 秒后第一个 tick，按钮会先显示占位符再跳数字）
  void refreshLogStats()
})
// 组件卸载与 HMR reload 都会触发 scope dispose，比 onUnmounted 覆盖更全
onScopeDispose(stopAutoRefresh)

async function apply(next: RouteLogFilters) {
  await run('apply', async () => {
    // 渠道名存在性守卫：渠道被删除/改名后，form 里残留的旧 channel_name 在下拉中
    // 已无对应项，若不清理会出现「下拉显示所有渠道、实际仍被旧筛选过滤」的脏选中态。
    if (
      next.channel_name &&
      !channelOptions.value.some((option) => option.name === next.channel_name)
    ) {
      next = { ...next, channel_name: undefined }
    }
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

// ===== 完整请求日志库（request-log.db）=====
// 与上面「转发日志」（loadout.db 的 route_requests）是两个库：前者是路由决策记录，
// 后者是逐次渠道尝试的完整请求/响应正文（体积大得多）。

/** 完整请求日志（request-log.db）占用统计；null = 尚未取到。 */
const logStats = ref<RequestLogStats | null>(null)

/** 拉取完整请求日志占用统计（体积 + 条数 + 保留策略）。 */
async function refreshLogStats() {
  try {
    logStats.value = await requestLogService.stats()
  } catch {
    // 静默：统计失败不该打扰用户，按钮回落为占位符即可。
    // 定时刷新已在 silentError 模式，这里再弹一次错误会和列表错误重复。
  }
}

/**
 * 点「日志大小」按钮：确认后清空完整请求日志库（request-log.db）。
 *
 * 为什么按钮既显示大小又能点：它是这个库唯一的视图入口，用户看到「690 MB」的
 * 第一反应就是「这能清掉吗」。同尺寸的操作入口在设置页，但用户不会为了清日志
 * 专门跑一趟设置。
 *
 * 必须确认：库可能很大且删了不可恢复。确认框里把条数、占用、最早一条都摆出来，
 * 让用户明确知道要删掉多少。
 */
const clearLogDescription = computed(() => {
  const stats = logStats.value
  if (!stats) return '将删除全部完整请求日志，此操作不可恢复。'
  const parts = [`共 ${stats.count} 条、占用 ${formatBytes(stats.size)}`]
  if (stats.oldest_started_at) parts.push(`最早一条为 ${formatDate(stats.oldest_started_at)}`)
  return `${parts.join('，')}。删除后不可恢复。`
})

async function clearRequestLogs() {
  const confirmed = await confirmDialog({
    title: '清空完整请求日志？',
    description: clearLogDescription.value,
    confirmText: '清空',
    destructive: true,
  })
  if (!confirmed) return
  await run(
    'clear-request-logs',
    async () => {
      const result = await requestLogService.clear()
      // 关联列已被后端一并清空，列表刷新后「进入日志」入口自动消失。
      await Promise.all([refreshPage(), refreshLogStats()])
      return result
    },
    '完整请求日志已清空',
  )
}

/**
 * 日志大小按钮文案：显示完整请求日志库（request-log.db）的占用。
 *
 * 取数中 / 取数失败时显示「…」而不是「请求日志」这类文字：这个入口的职责就是显示
 * 一个数字，文字占位会让用户以为它只是个普通按钮。
 */
const logSizeLabel = computed(() => {
  const stats = logStats.value
  if (!stats) return '…'
  return formatBytes(stats.size)
})

/** 悬浮提示：把条数、时间范围、当前策略一起说清楚，按钮本身只放得下大小。 */
const logSizeTitle = computed(() => {
  const stats = logStats.value
  if (!stats) return '正在读取完整请求日志占用…'
  const lines = [`完整请求日志：${formatBytes(stats.size)}，共 ${stats.count} 条`]
  if (stats.oldest_started_at) {
    lines.push(`最早一条：${formatDate(stats.oldest_started_at)}`)
  }
  const limits: string[] = []
  if (stats.max_age_days > 0) limits.push(`只留最近 ${stats.max_age_days} 天`)
  if (stats.max_size_mb > 0) limits.push(`最多 ${stats.max_size_mb} MB`)
  lines.push(
    limits.length ? `保留策略：${limits.join('、')}` : '保留策略：未设置（日志会一直增长）',
  )
  lines.push('点击可清空该日志库；保留策略在「设置 → 日志保留」')
  return lines.join('\n')
})
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
          />清空日志 </Button
        ><Button
          variant="outline"
          class="font-mono tabular-nums"
          :disabled="isPending('clear-request-logs')"
          :title="logSizeTitle"
          @click="clearRequestLogs"
        >
          <RiLoader4Line
            v-if="isPending('clear-request-logs')"
            class="animate-spin"
            size="16"
          /><RiDatabase2Line v-else size="16" />{{ logSizeLabel }}
        </Button></template
      >
    </PageHeader>
    <RouteLogFiltersForm
      :channels="channelOptions"
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
      :valid-request-log-ids="validRequestLogIds"
      :total="total"
      :page="page"
      :page-size="pageSize"
      @update:page="onPageChange"
      @update:page-size="onPageSizeChange"
      @expand="expand"
    >
      <template #header-actions>
        <RouteLogAutoRefreshSwitch
          :enabled="autoRefreshActive()"
          :paused="autoRefreshEnabled && !autoRefreshActive()"
          @update:enabled="setAutoRefreshEnabled"
        />
      </template>
    </RouteLogTable>
  </div>
</template>
