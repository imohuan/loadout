<script setup lang="ts">
// WorkBuddy 积分与用量监控面板（概览页最底部一块）。
// 数据：积分余额来自后端缓存（手动刷新才拉上游），消耗来自本地 wb_panel_usage 聚合。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useDark } from '@vueuse/core'
import { toast } from 'vue-sonner'
import {
  RiArrowDownSLine,
  RiImageLine,
  RiLightbulbLine,
  RiRefreshLine,
  RiSearchLine,
  RiSunLine,
  RiToolsLine,
} from '@remixicon/vue'
import HoverTextCard from '@/components/ui/HoverTextCard.vue'
import AxTable from '@/components/ui/AxTable.vue'
import DataPagination from '@/components/DataPagination.vue'
import type { WbModel, WbUsageModel } from '@/lib/workbuddy'
import VChart from 'vue-echarts'
import { use } from 'echarts/core'
import { BarChart, LineChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import {
  getWbSettings,
  getWbPanelData,
  refreshWbPanel,
  saveWbSettings,
	saveWbAutoRefresh,
  wbAccountBrief,
  wbAccountStatus,
  WB_TONE_DOT,
  WB_TONE_SOFT,
  type WbAccount,
  type WbPanelData,
  type WbUsageDay,
  type WbUsageLog,
} from '@/lib/workbuddy'

use([CanvasRenderer, BarChart, LineChart, GridComponent, TooltipComponent, LegendComponent])

// ---------- 状态 ----------
const data = ref<WbPanelData | null>(null)
const loading = ref(true)
const refreshing = ref(false)
const scope = ref<'all' | 'one'>('all')
const activeId = ref('')
const search = ref('')
const range = ref<'today' | '7d' | '30d' | 'all'>('30d')
const composeTab = ref<'source' | 'expiry'>('source')
const showAllRank = ref(false)

/** 消耗明细：跟随当前视角（全部 = 所有账号；指定 = 只看该账号）。 */
const usageLogs = computed<WbUsageLog[]>(() => {
  const all = data.value?.usage_logs || []
  if (scope.value === 'one') return all.filter((l) => l.account_id === activeId.value)
  return all
})

// 日志消耗面板分页（切页/切每页条数；账号或数据变化时回到第一页由 DataPagination 兜底）。
const logPage = ref(1)
const logPageSize = ref(50)
const pagedLogs = computed(() => {
  const start = (logPage.value - 1) * logPageSize.value
  return usageLogs.value.slice(start, start + logPageSize.value)
})

/** 千分位/缩写 tokens，与转发日志 RouteLogTable 同款。 */
function fmtTokens(value: number) {
  const n = value ?? 0
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 10_000) return `${(n / 1000).toFixed(1)}k`
  if (n >= 1000) return n.toLocaleString('en-US')
  return String(n)
}

/** 缓存占比：cached / prompt 百分比；无数据返回空。 */
function logCacheRatio(log: WbUsageLog) {
  const prompt = log.prompt_tokens ?? 0
  const cached = log.cached_tokens ?? 0
  if (prompt <= 0 || cached <= 0) return ''
  return `${Math.round((cached / prompt) * 100)}%`
}
// 刷新并发数：并发太多可能触发上游风控（封号），默认 3。
const concurrency = ref(3)
// 自动刷新：定时重算本地数据（不打上游），开关与间隔（秒）。
const autoRefresh = ref(true)
const autoInterval = ref(30)
let autoTimer: ReturnType<typeof setInterval> | null = null

// 跟随 useDark 的响应式状态：之前只在初始化时读一次 <html> 的 class，
// 页面上切换主题后图表配色不会重算，暗色下仍是浅色网格线。
const dark = useDark()

// ---------- 账号排序与过滤 ----------
const STATUS_ORDER: Record<string, number> = { ok: 0, warn: 1, bad: 2, off: 3 }
const allAccounts = computed<WbAccount[]>(() => data.value?.accounts ?? [])
const sortedAccounts = computed(() =>
  [...allAccounts.value].sort((a, b) => {
    const d = STATUS_ORDER[wbAccountStatus(a)] - STATUS_ORDER[wbAccountStatus(b)]
    if (d !== 0) return d
    return b.remain - a.remain
  }),
)
const filteredAccounts = computed(() => {
  const kw = search.value.trim().toLowerCase()
  if (!kw) return sortedAccounts.value
  return sortedAccounts.value.filter((a) => {
    const st = wbAccountBrief(a).label
    return (
      a.label.toLowerCase().includes(kw) ||
      a.channel_id.toLowerCase().includes(kw) ||
      st.toLowerCase().includes(kw)
    )
  })
})
const anomalyCount = computed(
  () => allAccounts.value.filter((a) => wbAccountStatus(a) !== 'ok').length,
)
const summaryText = computed(() => {
  const n = allAccounts.value.length
  const parts = [`${n} 个`]
  if (anomalyCount.value > 0) parts.push(`${anomalyCount.value} 异常`)
  return parts.join(' · ')
})

