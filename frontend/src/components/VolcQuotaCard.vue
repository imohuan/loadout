<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiDeleteBinLine,
  RiEditLine,
  RiLoader4Line,
  RiRefreshLine,
} from '@remixicon/vue'
import type {
  Channel,
  VolcQuotaConfig,
  VolcQuotaConfigDetails,
  VolcQuotaPackage,
} from '@/lib/types'
import { useChannels } from '@/composables/useChannels'
import { useVolcQuota } from '@/composables/useVolcQuota'
import { useConfirm } from '@/composables/useConfirm'
import EmptyState from '@/components/EmptyState.vue'

const quota = useVolcQuota()
const channelsApi = useChannels()
const { confirmDialog } = useConfirm()

// ===== 数据 =====
const configs = ref<VolcQuotaConfigDetails[]>([])
const arkChannels = ref<Channel[]>([])
const loading = ref(false)
const saving = ref(false)
const refreshingAll = ref(false)
const loaded = ref(false)

async function loadStatus(showSpinner = true) {
  if (showSpinner) loading.value = true
  try {
    const data = await quota.status()
    configs.value = data.configs || []
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载额度状态失败')
  } finally {
    if (showSpinner) loading.value = false
    loaded.value = true
  }
}

async function loadArkChannels() {
  try {
    const all = await channelsApi.list()
    arkChannels.value = all.filter(
      (c) => c.base_url && c.base_url.includes('ark.cn-beijing.volces.com'),
    )
  } catch {
    arkChannels.value = []
  }
}

onMounted(() => {
  loadStatus()
  loadArkChannels()
})

// ===== 折叠展开（参考 ChannelTable 的 expandedGroups 模式） =====
const expanded = ref<string[]>([])
function toggle(channelId: string) {
  const index = expanded.value.indexOf(channelId)
  if (index >= 0) expanded.value.splice(index, 1)
  else expanded.value.push(channelId)
}
function isExpanded(channelId: string) {
  return expanded.value.includes(channelId)
}

// ===== 展示辅助 =====
function title(item: VolcQuotaConfigDetails) {
  const c = item.config
  return c.channel_name || c.key_name || c.base_url || c.channel_id
}
function statusBadge(c: VolcQuotaConfig) {
  if (c.last_error) return { variant: 'destructive' as const, label: '同步失败' }
  if (c.last_synced_at) return { variant: 'default' as const, label: '已同步' }
  return { variant: 'secondary' as const, label: '未同步' }
}
/** 资源包耗尽数（v17 后 models 表已删，从 packages 算） */
function exhaustedCount(item: VolcQuotaConfigDetails): number {
  let n = 0
  for (const p of item.packages || []) {
    if (p.local_remaining <= 0 && p.initial_total > 0) n++
  }
  return n
}
/** 大数字缩写：1.2M / 3.4B */
function formatAmount(n: number) {
  if (n >= 1e9) return (n / 1e9).toFixed(2).replace(/\.00$/, '') + 'B'
  if (n >= 1e6) return (n / 1e6).toFixed(2).replace(/\.00$/, '') + 'M'
  if (n >= 1e4) return (n / 1e3).toFixed(1).replace(/\.0$/, '') + 'K'
  return String(n)
}
function formatTime(iso?: string) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString()
}

// ===== 资源包明细展示（v14） =====

/** 资源包显示名：优先 ConfigurationName，退回 ProductName/Product */
function pkgName(p: VolcQuotaPackage) {
  return p.configuration_name || p.product_name || p.product || p.instance_no
}
/** 资源包状态徽章：Effective=绿，UsedUp=红，其他（Expired等）=灰 */
function pkgBadge(p: VolcQuotaPackage) {
  if (p.status === 'Effective') return { variant: 'secondary' as const, label: '有效' }
  if (p.status === 'UsedUp') return { variant: 'destructive' as const, label: '已用完' }
  return { variant: 'outline' as const, label: p.status || '未知' }
}
/** 资源包过期时间友好显示 */
function pkgExpiry(p: VolcQuotaPackage) {
  if (!p.expiry_time) return ''
  return formatTime(p.expiry_time)
}
/** 资源包剩余进度（0~100）—— 本地口径：用 local_remaining / initial_total。
 * 首次刷新建底数 local_remaining=initial_total=available_amount，后续请求扣减。
 * 剩余=0 视为已耗尽（即使 billing 短暂显示有余额，本地优先拦截）。 */
