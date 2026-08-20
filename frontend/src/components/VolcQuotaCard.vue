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
import type { Channel, VolcQuotaConfig, VolcQuotaConfigDetails } from '@/lib/types'
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
const refreshingOne = ref('') // 正在刷新的 channel_id
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
/** 模型是否耗尽：本地余额扣到 0（有本地基准）或 billing 快照 exhausted */
function isExhausted(model: VolcQuotaConfigDetails['models'][number]) {
  return model.status === 'exhausted' || (hasLocal(model) && model.local_remaining <= 0)
}
function exhaustedCount(item: VolcQuotaConfigDetails) {
  return item.models.filter((m) => isExhausted(m)).length
}
/** 本地递减是否生效：initial_total>0 表示至少成功拉取过一次账单，可作本地基准 */
function hasLocal(model: VolcQuotaConfigDetails['models'][number]) {
  return model.initial_total > 0
}
/** 进度百分比：优先本地递减余额（不依赖账单 API），无本地基准时退回账单余额 */
function percent(model: VolcQuotaConfigDetails['models'][number]) {
  if (hasLocal(model)) {
    // 本地余额 = local_remaining / initial_total，扣到 0 即显示 0%。
    const base = model.initial_total > 0 ? model.initial_total : 1
    const p = Math.round((model.local_remaining / base) * 100)
    return Math.max(0, Math.min(100, p))
  }
  if (model.total_amount <= 0) return 0
  const p = Math.round((model.available_amount / model.total_amount) * 100)
  return Math.max(0, Math.min(100, p))
}
/** 行内余额文案：本地递减优先，账单作辅助 */
function balanceText(model: VolcQuotaConfigDetails['models'][number]) {
  if (hasLocal(model)) {
    return `本地剩余 ${formatAmount(model.local_remaining)} / 初始 ${formatAmount(model.initial_total)} ${model.unit}`
  }
  return `剩余 ${formatAmount(model.available_amount)} / 共 ${formatAmount(model.total_amount)} ${model.unit}`
}
/** 底部辅助信息：本地模式展示账单/用量/同步时间，账单模式展示用量/同步时间 */
function metaText(model: VolcQuotaConfigDetails['models'][number]) {
  const synced = `同步于 ${formatTime(model.synced_at)}`
  if (hasLocal(model)) {
    return `账单剩余 ${formatAmount(model.available_amount)} · 已用 ${formatAmount(model.used_amount)} · ${synced}`
  }
  return `已用 ${formatAmount(model.used_amount)} · ${synced}`
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

// ===== 刷新 =====
async function refreshAll() {
  refreshingAll.value = true
  try {
    const result = await quota.refresh()
    applyRefreshResult(result.configs_checked, result.failed_channels, result.disabled_models)
    await loadStatus(false)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingAll.value = false
  }
}

async function refreshOne(channelId: string) {
  refreshingOne.value = channelId
  try {
    const result = await quota.refresh(channelId)
    applyRefreshResult(result.configs_checked, result.failed_channels, result.disabled_models)
    await loadStatus(false)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '刷新失败')
  } finally {
    refreshingOne.value = ''
  }
}

function applyRefreshResult(
  checked: number,
  failed?: string[],
  disabled?: string[],
) {
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
const form = reactive<{ channel_id: string; access_key: string; secret_key: string; enabled: boolean; force_block: boolean }>({
  channel_id: '',
  access_key: '',
  secret_key: '',
  enabled: true,
  force_block: false,
})

function resetForm() {
  Object.assign(form, { channel_id: '', access_key: '', secret_key: '', enabled: true, force_block: false })
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
    await quota.save(configs.value.filter((item) => item.config.channel_id !== channelId).map((item) => item.config))
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
        为方舟渠道 Key 配置 AK/SK（火山引擎控制台访问授权），自动查询免费模型额度；每次请求按 total_tokens 本地扣减余额（不依赖账单接口，账单 429 也能拦截）；额度耗尽后自动禁用该模型（冷却至次日 0 点，错误信息「免费额度 失效」）。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-3">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <div class="text-xs text-muted-foreground">
          共 {{ configs.length }} 个 Key 配置<span v-if="loaded">，额度每天 0 点重置</span>
        </div>
        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            :disabled="refreshingAll || refreshingOne !== ''"
            @click="refreshAll"
          >
            <RiLoader4Line v-if="refreshingAll" class="animate-spin" size="16" />
            <RiRefreshLine v-else size="16" />
            刷新全部
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
                {{ item.config.base_url }}<span v-if="item.config.access_key"> · AK {{ item.config.access_key.slice(0, 8) }}…</span>
              </div>
            </div>
            <div class="flex shrink-0 items-center gap-2">
              <Badge v-if="item.config.force_block" variant="destructive">
                强制关停
              </Badge>
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
                  :disabled="refreshingAll || refreshingOne !== ''"
                  @click="refreshOne(item.config.channel_id)"
                >
                  <RiLoader4Line v-if="refreshingOne === item.config.channel_id" class="animate-spin" size="16" />
                  <RiRefreshLine v-else size="16" />
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

          <!-- 展开行：模型额度进度条 -->
          <div
            v-if="isExpanded(item.config.channel_id)"
            class="space-y-3 border-t bg-muted/30 px-4 py-4"
          >
            <div v-if="item.models.length" class="space-y-3">
              <div v-for="m in item.models" :key="m.model" class="space-y-1">
                <div class="flex items-center justify-between gap-2">
                  <span class="truncate text-sm font-medium">{{ m.model }}</span>
                  <span class="shrink-0 text-xs text-muted-foreground">
                    {{ balanceText(m) }}
                  </span>
                </div>
                <Progress
                  :model-value="percent(m)"
                  :class="isExhausted(m) ? 'bg-destructive/20' : ''"
                />
                <div class="flex items-center justify-between gap-2 text-xs text-muted-foreground">
                  <span>{{ metaText(m) }}</span>
                  <Badge :variant="isExhausted(m) ? 'destructive' : 'secondary'">
                    {{ isExhausted(m) ? '已耗尽（免费额度 失效）' : '正常' }}
                  </Badge>
                </div>
              </div>
            </div>
            <EmptyState v-else title="暂无额度数据" description="点击右侧刷新按钮获取最新额度（账单有延迟，后台每 15 分钟自动刷新）。" />
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
    <DialogContent>
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
            未找到方舟渠道，请先在「渠道列表」中添加 base_url 为 https://ark.cn-beijing.volces.com/api/v3 的渠道。
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
</template>