/**
 * 当前视角账号集合：全部 or 单账号。
 * 「已禁用 / 手动关闭」的账号从所有统计中剔除（剩余积分 / 消耗 / 构成 / 排行 / 趋势 / 明细），
 * 相当于不存在；列表里仍然显示，便于看到它们的状态。
 */
const viewAccounts = computed(() => {
  const usable = (a: WbAccount) => wbAccountStatus(a) !== 'bad' && wbAccountStatus(a) !== 'off'
  if (scope.value === 'one')
    return allAccounts.value.filter((a) => a.channel_id === activeId.value && usable(a))
  return allAccounts.value.filter(usable)
})

function pickAccount(id: string) {
  activeId.value = id
  scope.value = 'one'
  logPage.value = 1
}

// ---------- 指标 ----------
const fmt = (n: number) =>
  n >= 10000 ? `${(n / 1000).toFixed(1)}K` : n.toFixed(2).replace(/\.?0+$/, '') || '0'

const metrics = computed(() => {
  const accs = viewAccounts.value
  const remain = accs.reduce((s, a) => s + a.remain, 0)
  const today = accs.reduce((s, a) => s + a.spent_today, 0)
  const seven = accs.reduce((s, a) => s + a.spent_7d, 0)
  const month = accs.reduce((s, a) => s + a.spent_month, 0)
  const total = accs.reduce((s, a) => s + a.spent_total, 0)
  const reqs = accs.reduce((s, a) => s + a.requests, 0)
  return { remain, today, seven, month, total, reqs }
})

// ---------- 剩余积分构成 ----------
const composeGroups = computed(() => {
  const accs = viewAccounts.value
  const map = new Map<string, { name: string; remain: number; size: number }>()
  for (const a of accs)
    for (const g of a.groups || []) {
      const cur = map.get(g.name) || { name: g.name, remain: 0, size: 0 }
      cur.remain += g.remain
      cur.size += g.size
      map.set(g.name, cur)
    }
  return [...map.values()].sort((a, b) => b.remain - a.remain)
})
const composeTotal = computed(() => composeGroups.value.reduce((s, g) => s + g.remain, 0))

const composeExpiry = computed(() => {
  const accs = viewAccounts.value
  const map = new Map<string, number>()
  for (const a of accs)
    for (const g of a.groups || [])
      for (const e of g.expiry || []) {
        const month = (e.end || '').slice(0, 7)
        map.set(month, (map.get(month) || 0) + e.remain)
      }
  return [...map.entries()]
    .map(([month, remain]) => ({ month, remain }))
    .sort((a, b) => a.month.localeCompare(b.month))
})
// 本地月（toISOString 是 UTC 月，月末月初会与上游本地时间差一天）。
const nowMonth = new Date()
  .toLocaleDateString("sv-SE", { year: "numeric", month: "2-digit" })
  .slice(0, 7)

// ---------- 排行（跟随区间切换：今日 / 近7天 / 近30天 / 总计） ----------
const rankRows = computed(() => {
  if (scope.value !== 'all') return []
  const spentOf = (a: WbAccount) => {
    if (range.value === 'today') return a.spent_today
    if (range.value === '7d') return a.spent_7d
    if (range.value === '30d') return a.spent_month
    return a.spent_total
  }
  const rows = [...viewAccounts.value].sort((a, b) => spentOf(b) - spentOf(a))
  const totalSpent = rows.reduce((s, a) => s + spentOf(a), 0) || 1
  return rows.map((a) => ({
    a,
    spent: spentOf(a),
    pct: (spentOf(a) / totalSpent) * 100,
    barPct: (spentOf(a) / (spentOf(rows[0]) || 1)) * 100,
  }))
})
const visibleRank = computed(() =>
  showAllRank.value ? rankRows.value : rankRows.value.slice(0, 8),
)

// ---------- 趋势图（堆叠柱 + 折线双轴） ----------
const palette = [
  '#6366f1',
  '#f59e0b',
  '#10b981',
  '#ef4444',
  '#8b5cf6',
  '#06b6d4',
  '#f43f5e',
  '#84cc16',
]

const trendDays = computed<WbUsageDay[]>(() => {
  const accs = viewAccounts.value
  const map = new Map<string, WbUsageDay>()
  for (const a of accs) {
    for (const d of data.value?.usage_daily[a.channel_id] || []) {
      const cur = map.get(d.date) || { date: d.date, requests: 0, credit: 0, models: {} }
      cur.requests += d.requests
      cur.credit += d.credit
      for (const [m, v] of Object.entries(d.models)) cur.models[m] = (cur.models[m] || 0) + v
      map.set(d.date, cur)
    }
  }
  let days = [...map.values()].sort((a, b) => a.date.localeCompare(b.date))
  if (range.value === 'today') days = days.slice(-1)
  else if (range.value === '7d') days = days.slice(-7)
  else if (range.value === '30d') days = days.slice(-30)
  return days
})

const trendModelNames = computed(() => {
  const set = new Set<string>()
  for (const d of trendDays.value) for (const m of Object.keys(d.models)) set.add(m)
  return [...set].slice(0, 10)
})