function pkgProgress(p: VolcQuotaPackage) {
  if (p.initial_total > 0) {
    return Math.max(0, Math.min(100, Math.round((p.local_remaining / p.initial_total) * 100)))
  }
  if (p.total_amount <= 0) return 0
  return Math.max(0, Math.min(100, Math.round((p.available_amount / p.total_amount) * 100)))
}

/** 资源包过滤：按 configuration_name / configuration_code / product 模糊匹配 */
function matchPackage(p: VolcQuotaPackage, kw: string) {
  if (!kw) return true
  const k = kw.toLowerCase()
  return (
    (p.configuration_name || '').toLowerCase().includes(k) ||
    (p.configuration_code || '').toLowerCase().includes(k) ||
    (p.product_name || '').toLowerCase().includes(k) ||
    (p.product || '').toLowerCase().includes(k)
  )
}

/** 资源包过滤输入（按 channel_id 隔离，多 Key 不互相干扰） */
const pkgFilter = ref<Record<string, string>>({})
function setPkgFilter(channelId: string, v: string) {
  pkgFilter.value[channelId] = v
}
function getPkgFilter(channelId: string) {
  return pkgFilter.value[channelId] || ''
}
function filteredPackages(item: { config: { channel_id: string }; packages?: VolcQuotaPackage[] }) {
  const kw = getPkgFilter(item.config.channel_id)
  const list = item.packages || []
  if (!kw) return list
  return list.filter((p) => matchPackage(p, kw))
}

// ===== 资源包视图切换：表格 / 卡片（按 channel_id 隔离） =====
type PkgViewMode = 'table' | 'card'
const pkgView = ref<Record<string, PkgViewMode>>({})
function getPkgView(channelId: string): PkgViewMode {
  return pkgView.value[channelId] || 'table'
}
function setPkgView(channelId: string, v: string) {
  if (v === 'table' || v === 'card') pkgView.value[channelId] = v
}

// ===== 卡片视图：按 model 聚合资源包（口径同后台 syncModelStatesByAggregate） =====
interface AggregatedPackage {
  key: string // 聚合锚点：model，退回 configuration_code / configuration_name
  name: string // 展示名
  unit: string
  initialTotal: number // SUM(initial_total)
  localRemaining: number // SUM(local_remaining)
  usedAmount: number // SUM(used_amount)；本地口径下=initialTotal-localRemaining
  totalAmount: number // SUM(total_amount)
  exhausted: boolean // 是否已耗尽
  percentage: number // 0~100 本地口径
}
function aggregatePackages(
  item: { config: { channel_id: string }; packages?: VolcQuotaPackage[] },
): AggregatedPackage[] {
  const list = filteredPackages(item)
  const map = new Map<string, AggregatedPackage>()
  for (const p of list) {
    const key = p.model || p.configuration_code || pkgName(p)
    const name = p.configuration_name || p.model || pkgName(p)
    let agg = map.get(key)
    if (!agg) {
      agg = {
        key,
        name,
        unit: p.unit,
        initialTotal: 0,
        localRemaining: 0,
        usedAmount: 0,
        totalAmount: 0,
        exhausted: false,
        percentage: 0,
      }
      map.set(key, agg)
    }
    agg.initialTotal += p.initial_total
    agg.localRemaining += p.local_remaining
    agg.usedAmount += p.used_amount
    agg.totalAmount += p.total_amount
    if (p.status === 'UsedUp' || (p.local_remaining <= 0 && p.initial_total > 0)) agg.exhausted = true
  }
  const out: AggregatedPackage[] = []
  for (const agg of map.values()) {
    // 本地口径百分比：有 initialTotal 用 localRemaining/initialTotal，否则退回 available/total。
    if (agg.initialTotal > 0) {
      agg.percentage = Math.max(0, Math.min(100, Math.round((agg.localRemaining / agg.initialTotal) * 100)))
      agg.usedAmount = agg.initialTotal - agg.localRemaining
    } else if (agg.totalAmount > 0) {
      agg.percentage = Math.max(0, Math.min(100, Math.round((agg.localRemaining / agg.totalAmount) * 100)))
    } else {
      agg.percentage = 0
    }
    out.push(agg)
  }
  // 排序：未耗尽在前，剩余多的在前
  out.sort((a, b) => {
    if (a.exhausted !== b.exhausted) return a.exhausted ? 1 : -1
    return b.localRemaining - a.localRemaining
  })
  return out
}

