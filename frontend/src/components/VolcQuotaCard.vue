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
import VolcQuotaModelCards from '@/components/VolcQuotaModelCards.vue'

const quota = useVolcQuota()
const channelsApi = useChannels()
const { confirmDialog } = useConfirm()

// ===== 数据 =====
const configs = ref<VolcQuotaConfigDetails[]>([])
const arkChannels = ref<Channel[]>([])
const loading = ref(false)
const saving = ref(false)
const refreshingLocal = ref(false)
const refreshingRemote = ref(false)
const loaded = ref(false)

async function loadStatus(showSpinner = true) {
  if (showSpinner) loading.value = true
  try {
    const data = await quota.status()
    configs.value = data.configs || []
    // 默认展开第一个 Key 渠道，方便直接看明细
    if (!expanded.value.length && configs.value.length) {
      expanded.value = [configs.value[0].config.channel_id]
    }
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

// ===== 卡片组件实例收集：按 channel_id 存储每个 Key 的模型聚合卡片，
// 刷新后调用对应实例的 refresh() 重载数据（无需重新挂载组件）。 =====
const cardRefs = new Map<string, { refresh: () => Promise<void> }>()
// 缓存每个 channelId 的 ref 回调，避免 :ref 每次渲染重建新函数。
const cardRefCbs = new Map<string, (el: unknown) => void>()
function setCardRef(channelId: string) {
  let cb = cardRefCbs.get(channelId)
  if (!cb) {
    cb = (el: unknown) => {
      if (el) cardRefs.set(channelId, el as { refresh: () => Promise<void> })
      else cardRefs.delete(channelId)
    }
    cardRefCbs.set(channelId, cb)
  }
  return cb
}
function refreshCards(channelIds?: string[]) {
  const targets = channelIds
    ? channelIds.map((id) => cardRefs.get(id)).filter(Boolean)
    : [...cardRefs.values()]
  return Promise.all(
    (targets as { refresh: () => Promise<void> }[]).map((c) => c.refresh()),
  )
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

// ===== 资源包视图切换：卡片（聚合概览，默认）/ 表格（明细） =====
type PkgViewMode = 'card' | 'table'
const pkgView = ref<PkgViewMode>('card')
function setPkgView(v: string) {
  if (v === 'card' || v === 'table') pkgView.value = v
}

// ===== 刷新 =====
// 刷新本地：只重查 SQLite 现有数据，刷新 UI（不碰远程 API）。
async function refreshLocal() {
  refreshingLocal.value = true
  try {
    await loadStatus(false)
    // 同步渠道勾选，保持「关注」分组与渠道配置一致
    await loadArkChannels()
    // 同步刷新所有卡片的聚合数据（表格数据已由 loadStatus 刷新）
    await refreshCards()
    toast.success('本地数据已刷新')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingLocal.value = false
  }
}

// 刷新远程：先弹 dialog 检查近 10 分钟请求日志 → 用户确认后真正拉远程。
const remoteDialogOpen = ref(false)
const remoteChecking = ref(false)
const remoteTarget = ref('') // 空 = 全量；否则 channel_id
const remoteTargetName = ref('')
const remoteForce = ref(false) // 强制刷新：把本地余额拉回远程值
const recentUsage = ref<{
  has_recent: boolean
  request_count: number
  last_request_at: string
} | null>(null)

function openRemoteRefresh(channelId = '') {
  remoteTarget.value = channelId
  remoteForce.value = false
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
  refreshingRemote.value = true
  try {
    const result = await quota.refresh(remoteTarget.value || undefined, remoteForce.value)
    applyRefreshResult(result.configs_checked, result.failed_channels, result.disabled_models)
    await loadStatus(false)
    await loadArkChannels()
    // 同步刷新卡片聚合数据：单 Key 只刷该 Key，全量刷所有
    await refreshCards(remoteTarget.value ? [remoteTarget.value] : undefined)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingRemote.value = false
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

    await refreshCards()
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
    await refreshCards()
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
          <Tabs :model-value="pkgView"
            class="[&_[data-slot=tabs-list]]:h-8 [&_[data-slot=tabs-trigger]]:px-3 [&_[data-slot=tabs-trigger]]:text-xs"
            @update:model-value="setPkgView">
            <TabsList>
              <TabsTrigger value="card">卡片</TabsTrigger>
              <TabsTrigger value="table">表格</TabsTrigger>
            </TabsList>
          </Tabs>
          <Button variant="outline" size="sm" :disabled="refreshingLocal" @click="refreshLocal">
            <RiLoader4Line v-if="refreshingLocal" class="animate-spin" size="16" />
            <RiRefreshLine v-else size="16" />
            刷新本地
          </Button>
          <Button size="sm" :disabled="refreshingRemote" @click="openRemoteRefresh()">
            <RiLoader4Line v-if="refreshingRemote" class="animate-spin" size="16" />
            <RiRefreshLine v-else size="16" />
            刷新远程
          </Button>
          <Button size="sm" @click="openCreate">
            <RiAddLine size="16" />
            添加配置
          </Button>
        </div>
      </div>



      <!-- 表格视图：每个 Key 的折叠明细 -->
      <div v-if="configs.length" class="divide-y rounded-md border">
        <div v-for="item in configs" :key="item.config.channel_id">
          <!-- 折叠行 -->
          <div class="flex items-center gap-1 px-2 py-2">
            <Button variant="ghost" size="icon" class="size-8 shrink-0"
              :aria-label="isExpanded(item.config.channel_id) ? '收起' : '展开'"
              :aria-expanded="isExpanded(item.config.channel_id)" @click="toggle(item.config.channel_id)">
              <RiArrowDownSLine v-if="isExpanded(item.config.channel_id)" size="16" />
              <RiArrowRightSLine v-else size="16" />
            </Button>
            <div class="min-w-0 flex-1">
              <div class="truncate text-sm font-medium">{{ title(item) }}</div>
              <div class="truncate font-mono text-xs text-muted-foreground">
                {{ item.config.base_url
                }}<span v-if="item.config.access_key">
                  · AK {{ item.config.access_key.slice(0, 8) }}…</span>
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
                <Button variant="ghost" size="icon" class="size-8" aria-label="刷新此 Key 额度" :disabled="refreshingRemote"
                  @click="refreshOne(item.config.channel_id)">
                  <RiRefreshLine size="16" />
                </Button>
                <Button variant="ghost" size="icon" class="size-8" aria-label="编辑配置" @click="openEdit(item)">
                  <RiEditLine size="16" />
                </Button>
                <Button variant="ghost" size="icon" class="size-8" aria-label="删除配置"
                  @click="remove(item.config.channel_id)">
                  <RiDeleteBinLine size="16" />
                </Button>
              </div>
            </div>
          </div>

          <!-- 展开行：资源包逐条明细（v14，同 main.go 输出粒度） -->
          <div v-if="isExpanded(item.config.channel_id)" class="space-y-3 border-t bg-muted/30 px-4 py-4">
            <!-- 卡片：该 Key 模型聚合（关注/其他分组） -->
            <!-- 卡片（常驻，切换 tab 不重复请求） -->
            <div v-show="pkgView === 'card'">
              <VolcQuotaModelCards
                :ref="setCardRef(item.config.channel_id)"
                :channel-id="item.config.channel_id"
              />
            </div>
            <!-- 表格（常驻） -->
            <div v-show="pkgView === 'table'">
              <div v-if="item.packages?.length" class="overflow-hidden rounded-md border bg-background/60">
              <div class="flex items-center justify-between gap-2 border-b bg-muted/50 px-3 py-1.5">
                <span class="text-xs font-medium text-muted-foreground">
                  资源包（{{ filteredPackages(item).length }} / {{ item.packages.length }}）
                </span>
                <Input :model-value="getPkgFilter(item.config.channel_id)" placeholder="过滤：模型名 / code / 关键字…"
                  class="h-7 w-56 text-xs"
                  @update:model-value="(v: string) => setPkgFilter(item.config.channel_id, v)" />
              </div>
              <Table class="w-full text-xs">
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
                  <TableRow v-for="p in filteredPackages(item)" :key="p.instance_no" class="hover:bg-muted/30">
                    <TableCell class="py-1.5 align-top">
                      <div class="truncate font-medium" :title="pkgName(p)">{{ pkgName(p) }}</div>
                      <div class="truncate font-mono text-[10px] text-muted-foreground" :title="p.configuration_code">
                        {{ p.configuration_code || p.product || '' }}
                      </div>
                    </TableCell>
                    <TableCell class="py-1.5 text-right tabular-nums">{{
                      formatAmount(p.total_amount)
                      }}</TableCell>
                    <TableCell class="py-1.5">
                      <div class="flex items-center gap-2">
                        <Progress :model-value="pkgProgress(p)" :class="p.local_remaining <= 0 && p.initial_total > 0
                            ? 'bg-destructive/20'
                            : p.total_amount > 0 && p.available_amount / p.total_amount < 0.2
                              ? 'bg-amber-500/20'
                              : ''
                          " class="h-1.5 flex-1" />
                        <!-- 本地余额精确显示（千分位），否则 formatAmount(1999811)="2M" 看不到扣减差异 -->
                        <span class="w-32 shrink-0 text-right tabular-nums" :class="p.local_remaining <= 0 && p.initial_total > 0 ? 'text-destructive' : ''
                          " :title="`本地剩余 ${p.local_remaining} / 初始 ${p.initial_total}`">
                          {{ p.local_remaining.toLocaleString()
                          }}<span v-if="p.initial_total > 0" class="text-muted-foreground">/{{
                            p.initial_total.toLocaleString() }}</span>
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
              <div v-if="filteredPackages(item).length === 0"
                class="px-3 py-4 text-center text-xs text-muted-foreground">
                没有匹配「{{ getPkgFilter(item.config.channel_id) }}」的资源包
              </div>
              </div>
              <EmptyState v-else title="暂无额度数据"
              description="点击右侧刷新按钮获取最新额度（账单有延迟，后台每 15 分钟自动刷新）。" />
          </div>
        </div>
      </div>
      </div>

      <EmptyState v-else title="还没有配置" description="添加一个方舟渠道 Key 的 AK/SK，即可查看各免费模型的剩余额度。" />
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
          <Input id="vq-secret-key" v-model="form.secret_key" type="password"
            :placeholder="editing ? '留空保持不变' : 'SK 仅保存时传输，界面不回显'" :required="!editing" />
        </div>
        <div class="flex items-center gap-2">
          <Switch id="vq-enabled" v-model="form.enabled" />
          <Label for="vq-enabled">启用额度监控与自动禁用</Label>
        </div>
        <div class="flex items-center gap-2">
          <Switch id="vq-force-block" v-model="form.force_block" />
          <Label for="vq-force-block">强制关停（忽略手动恢复）</Label>
          <span class="text-xs text-muted-foreground">开启后，即使模型状态被手动恢复，请求时仍按额度表拦截</span>
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
        <div class="flex items-center gap-2 rounded-md border p-3">
          <Switch id="vq-force-refresh" v-model="remoteForce" />
          <Label for="vq-force-refresh">强制刷新</Label>
          <span class="text-xs text-muted-foreground">
            将本地剩余额度强制覆盖为远程账单值（本地与远程不齐时用于校正）
          </span>
        </div>
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
        <Button type="button" :disabled="remoteChecking || refreshingRemote" @click="confirmRemoteRefresh">
          <RiLoader4Line v-if="refreshingRemote" class="animate-spin" size="16" />
          确认刷新
        </Button>
        <Button type="button" variant="outline" @click="remoteDialogOpen = false">取消</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>

<style>
/* 拖拽中的卡片样式：
 * sortablejs 拖拽时把原节点从 grid 容器抽到 body 下、加 position:absolute + 内联 width/height，
 * 破坏 Tailwind class 的 grid/flex 上下文（脱列、flex 塌陷、边框/背景色丢失）。
 * 这里用 !important 强制恢复卡片基础视觉，再叠加倾斜+阴影。颜色走项目 token 保持一致。 */
.volc-dragging {
  /* 基础视觉重置 */
  display: flex !important;
  flex-direction: column !important;
  gap: 0.375rem !important;
  padding: 0.75rem !important;
  border: 1px solid hsl(var(--border)) !important;
  border-radius: 0.375rem !important;
  background-color: hsl(var(--background)) !important;
  color: hsl(var(--foreground)) !important;
  box-sizing: border-box !important;
  width: 240px !important;
  /* 拖拽增强 */
  transform: rotate(2deg) scale(1.03) !important;
  box-shadow: 0 20px 25px -5px rgb(0 0 0 / 0.18), 0 8px 10px -6px rgb(0 0 0 / 0.1) !important;
  cursor: grabbing !important;
  opacity: 1 !important;
  z-index: 9999 !important;
}
</style>