const trendOption = computed(() => {
  const axis = trendDays.value.map((d) => d.date.slice(5))
  const series: Array<Record<string, unknown>> = trendModelNames.value.map((m, i) => ({
    name: m,
    type: 'bar' as const,
    stack: 'credit',
    barMaxWidth: 22,
    data: trendDays.value.map((d) => Number((d.models[m] || 0).toFixed(2))),
    itemStyle: { color: palette[i % palette.length] },
  }))
  series.push({
    name: '调用次数',
    type: 'line' as const,
    yAxisIndex: 1,
    smooth: true,
    symbol: 'circle',
    symbolSize: 5,
    lineStyle: { width: 2, color: '#3b82f6' },
    itemStyle: { color: '#3b82f6' },
    data: trendDays.value.map((d) => d.requests),
  })
  const txt = dark.value ? '#a1a1aa' : '#71717a'
  const split = dark.value ? '#27272a' : '#e4e4e7'
  return {
    grid: { left: 44, right: 44, top: 34, bottom: 26 },
    legend: {
      top: 0,
      textStyle: { color: txt, fontSize: 11 },
      data: [...trendModelNames.value, '调用次数'],
    },
    tooltip: {
      trigger: 'axis' as const,
      backgroundColor: dark.value ? '#27272a' : '#ffffff',
      borderColor: dark.value ? '#3f3f46' : '#e4e4e7',
      borderWidth: 1,
      textStyle: { color: dark.value ? '#e4e4e7' : '#3f3f46', fontSize: 12 },
    },
    xAxis: {
      type: 'category' as const,
      data: axis,
      axisLabel: { color: txt, fontSize: 11 },
      axisLine: { lineStyle: { color: split } },
    },
    yAxis: [
      {
        type: 'value' as const,
        name: '积分',
        axisLabel: { color: txt, fontSize: 11 },
        splitLine: { lineStyle: { color: split } },
        nameTextStyle: { color: txt },
      },
      {
        type: 'value' as const,
        name: '调用',
        axisLabel: { color: txt, fontSize: 11 },
        splitLine: { show: false },
        nameTextStyle: { color: txt },
      },
    ],
    series,
  }
})

// ---------- 模型明细 ----------
const modelRows = computed(() => {
  const accs = viewAccounts.value
  if (scope.value === 'one') {
    const a = accs[0]
    if (!a) return []
    return (data.value?.usage_by_model || [])
      .filter((m: WbUsageModel) =>
        (data.value?.usage_daily[a.channel_id] || []).some((d) => d.models[m.model]),
      )
      .map((m: WbUsageModel) => ({ ...m }))
  }
  return [...(data.value?.usage_by_model || [])]
})
const modelRate = (id: string) => {
  for (const a of allAccounts.value || []) {
    const m = (a.models || []).find((x) => x.id === id)
    if (m) return m
  }
  return undefined
}
const rateClass = (rate: number) =>
  rate <= 0
    ? 'text-emerald-600 dark:text-emerald-400'
    : rate < 0.2
      ? 'text-cyan-600 dark:text-cyan-400'
      : rate < 0.8
        ? 'text-orange-600 dark:text-orange-400'
        : 'text-red-600 dark:text-red-400'

async function copyModel(id: string) {
  try {
    await navigator.clipboard.writeText(id)
    toast.success(`已复制 ${id}`)
  } catch {
    toast.error('复制失败')
  }
}

// ---------- 数据加载 ----------
async function load() {
  try {
    data.value = await getWbPanelData()
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载面板失败')
  } finally {
    loading.value = false
  }
}

async function refresh() {
  refreshing.value = true
  try {
    const r = await refreshWbPanel()
    if (r.failed?.length) toast.warning(`同步完成，${r.failed.length} 个失败：${r.failed[0]}`)
    else toast.success(`已同步 ${r.synced} 个账号`)
    await load()
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshing.value = false
  }
}

onMounted(load)

onBeforeUnmount(() => {
  if (autoTimer) {
    clearInterval(autoTimer)
    autoTimer = null
  }
})

onMounted(async () => {
  try {
    const s = await getWbSettings()
    concurrency.value = s.concurrency || 3
    autoRefresh.value = s.auto_refresh !== 0
  } catch {
    /* 用默认值 */
  }
  setupAutoTimer()
})

/** 按当前开关/间隔重建定时器：只重算本地数据（load 不打上游）。 */
function setupAutoTimer() {
  if (autoTimer) {
    clearInterval(autoTimer)
    autoTimer = null
  }
  if (!autoRefresh.value) return
  autoTimer = setInterval(() => {
    if (refreshing.value || loading.value) return
		localRefreshing.value = true
		load().finally(() => {
			localRefreshing.value = false
		})
  }, autoInterval.value * 1000)
}

/** 本地刷新状态：点「本地刷新」或定时器触发时短暂转圈。 */
const localRefreshing = ref(false)

/** 立即重算一次本地数据（不打上游）。 */
async function manualLocalRefresh() {
  if (localRefreshing.value) return
  localRefreshing.value = true
  try {
    await load()
  } finally {
    localRefreshing.value = false
  }
}