// ===== 刷新 =====
// 刷新本地：只重查 SQLite 现有数据，刷新 UI（不碰远程 API）。
async function refreshLocal() {
  refreshingAll.value = true
  try {
    await loadStatus(false)
    toast.success('本地数据已刷新')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingAll.value = false
  }
}

// 刷新远程：先弹 dialog 检查近 10 分钟请求日志 → 用户确认后真正拉远程。
const remoteDialogOpen = ref(false)
const remoteChecking = ref(false)
const remoteTarget = ref('') // 空 = 全量；否则 channel_id
const remoteTargetName = ref('')
const recentUsage = ref<{
  has_recent: boolean
  request_count: number
  last_request_at: string
} | null>(null)

function openRemoteRefresh(channelId = '') {
  remoteTarget.value = channelId
  if (channelId) {
    const item = configs.value.find((c) => c.config.channel_id === channelId)
    remoteTargetName.value = item ? title(item) : channelId
  } else {
    remoteTargetName.value = '全部 Key'
  }
  remoteDialogOpen.value = true
  checkRecentUsage()
}

async function checkRecentUsage() {
  remoteChecking.value = true
  recentUsage.value = null
  try {
    const data = await quota.recentUsage(remoteTarget.value, 10)
    recentUsage.value = {
      has_recent: data.has_recent,
      request_count: data.request_count,
      last_request_at: data.last_request_at,
    }
  } catch {
    // 查询失败不阻断，让用户自行判断。
    recentUsage.value = { has_recent: false, request_count: 0, last_request_at: '' }
  } finally {
    remoteChecking.value = false
  }
}

async function confirmRemoteRefresh() {
  remoteDialogOpen.value = false
  refreshingAll.value = true
  try {
    const result = await quota.refresh(remoteTarget.value || undefined)
    applyRefreshResult(result.configs_checked, result.failed_channels, result.disabled_models)
    await loadStatus(false)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingAll.value = false
  }
}

async function refreshOne(channelId: string) {
  // 单个 Key 的刷新也是远程刷新，走 dialog 检查流程。
  openRemoteRefresh(channelId)
}

function applyRefreshResult(checked: number, failed?: string[], disabled?: string[]) {
  const parts: string[] = []
  if (checked > 0) parts.push(`已刷新 ${checked} 个配置`)
  if (disabled?.length) {
    parts.push(`已禁用 ${disabled.length} 个耗尽模型`)
    toast.warning(parts.join('，') + `：${disabled.slice(0, 8).join('、')}`)
    return
  }
  if (failed?.length) {
    toast.error(`刷新失败 ${failed.length} 个渠道：${failed.slice(0, 5).join('、')}`)
    return
  }
  toast.success(parts.join('') || '刷新完成')
}

// ===== 新增 / 编辑 / 删除 =====
const dialogOpen = ref(false)
const editing = ref(false) // false = 新增
const form = reactive<{
  channel_id: string
  access_key: string
  secret_key: string
  enabled: boolean
  force_block: boolean
}>({
  channel_id: '',
  access_key: '',
  secret_key: '',
  enabled: true,
  force_block: false,
})

function resetForm() {
  Object.assign(form, {
    channel_id: '',
    access_key: '',
    secret_key: '',
    enabled: true,
    force_block: false,
  })
  editing.value = false
}

function openCreate() {
  resetForm()
  dialogOpen.value = true
}

function openEdit(item: VolcQuotaConfigDetails) {
  const c = item.config
  editing.value = true
  Object.assign(form, {
    channel_id: c.channel_id,
    access_key: c.access_key,
    secret_key: '', // 编辑时不回显明文
    enabled: c.enabled,
    force_block: c.force_block ?? false,
  })
  dialogOpen.value = true
}