/** 修改间隔（0 = 不刷新）并持久化开关状态。 */
async function onAutoIntervalChange(sec: string | number) {
  const n = Number(sec)
  if (n === (autoRefresh.value ? autoInterval.value : 0)) return
  if (n === 0) {
    autoRefresh.value = false
    setupAutoTimer()
    try {
      await saveWbAutoRefresh(false)
      toast.success('自动刷新已关闭')
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存失败')
    }
    return
  }
  autoInterval.value = n
  autoRefresh.value = true
  setupAutoTimer()
  try {
    await saveWbAutoRefresh(true)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
  }
}

/** 保存并发设置（Select 的 value 是字符串，转回数字）。 */
async function onConcurrencyChange(v: string | number) {
  const n = Number(v)
  if (!n || n === concurrency.value) return
  try {
    await saveWbSettings(n)
    concurrency.value = n
    toast.success(`并发数已设为 ${n}`)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
  }
}

/** 模型行 hover 详情：能力 + 上下文 + 输出上限 + 该 Key 的真实健康状态。 */
function modelHoverText(m: WbModel): string {
  const a = allAccounts.value[0]
  const st = a?.model_states?.[m.id]
  const lines: string[] = []
  lines.push(`显示名：${m.name}`)
  lines.push(`模型 ID：${m.id}`)
  if (m.description) lines.push(`描述：${m.description}`)
  lines.push(`倍率：${m.credits || '未知'}`)
  if (m.context) lines.push(`上下文：${(m.context / 1000).toFixed(0)}K`)
  if (m.max_output) lines.push(`最大输出：${(m.max_output / 1000).toFixed(0)}K`)
  const caps: string[] = []
  if (m.supports_images) caps.push('图片')
  if (m.supports_tool) caps.push('工具')
  if (m.supports_reasoning) caps.push(m.only_reasoning ? '仅推理' : '推理')
  lines.push(`能力：${caps.join(' / ') || '纯文本'}`)
  if (m.reasoning_effort) lines.push(`推理档位：${m.reasoning_effort}`)
  if (st) {
    lines.push('')
    lines.push(`健康状态：${st.label}`)
    lines.push(`自动状态：${st.status}`)
    if (st.failure_class) lines.push(`失败分类：${st.failure_class}`)
    if (st.fail_count > 0) lines.push(`连续失败：${st.fail_count} 次`)
    if (st.last_error) lines.push(`最近错误：${st.last_error.slice(0, 200)}`)
    if (st.disabled_until) lines.push(`恢复时间：${st.disabled_until}`)
  } else {
    lines.push('')
    lines.push('健康状态：可用（从未失败）')
  }
  return lines.join('\n')
}
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base"
        ><RiSunLine size="18" />WorkBuddy 面板监控</CardTitle
      >
      <CardDescription
        >积分余额来自手动同步的缓存；消耗从每个请求的 usage.credit
        本地累计，不调上游。</CardDescription
      >
    </CardHeader>
    <CardContent class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="text-xs text-muted-foreground">共 {{ allAccounts.length }} 个账号缓存</div>
        <div class="flex items-center gap-2">
          <!-- 刷新积分：按钮组（左：手动刷新；右：并发数下拉） -->
          <div class="inline-flex h-7 items-stretch overflow-hidden rounded-md border border-border">
            <button
              type="button"
              class="flex h-full items-center gap-1.5 px-2.5 text-[0.8rem] font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="refreshing"
              @click="refresh"
            >
              <RiRefreshLine :class="{ 'animate-spin': refreshing }" size="14" />
              <span>刷新积分</span>
            </button>
            <span class="w-px shrink-0 bg-border" aria-hidden="true" />
            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <button
                  type="button"
                  class="flex h-full items-center gap-1 px-2 text-[0.8rem] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
                  aria-label="选择刷新并发数"
                  :disabled="refreshing"
                  title="刷新并发数：当前 {{ concurrency }}"
                >
                  <span class="tabular-nums text-[11px]">{{ concurrency }}</span>
                  <RiArrowDownSLine size="14" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" :side-offset="4" class="min-w-44">
                <DropdownMenuLabel class="text-xs font-normal text-muted-foreground">
                  刷新并发数（当前 {{ concurrency }}）
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuRadioGroup
                  :model-value="concurrency"
                  @update:model-value="onConcurrencyChange"
                >
                  <DropdownMenuRadioItem
                    v-for="n in [1, 2, 3, 5, 8, 10]"
                    :key="n"
                    :value="n"
                  >
                    {{ n }} 个并发
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
			<!-- 本地刷新：按钮组（左：立即重算一次；右：间隔下拉，0 = 不刷新） -->
          <div class="inline-flex h-7 items-stretch overflow-hidden rounded-md border border-border">
            <button
              type="button"
					class="flex h-full items-center gap-1.5 px-2.5 text-[0.8rem] font-medium text-foreground transition-colors hover:bg-muted"
					title="重新计算本地数据（不打上游）"
					@click="manualLocalRefresh"
            >
					<RiRefreshLine
						:class="{ 'animate-spin': localRefreshing }"
						size="14"
					/>
					<span>本地刷新</span>
            </button>
            <span class="w-px shrink-0 bg-border" aria-hidden="true" />
            <DropdownMenu>
              <DropdownMenuTrigger as-child>
                <button
                  type="button"
                  class="flex h-full items-center gap-1 px-2 text-[0.8rem] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
                  aria-label="选择自动刷新间隔"
                  title="自动刷新间隔：当前 {{ autoInterval }} 秒"
                >
						<span class="tabular-nums text-[11px]">{{
							autoRefresh ? (autoInterval >= 60 ? autoInterval / 60 + 'm' : autoInterval + 's') : '关'
						}}</span>
                  <RiArrowDownSLine size="14" />
                </button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" :side-offset="4" class="min-w-44">
                <DropdownMenuLabel class="text-xs font-normal text-muted-foreground">
						自动刷新间隔（本地数据重算，不打上游）
                </DropdownMenuLabel>
                <DropdownMenuSeparator />
                <DropdownMenuRadioGroup
						:model-value="autoRefresh ? autoInterval : 0"
                  @update:model-value="onAutoIntervalChange"
                >
						<DropdownMenuRadioItem :value="0">不刷新</DropdownMenuRadioItem>
						<DropdownMenuRadioItem
							v-for="sec in [10, 30, 60, 120, 300]"
							:key="sec"
							:value="sec"
						>
							{{ sec >= 60 ? sec / 60 + ' 分钟' : sec + ' 秒' }}
                  </DropdownMenuRadioItem>
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
      </div>
      <div v-if="loading" class="py-10 text-center text-sm text-muted-foreground">加载中…</div>
      <div v-else-if="!allAccounts.length" class="py-10 text-center text-sm text-muted-foreground">
        暂无 WorkBuddy 账号缓存，点右上「刷新积分」同步一次。
      </div>
      <div v-else class="grid gap-4 lg:grid-cols-[268px_1fr] max-lg:grid-cols-1">
        <!-- 左侧栏 -->
        <aside class="flex flex-col gap-3 rounded-md border p-2.5">
          <div class="flex items-center gap-2">
            <span
              class="grid size-7 shrink-0 place-items-center rounded-md bg-gradient-to-br from-violet-500 to-indigo-500 text-xs font-bold text-white"
              >W</span
            >
            <div class="min-w-0">
              <p class="truncate text-sm font-semibold">WorkBuddy</p>
              <p class="text-[11px] text-muted-foreground">积分与用量面板</p>
            </div>
          </div>
          <!-- 范围切换 -->
          <div class="inline-flex rounded-md bg-muted p-0.5 text-xs">
            <button
              type="button"
              class="flex-1 rounded px-2 py-1 font-medium transition-colors"
              :class="scope === 'all' ? 'bg-background shadow' : 'text-muted-foreground'"
              @click="scope = 'all'"
            >
              全部账号
            </button>
            <button
              type="button"
              class="flex-1 rounded px-2 py-1 font-medium transition-colors disabled:cursor-not-allowed"
              :class="scope === 'one' ? 'bg-background shadow' : 'text-muted-foreground'"
              :disabled="!activeId"
              @click="scope = 'one'"
            >
              指定账号
            </button>
          </div>
          <!-- 账号列表 -->
          <div>
            <div
              class="mb-1.5 flex items-center justify-between text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
            >
              <span>账号</span><span class="font-normal normal-case">{{ summaryText }}</span>
            </div>
            <div class="relative mb-1.5">
              <RiSearchLine
                size="13"
                class="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-muted-foreground"
              />
              <Input v-model="search" placeholder="搜索账号 / 状态…" class="h-7 pl-7 text-xs" />
            </div>
            <div class="max-h-[420px] space-y-0.5 overflow-y-auto">
              <button
                v-for="a in filteredAccounts"
                :key="a.channel_id"
                type="button"
                class="flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left transition-colors"
                :class="
                  activeId === a.channel_id && scope === 'one' ? 'bg-muted' : 'hover:bg-muted/60'
                "
                @click="pickAccount(a.channel_id)"
              >
                <span
                  class="size-1.5 shrink-0 rounded-full"
                  :class="WB_TONE_DOT[wbAccountBrief(a).tone]"
                />
                <span class="min-w-0 flex-1">
                  <span class="block truncate text-xs font-medium">{{ a.label }}</span>
                  <span class="block truncate font-mono text-[10px] text-muted-foreground">{{
                    a.channel_id.slice(-8)
                  }}</span>
                </span>
                <span class="shrink-0 text-right">
                  <span class="block text-xs font-semibold tabular-nums">{{ fmt(a.remain) }}</span>
                  <span
                    class="mt-0.5 inline-block rounded px-1 py-0.5 text-[10px] font-medium"
                    :class="WB_TONE_SOFT[wbAccountBrief(a).tone]"
                    >{{ wbAccountBrief(a).label }}</span
                  >
                </span>
              </button>
              <p
                v-if="!filteredAccounts.length"
                class="py-3 text-center text-xs text-muted-foreground"
              >
                无匹配账号
              </p>
            </div>
          </div>
          <!-- 模型列表 -->
          <div>
            <div
              class="mb-1.5 text-[11px] font-semibold uppercase tracking-wide text-muted-foreground"
            >
              模型
            </div>
            <div class="space-y-0.5">
              <HoverTextCard
                v-for="m in allAccounts[0]?.models || []"
                :key="m.id"
                :text="modelHoverText(m)"
                label="模型详情"
                mono
              >
                <div
                  class="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1 transition-colors hover:bg-muted"
                  @click="copyModel(m.id)"
                >
                  <span
                    class="w-10 shrink-0 text-right text-[11px] font-bold tabular-nums"
                    :class="rateClass(m.rate)"
                    >{{ m.rate.toFixed(2) }}</span
                  >
                  <span class="min-w-0 flex-1 truncate text-xs">{{ m.id }}</span>
                  <span class="flex shrink-0 items-center gap-0.5">
                    <RiImageLine
                      v-if="m.supports_images"
                      size="11"
                      class="text-blue-500"
                      title="支持图片"
                    />
                    <RiToolsLine
                      v-if="m.supports_tool"
                      size="11"
                      class="text-emerald-500"
                      title="支持工具调用"
                    />
                    <RiLightbulbLine
                      v-if="m.supports_reasoning"
                      size="11"
                      class="text-violet-500"
                      :title="m.only_reasoning ? '仅推理模式' : '支持推理'"
                    />
                  </span>
                </div>
              </HoverTextCard>
            </div>
          </div>
        </aside>

        <!-- 右侧画布 -->
        <div class="min-w-0 space-y-4">
          <!-- 标题区 -->
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p class="text-sm font-semibold">
                {{
                  scope === 'all'
                    ? '用量总览'
                    : allAccounts.find((a) => a.channel_id === activeId)?.label || '账号'
                }}
                <Badge
                  v-if="scope === 'one'"
                  class="ml-1 border-0"
                  :class="
                    WB_TONE_SOFT[
                      wbAccountBrief(
                        allAccounts.find((a) => a.channel_id === activeId) || allAccounts[0],
                      ).tone
                    ]
                  "
                  >{{
                    wbAccountBrief(
                      allAccounts.find((a) => a.channel_id === activeId) || allAccounts[0],
                    ).label
                  }}</Badge
                >
              </p>
              <p class="text-xs text-muted-foreground">
                {{ viewAccounts.length }} 个账号 · 来源 WorkBuddy usage.credit
              </p>
            </div>
          </div>

          <!-- 指标卡（2x2）+ 剩余积分构成：同一 grid 左右并排；窄屏上下堆叠 -->
          <div class="grid grid-cols-1 gap-2 lg:grid-cols-2">
            <div
              class="grid auto-rows-fr grid-cols-2 gap-2 self-stretch"
            >
              <div
                class="flex items-center justify-between rounded-md border bg-muted/40 px-3 py-2.5"
              >
                <span class="text-xs text-muted-foreground">剩余积分</span>
                <span class="text-lg font-bold tabular-nums">{{ fmt(metrics.remain) }}</span>
              </div>
              <div
                class="flex items-center justify-between rounded-md border bg-muted/40 px-3 py-2.5"
              >
                <span class="text-xs text-muted-foreground">今日消耗</span>
                <span class="text-lg font-bold tabular-nums">{{ fmt(metrics.today) }}</span>
              </div>
              <div
                class="flex items-center justify-between rounded-md border bg-muted/40 px-3 py-2.5"
              >
                <span class="text-xs text-muted-foreground">近 7 天</span>
                <span class="text-lg font-bold tabular-nums">{{ fmt(metrics.seven) }}</span>
              </div>
              <div
                class="flex items-center justify-between rounded-md border bg-muted/40 px-3 py-2.5"
              >
                <span class="text-xs text-muted-foreground">本月消耗</span>
                <span class="text-lg font-bold tabular-nums">{{ fmt(metrics.month) }}</span>
              </div>
            </div>

            <!-- 剩余积分构成（右侧） -->
            <div class="rounded-md border p-3">
              <div class="mb-2 flex items-center justify-between">
                <p class="text-xs font-semibold">剩余积分构成</p>
                <div class="inline-flex rounded-md bg-muted p-0.5 text-[11px]">
                  <button
                    type="button"
                    class="rounded px-2 py-0.5"
                    :class="
                      composeTab === 'source' ? 'bg-background shadow' : 'text-muted-foreground'
                    "
                    @click="composeTab = 'source'"
                  >
                    按来源
                  </button>
                  <button
                    type="button"
                    class="rounded px-2 py-0.5"
                    :class="
                      composeTab === 'expiry' ? 'bg-background shadow' : 'text-muted-foreground'
                    "
                    @click="composeTab = 'expiry'"
                  >
                    按到期
                  </button>
                </div>
              </div>
              <template v-if="composeTab === 'source'">
                <div
                  v-if="!composeGroups.length"
                  class="py-3 text-center text-xs text-muted-foreground"
                >
                  暂无数据
                </div>
                <div v-for="g in composeGroups" :key="g.name" class="mb-1.5 last:mb-0">
                  <div class="flex items-center justify-between text-xs">
                    <span class="min-w-0 truncate">{{ g.name }}</span>
                    <span class="shrink-0 tabular-nums text-muted-foreground"
                      >{{ fmt(g.remain) }} ·
                      {{ composeTotal ? ((g.remain / composeTotal) * 100).toFixed(1) : 0 }}%</span
                    >
                  </div>
                  <div class="mt-1 h-1.5 overflow-hidden rounded-full bg-muted">
                    <div
                      class="h-full rounded-full bg-violet-500"
                      :style="{ width: (composeTotal ? (g.remain / composeTotal) * 100 : 0) + '%' }"
                    />
                  </div>
                </div>
              </template>
              <template v-else>
                <div
                  v-if="!composeExpiry.length"
                  class="py-3 text-center text-xs text-muted-foreground"
                >
                  暂无数据
                </div>
                <div
                  v-for="e in composeExpiry"
                  :key="e.month"
                  class="flex items-center justify-between text-xs"
                >
                  <span>
                    {{ e.month }}
                    <Badge
                      v-if="e.month === nowMonth"
                      variant="destructive"
                      class="ml-1 text-[10px]"
                      >即将过期</Badge
                    >
                  </span>
                  <span class="tabular-nums text-muted-foreground">{{ fmt(e.remain) }}</span>
                </div>
              </template>
            </div>
          </div>

          <!-- 账号排行（仅全部账号） -->
          <div v-if="scope === 'all'" class="rounded-md border p-3">
            <div class="mb-2 flex items-center justify-between">
              <p class="text-xs font-semibold">账号消耗排行</p>
              <!-- 区间切换移到这里，与排行/趋势联动 -->
              <div class="inline-flex rounded-md border bg-background p-0.5 text-xs">
                <button
                  v-for="(t, k) in {
                    today: '今日',
                    '7d': '近 7 天',
                    '30d': '近 30 天',
                    all: '总计',
                  }"
                  :key="k"
                  type="button"
                  class="rounded px-2 py-0.5 transition-colors"
                  :class="
                    range === k
                      ? 'bg-primary text-primary-foreground'
                      : 'text-muted-foreground hover:text-foreground'
                  "
                  @click="range = k as any"
                >
                  {{ t }}
                </button>
              </div>
            </div>
            <div
              v-for="row in visibleRank"
              :key="row.a.channel_id"
              class="flex cursor-pointer items-center gap-2 rounded-md px-1 py-1.5 text-xs hover:bg-muted/60"
              @click="pickAccount(row.a.channel_id)"
            >
              <span
                class="size-1.5 shrink-0 rounded-full"
                :class="WB_TONE_DOT[wbAccountBrief(row.a).tone]"
              />
              <span class="w-24 shrink-0 truncate font-medium">{{ row.a.label }}</span>
              <span class="w-10 shrink-0 font-mono text-[10px] text-muted-foreground">{{
                row.a.channel_id.slice(-4)
              }}</span>
              <div class="h-1.5 min-w-8 flex-1 overflow-hidden rounded-full bg-muted">
                <div
                  class="h-full rounded-full"
                  :class="
                    wbAccountBrief(row.a).tone === 'warn'
                      ? 'bg-amber-500'
                      : wbAccountBrief(row.a).tone === 'bad'
                        ? 'bg-red-500'
                        : wbAccountBrief(row.a).tone === 'off'
                          ? 'bg-slate-400'
                          : 'bg-emerald-500'
                  "
                  :style="{ width: row.barPct + '%' }"
                />
              </div>
              <span class="w-14 shrink-0 text-right tabular-nums text-muted-foreground">{{
                fmt(row.spent)
              }}</span>
              <span class="w-10 shrink-0 text-right font-semibold tabular-nums"
                >{{ row.pct.toFixed(1) }}%</span
              >
            </div>
            <button
              v-if="rankRows.length > 8"
              type="button"
              class="mt-1.5 text-xs text-blue-500 hover:underline"
              @click="showAllRank = !showAllRank"
            >
              {{ showAllRank ? '收起' : `+ 还有 ${rankRows.length - 8} 个账号 · 展开全部` }}
            </button>
          </div>


          <!-- 趋势图 -->
          <div class="rounded-md border p-3">
            <p class="mb-2 text-xs font-semibold">消耗趋势</p>
            <div
              v-if="!trendDays.length"
              class="flex h-48 items-center justify-center text-sm text-muted-foreground"
            >
              暂无消耗记录
            </div>
            <div v-else class="h-56"><VChart :option="trendOption" autoresize /></div>
          </div>
          <!-- 日志消耗面板：逐条消耗明细（远程基线 + 刷新后本地），选中账号时只显示该账号 -->
          <div class="rounded-md border p-3">
				<p class="mb-2 text-xs font-semibold">日志消耗面板</p>
				<div
					v-if="!usageLogs.length"
					class="py-4 text-center text-xs text-muted-foreground"
				>
					暂无消耗记录
				</div>
				<template v-else>
					<!-- 高度限制：超出在表格内滚动 -->
					<div class="max-h-80 overflow-y-auto">
						<AxTable>
							<Table class="text-xs">
								<TableHeader>
									<TableRow>
									<TableHead>时间</TableHead>
										<TableHead class="min-w-[110px]">账号</TableHead>
										<TableHead class="min-w-[140px]">模型</TableHead>
									<TableHead>来源</TableHead>
									<TableHead class="text-right">Tokens</TableHead>
									<TableHead class="text-right">缓存</TableHead>
									<TableHead class="text-right">消耗积分</TableHead>
									<TableHead class="text-right">日志</TableHead>
									</TableRow>
								</TableHeader>
								<TableBody>
									<TableRow v-for="log in pagedLogs" :key="log.source + log.request_id">
										<TableCell class="tabular-nums text-muted-foreground">{{ log.created_at }}</TableCell>
										<TableCell>{{ log.account || log.account_id.slice(0, 8) }}</TableCell>
										<TableCell>{{ log.model }}</TableCell>
										<TableCell>
											<span
												class="inline-block rounded px-1.5 py-0.5 text-[10px] font-medium"
												:class="
													log.source === 'remote'
														? 'bg-blue-500/15 text-blue-700 dark:text-blue-300'
														: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
												"
											>
												{{ log.source === 'remote' ? '远程' : '本地' }}
											</span>
										</TableCell>
										<TableCell class="whitespace-nowrap text-right text-xs tabular-nums">
											<template v-if="log.source === 'local'">
												<span class="font-medium text-foreground">
													{{ fmtTokens(log.prompt_tokens) }} /
													{{ fmtTokens(log.completion_tokens) }}
												</span>
												<span
													v-if="log.cached_tokens > 0"
													class="ml-1 text-[10px] text-violet-600 dark:text-violet-300"
													>缓存 ↓ {{ fmtTokens(log.cached_tokens) }}</span
												>
											</template>
											<span v-else class="text-muted-foreground">-</span>
										</TableCell>
										<TableCell class="whitespace-nowrap text-right text-xs tabular-nums font-mono">
											<span
												v-if="logCacheRatio(log)"
												class="font-medium text-violet-600 dark:text-violet-300"
											>
												{{ logCacheRatio(log) }}
											</span>
											<span v-else class="text-muted-foreground">-</span>
										</TableCell>
										<TableCell class="text-right font-semibold tabular-nums">{{ fmt(log.credit) }}</TableCell>
										<TableCell class="whitespace-nowrap text-right">
											<router-link
												v-if="log.source === 'local'"
												:to="`/request-logs/${log.request_id}`"
												class="text-xs text-primary hover:underline"
												>进入日志</router-link
											>
											<span v-else class="text-xs text-muted-foreground">-</span>
										</TableCell>
									</TableRow>
								</TableBody>
							</Table>
						</AxTable>
					</div>
					<DataPagination
						v-model:page="logPage"
						v-model:page-size="logPageSize"
						:total="usageLogs.length"
						:page-sizes="[20, 50, 100]"
					/>
				</template>
          </div>


          <!-- 模型明细表 -->
          <div class="rounded-md border p-3">
            <p class="mb-2 text-xs font-semibold">模型消耗明细</p>
            <div v-if="!modelRows.length" class="py-4 text-center text-xs text-muted-foreground">
              暂无消耗记录
            </div>
				<AxTable v-else>
					<Table class="text-xs">
						<TableHeader>
							<TableRow>
								<TableHead class="min-w-[140px]">模型</TableHead>
								<TableHead class="w-[70px] text-right">倍率</TableHead>
								<TableHead class="w-[80px] text-right">请求数</TableHead>
								<TableHead class="w-[90px] text-right">消耗积分</TableHead>
								<TableHead class="w-[70px] text-right">占比</TableHead>
							</TableRow>
						</TableHeader>
						<TableBody>
							<TableRow v-for="m in modelRows" :key="m.model">
								<TableCell>{{ m.model }}</TableCell>
								<TableCell
									class="text-right tabular-nums"
									:class="rateClass(modelRate(m.model)?.rate || 0)"
								>
									{{ (modelRate(m.model)?.rate || 0).toFixed(2) }}
								</TableCell>
								<TableCell class="text-right tabular-nums">{{ m.requests }}</TableCell>
								<TableCell class="text-right font-semibold tabular-nums">
									{{ fmt(m.credit) }}
								</TableCell>
								<TableCell class="text-right tabular-nums text-muted-foreground">
									{{ metrics.total ? ((m.credit / metrics.total) * 100).toFixed(1) : 0 }}%
								</TableCell>
							</TableRow>
						</TableBody>
					</Table>
				</AxTable>
          </div>
        </div>
      </div>
    </CardContent>
  </Card>
</template>