async function submit() {
  if (!form.channel_id) {
    toast.error('请选择渠道 Key')
    return
  }
  if (!form.access_key || (!editing.value && !form.secret_key)) {
    toast.error('请填写 AK/SK')
    return
  }
  saving.value = true
  try {
    const draft: VolcQuotaConfig = {
      channel_id: form.channel_id,
      access_key: form.access_key,
      secret_key: form.secret_key || undefined, // 留空 = 保留既有密文
      enabled: form.enabled,
      force_block: form.force_block,
    }
    // 编辑（同渠道已存在）→ 替换该行；新增 → 追加（PUT 为整体覆盖语义）。
    const existed = configs.value.some((item) => item.config.channel_id === form.channel_id)
    const payload = existed
      ? configs.value.map((item) =>
          item.config.channel_id === form.channel_id ? draft : item.config,
        )
      : [...configs.value.map((item) => item.config), draft]
    await quota.save(payload)
    dialogOpen.value = false
    toast.success(editing.value ? '配置已更新' : '配置已添加')
    await Promise.all([loadStatus(false), loadArkChannels()])
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    saving.value = false
  }
}

async function remove(channelId: string) {
  if (!(await confirmDialog('删除该 Key 的额度监控配置？（不会删除渠道本身）'))) return
  try {
    await quota.save(
      configs.value
        .filter((item) => item.config.channel_id !== channelId)
        .map((item) => item.config),
    )
    toast.success('配置已删除')
    await loadStatus(false)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '删除失败')
  }
}

const displayName = (ch: Channel) => ch.channel_name || ch.name
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="text-base">火山引擎免费额度</CardTitle>
      <CardDescription>
        为方舟渠道 Key 配置 AK/SK（火山引擎控制台访问授权），自动查询免费模型额度；每次请求按
        total_tokens 本地扣减余额（不依赖账单接口，账单 429
        也能拦截）；额度耗尽后自动禁用该模型（每 15 分钟检测恢复，错误信息「模型免费额度用完」）。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="text-xs text-muted-foreground">
          共 {{ configs.length }} 个 Key 配置<span v-if="loaded">，额度每日自动恢复（8~11:30 不等）</span>
        </div>
        <div class="flex items-center gap-2">
          <Button variant="outline" size="sm" :disabled="refreshingAll" @click="refreshLocal">
            <RiRefreshLine size="16" />
            刷新本地
          </Button>
          <Button size="sm" :disabled="refreshingAll" @click="openRemoteRefresh()">
            <RiLoader4Line v-if="refreshingAll" class="animate-spin" size="16" />
            <RiRefreshLine v-else size="16" />
            刷新远程
          </Button>
          <Button size="sm" @click="openCreate">
            <RiAddLine size="16" />
            添加配置
          </Button>
        </div>
      </div>

      <div v-if="configs.length" class="divide-y rounded-md border">
        <div v-for="item in configs" :key="item.config.channel_id">
          <!-- 折叠行 -->
          <div class="flex items-center gap-1 px-2 py-2">
            <Button
              variant="ghost"
              size="icon"
              class="size-8 shrink-0"
              :aria-label="isExpanded(item.config.channel_id) ? '收起' : '展开'"
              :aria-expanded="isExpanded(item.config.channel_id)"
              @click="toggle(item.config.channel_id)"
            >
              <RiArrowDownSLine v-if="isExpanded(item.config.channel_id)" size="16" />
              <RiArrowRightSLine v-else size="16" />
            </Button>
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-medium">{{ title(item) }}</div>
              <div class="truncate font-mono text-xs text-muted-foreground">
                {{ item.config.base_url
                }}<span v-if="item.config.access_key">
                  · AK {{ item.config.access_key.slice(0, 8) }}…</span
                >
              </div>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <Badge v-if="item.config.force_block" variant="destructive"> 强制关停 </Badge>
              <Badge v-if="exhaustedCount(item) > 0" variant="destructive">
                {{ exhaustedCount(item) }} 个已耗尽
              </Badge>
              <Badge :variant="statusBadge(item.config).variant">
                {{ statusBadge(item.config).label }}
              </Badge>
              <div class="flex items-center gap-0.5">
                <Button
                  variant="ghost"
                  size="icon"
                  class="size-8"
                  aria-label="刷新此 Key 额度"
                  :disabled="refreshingAll"
                  @click="refreshOne(item.config.channel_id)"
                >
                  <RiRefreshLine size="16" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  class="size-8"
                  aria-label="编辑配置"
                  @click="openEdit(item)"
                >
                  <RiEditLine size="16" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  class="size-8"
                  aria-label="删除配置"
                  @click="remove(item.config.channel_id)"
                >
                  <RiDeleteBinLine size="16" />
                </Button>
              </div>
            </div>
          </div>

          <!-- 展开行：资源包逐条明细（v14，同 main.go 输出粒度） -->
          <div
            v-if="isExpanded(item.config.channel_id)"
            class="space-y-3 border-t bg-muted/30 px-4 py-4"
          >
            <!-- 资源包逐条明细 -->
            <div
              v-if="item.packages?.length"
              class="overflow-hidden rounded-md border bg-background/60"
            >
              <div class="flex items-center justify-between gap-2 border-b bg-muted/50 px-3 py-1.5">
                <span class="text-xs font-medium text-muted-foreground">
                  资源包（{{ filteredPackages(item).length }} / {{ item.packages.length }}）
                </span>
                <div class="flex items-center gap-2">
                  <Tabs
                    :model-value="getPkgView(item.config.channel_id)"
                    class="[&_[data-slot=tabs-list]]:h-7 [&_[data-slot=tabs-trigger]]:px-2 [&_[data-slot=tabs-trigger]]:text-xs"
                    @update:model-value="(v: string) => setPkgView(item.config.channel_id, v)"
                  >
                    <TabsList>
                      <TabsTrigger value="table">表格</TabsTrigger>
                      <TabsTrigger value="card">卡片</TabsTrigger>
                    </TabsList>
                  </Tabs>
                  <Input
                    :model-value="getPkgFilter(item.config.channel_id)"
                    placeholder="过滤：模型名 / code / 关键字…"
                    class="h-7 w-56 text-xs"
                    @update:model-value="(v: string) => setPkgFilter(item.config.channel_id, v)"
                  />
                </div>
              </div>
              <Table v-if="getPkgView(item.config.channel_id) === 'table'" class="w-full text-xs">
                <TableHeader>
                  <TableRow class="bg-muted/30 hover:bg-muted/30">
                    <TableHead>资源包</TableHead>
                    <TableHead class="text-right">总额</TableHead>
                    <TableHead class="w-[30%]">剩余（本地）</TableHead>
                    <TableHead class="text-right">已用</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead>到期</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow
                    v-for="p in filteredPackages(item)"
                    :key="p.instance_no"
                    class="hover:bg-muted/30"
                  >
                    <TableCell class="py-1.5 align-top">
                      <div class="truncate font-medium" :title="pkgName(p)">{{ pkgName(p) }}</div>
                      <div
                        class="truncate font-mono text-[10px] text-muted-foreground"
                        :title="p.configuration_code"
                      >
                        {{ p.configuration_code || p.product || '' }}
                      </div>
                    </TableCell>
                    <TableCell class="py-1.5 text-right tabular-nums">{{
                      formatAmount(p.total_amount)
                    }}</TableCell>
                    <TableCell class="py-1.5">
                      <div class="flex items-center gap-2">
                        <Progress
                          :model-value="pkgProgress(p)"
                          :class="
                            p.local_remaining <= 0 && p.initial_total > 0
                              ? 'bg-destructive/20'
                              : p.total_amount > 0 && p.available_amount / p.total_amount < 0.2
                                ? 'bg-amber-500/20'
                                : ''
                          "
                          class="h-1.5 flex-1"
                        />
                        <!-- 本地余额精确显示（千分位），否则 formatAmount(1999811)="2M" 看不到扣减差异 -->
                        <span
                          class="w-32 shrink-0 text-right tabular-nums"
                          :class="
                            p.local_remaining <= 0 && p.initial_total > 0 ? 'text-destructive' : ''
                          "
                          :title="`本地剩余 ${p.local_remaining} / 初始 ${p.initial_total}`"
                        >
                          {{ p.local_remaining.toLocaleString()
                          }}<span v-if="p.initial_total > 0" class="text-muted-foreground"
                            >/{{ p.initial_total.toLocaleString() }}</span
                          >
                        </span>
                      </div>
                    </TableCell>
                    <!-- 已用：本地口径（initial_total - local_remaining），不是 billing used_amount
                         （billing 是 total-available，本地扣的不会算进去）。 -->
                    <TableCell class="py-1.5 text-right tabular-nums text-muted-foreground">
                      {{
                        (p.initial_total > 0
                          ? p.initial_total - p.local_remaining
                          : p.used_amount
                        ).toLocaleString()
                      }}
                    </TableCell>
                    <TableCell class="py-1.5">
                      <Badge :variant="pkgBadge(p).variant">{{ pkgBadge(p).label }}</Badge>
                    </TableCell>
                    <TableCell class="py-1.5 text-muted-foreground">{{ pkgExpiry(p) }}</TableCell>
                  </TableRow>
                </TableBody>
              </Table>

              <!-- 卡片视图：按 model 聚合资源包，网格一行 4 个 -->
              <div
                v-else-if="getPkgView(item.config.channel_id) === 'card'"
                class="grid grid-cols-1 gap-2 p-3 sm:grid-cols-2 lg:grid-cols-4"
              >
                <div
                  v-for="a in aggregatePackages(item)"
                  :key="a.key"
                  class="flex flex-col gap-2 rounded-md border bg-background/60 p-3"
                  :class="a.exhausted ? 'border-destructive/40 bg-destructive/5' : ''"
                >
                  <!-- 顶部：左侧模型名，右侧 token 积分（当前剩余/总） -->
                  <div class="flex items-start justify-between gap-2">
                    <div class="min-w-0">
                      <div class="truncate text-sm font-medium" :title="a.name">{{ a.name }}</div>
                      <div class="text-[10px] uppercase tracking-wide text-muted-foreground">
                        {{ a.unit || 'token' }}
                      </div>
                    </div>
                    <div class="shrink-0 text-right">
                      <div
                        class="text-sm font-semibold tabular-nums"
                        :class="a.exhausted ? 'text-destructive' : ''"
                      >
                        {{ a.localRemaining.toLocaleString() }}
                      </div>
                      <div class="text-[10px] text-muted-foreground tabular-nums">
                        / {{ a.initialTotal.toLocaleString() }}
                      </div>
                    </div>
                  </div>
                  <!-- 进度条 + 右侧百分比 -->
                  <div class="flex items-center gap-2">
                    <Progress
                      :model-value="a.percentage"
                      class="h-1.5 flex-1"
                      :class="
                        a.exhausted
                          ? 'bg-destructive/20'
                          : a.percentage < 20
                            ? 'bg-amber-500/20'
                            : ''
                      "
                    />
                    <span
                      class="w-9 shrink-0 text-right text-xs tabular-nums text-muted-foreground"
                      :class="a.exhausted ? 'text-destructive' : ''"
                    >
                      {{ a.percentage }}%
                    </span>
                  </div>
                </div>
              </div>
              <div
                v-if="filteredPackages(item).length === 0"
                class="px-3 py-4 text-center text-xs text-muted-foreground"
              >
                没有匹配「{{ getPkgFilter(item.config.channel_id) }}」的资源包
              </div>
            </div>

            <EmptyState
              v-else-if="!item.packages?.length"
              title="暂无额度数据"
              description="点击右侧刷新按钮获取最新额度（账单有延迟，后台每 15 分钟自动刷新）。"
            />
          </div>
        </div>
      </div>

      <EmptyState
        v-else
        title="还没有配置"
        description="添加一个方舟渠道 Key 的 AK/SK，即可查看各免费模型的剩余额度。"
      />
    </CardContent>
  </Card>

  <Dialog v-model:open="dialogOpen">
    <DialogContent class="max-w-xl!">
      <DialogHeader>
        <DialogTitle>{{ editing ? '编辑额度配置' : '添加额度配置' }}</DialogTitle>
        <DialogDescription>
          每个渠道 Key 对应一对 AK/SK（火山引擎控制台 → 访问控制 → API 访问密钥）。
        </DialogDescription>
      </DialogHeader>
      <form class="space-y-3" @submit.prevent="submit">
        <div class="space-y-1">
          <Label>渠道 Key</Label>
          <Select v-model="form.channel_id" :disabled="editing">
            <SelectTrigger>
              <SelectValue placeholder="选择方舟渠道（ark.cn-beijing.volces.com）" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem v-for="ch in arkChannels" :key="ch.id" :value="ch.id">
                {{ displayName(ch) }}
              </SelectItem>
            </SelectContent>
          </Select>
          <p v-if="!arkChannels.length" class="text-xs text-muted-foreground">
            未找到方舟渠道，请先在「渠道列表」中添加 base_url 为
            https://ark.cn-beijing.volces.com/api/v3 的渠道。
          </p>
        </div>
        <div class="space-y-1">
          <Label for="vq-access-key">Access Key（AK）</Label>
          <Input id="vq-access-key" v-model="form.access_key" placeholder="AKLT…" required />
        </div>
        <div class="space-y-1">
          <Label for="vq-secret-key">Secret Key（SK）</Label>
          <Input
            id="vq-secret-key"
            v-model="form.secret_key"
            type="password"
            :placeholder="editing ? '留空保持不变' : 'SK 仅保存时传输，界面不回显'"
            :required="!editing"
          />
        </div>
        <div class="flex items-center gap-2">
          <Switch id="vq-enabled" v-model="form.enabled" />
          <Label for="vq-enabled">启用额度监控与自动禁用</Label>
        </div>
        <div class="flex items-center gap-2">
          <Switch id="vq-force-block" v-model="form.force_block" />
          <Label for="vq-force-block">强制关停（忽略手动恢复）</Label>
          <span class="text-xs text-muted-foreground"
            >开启后，即使模型状态被手动恢复，请求时仍按额度表拦截</span
          >
        </div>
        <DialogFooter>
          <Button type="submit" :disabled="saving">
            <RiLoader4Line v-if="saving" class="animate-spin" size="16" />
            {{ editing ? '保存修改' : '添加' }}
          </Button>
          <Button type="button" variant="outline" @click="dialogOpen = false">取消</Button>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>

  <!-- 远程刷新确认：先查近 10 分钟请求日志，有请求则提醒用户 -->
  <Dialog v-model:open="remoteDialogOpen">
    <DialogContent class="max-w-xl!">
      <DialogHeader>
        <DialogTitle>刷新远程额度</DialogTitle>
        <DialogDescription>
          将重新调用火山引擎账单接口拉取最新免费额度（{{ remoteTargetName }}）。
        </DialogDescription>
      </DialogHeader>
      <div class="space-y-3">
        <div class="rounded-md border bg-muted/40 p-3 text-sm">
          <div v-if="remoteChecking" class="flex items-center gap-2 text-muted-foreground">
            <RiLoader4Line class="animate-spin" size="16" />
            正在查询近 10 分钟请求日志…
          </div>
          <div v-else-if="recentUsage && recentUsage.has_recent" class="space-y-1">
            <div class="flex items-center gap-2 font-medium text-amber-600">
              <span class="inline-block size-2 rounded-full bg-amber-500"></span>
              该 账户 近 10 分钟有 {{ recentUsage.request_count }} 次模型请求
            </div>
            <div class="text-xs text-muted-foreground">
              最后一次请求于 {{ formatTime(recentUsage.last_request_at) }}。
              刷新期间请求可能互相干扰，建议等请求空闲后再刷新。
            </div>
          </div>
          <div v-else class="flex items-center gap-2 font-medium text-emerald-600">
            <span class="inline-block size-2 rounded-full bg-emerald-500"></span>
            近 10 分钟无请求，可以安全刷新
          </div>
        </div>
      </div>
      <DialogFooter>
        <Button
          type="button"
          :disabled="remoteChecking || refreshingAll"
          @click="confirmRemoteRefresh"
        >
          <RiLoader4Line v-if="refreshingAll" class="animate-spin" size="16" />
          确认刷新
        </Button>
        <Button type="button" variant="outline" @click="remoteDialogOpen = false">取消</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
