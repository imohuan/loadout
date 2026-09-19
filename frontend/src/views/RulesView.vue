<script setup lang="ts">
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiCheckLine,
  RiDeleteBinLine,
  RiEditLine,
  RiFlaskLine,
  RiRefreshLine,
  RiFilter3Line,
} from '@remixicon/vue'
import {
  createFailureRule,
  deleteFailureRule,
  getProviderFrameworks,
  listFailureRules,
  listRuleDecisions,
  patchFailureRule,
  updateFailureRule,
  verifyFailureRule,
  type FailureRule,
  type RuleDecision,
  type RuleEvidence,
  type RuleInput,
} from '@/lib/failureRules'
import { api } from '@/lib/api'
import TargetModelPicker from '@/components/TargetModelPicker.vue'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'

const VERDICTS = [
  { value: 'disable_key', label: '禁用 Key' },
  { value: 'disable_model', label: '禁用模型' },
  { value: 'disable_provider', label: '禁用平台' },
  { value: 'cooldown', label: '冷却' },
  { value: 'ignore', label: '忽略' },
  { value: 'retry_same', label: '重试当前' },
  { value: 'switch_next', label: '换下一个' },
]
const RECOVERS = [
  { value: '', label: '不适用' },
  { value: 'never', label: '永久禁用' },
  { value: 'daily', label: '每日恢复' },
  { value: 'fixed', label: '定时恢复' },
]
const FIELDS = [
  { value: 'status_code', label: '状态码' },
  { value: 'body_code', label: '业务码' },
  { value: 'message_text', label: '错误文案' },
  { value: 'message_regex', label: '正则' },
]
const OPS = [
  { value: 'eq', label: '等于' },
  { value: 'contains', label: '包含' },
  { value: 'not_contains', label: '不包含' },
  { value: 'regex', label: '正则' },
]
const SCOPE_MODES = [
  { value: '', label: '全部平台' },
  { value: 'urls', label: '指定平台' },
  { value: 'framework', label: '按框架' },
]

const VERDICT_LABELS: Record<string, string> = Object.fromEntries(VERDICTS.map((v) => [v.value, v.label]))

const tab = ref<'rules' | 'logs'>('rules')
const rules = ref<FailureRule[]>([])
const decisions = ref<RuleDecision[]>([])
const loading = ref(false)

// ===== 过滤/搜索（参考转发日志页的筛选约定）=====
const search = ref('')
const filterSource = ref('__all__') // '' 全部 | manual | ai_draft | ai_confirmed
const filterVerdict = ref('__all__') // '' 全部 | verdict
const filterEnabled = ref('__all__') // '' 全部 | on | off
const logsSearch = ref('')

function sourceOf(rule: FailureRule): 'manual' | 'ai_draft' | 'ai_confirmed' {
  if (rule.source !== 'ai') return 'manual'
  return rule.confirmed ? 'ai_confirmed' : 'ai_draft'
}

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  return rules.value.filter((r) => {
    if (q) {
      const hay = `${r.name} ${r.model} ${r.provider_base_url} ${(r.provider_base_urls || []).join(' ')} ${r.provider_framework} ${r.id}`.toLowerCase()
      if (!hay.includes(q)) return false
    }
    if (filterSource.value !== '__all__' && sourceOf(r) !== filterSource.value) return false
    if (filterVerdict.value !== '__all__' && r.action.verdict !== filterVerdict.value) return false
    if (filterEnabled.value === 'off' && r.enabled) return false
    return true
  })
})

const filteredLogs = computed(() => {
  const q = logsSearch.value.trim().toLowerCase()
  if (!q) return decisions.value
  return decisions.value.filter((d) =>
    `${d.model} ${d.matched_rule_name} ${d.verdict} ${d.error_excerpt}`.toLowerCase().includes(q),
  )
})

function clearFilters() {
  search.value = ''
  filterSource.value = '__all__'
  filterVerdict.value = '__all__'
  filterEnabled.value = '__all__'
}

// ===== 编辑器 =====
const showEditor = ref(false)
const editing = ref<FailureRule | null>(null)
const form = ref<RuleInput>(emptyForm())
const scopeUrlsText = ref('')
const saving = ref(false)

const frameworks = ref<string[]>([])
const platforms = ref<Array<{ base_url: string; name: string; framework: string }>>([])

function emptyForm(): RuleInput {
  return {
    name: '',
    priority: 100,
    provider_base_url: '',
    scope_mode: '',
    provider_base_urls: [],
    provider_framework: '',
    model: '',
    match: { any: [{ field: 'status_code', op: 'eq', value: 429 }] },
    action: { verdict: 'cooldown', recover: 'fixed', cooldown_seconds: 120 },
  }
}

async function load() {
  loading.value = true
  try {
    rules.value = await listFailureRules()
    const info = await getProviderFrameworks()
    frameworks.value = info.frameworks ?? []
    platforms.value = info.platforms ?? []
    if (tab.value === 'logs') {
      decisions.value = (await listRuleDecisions(100)) ?? []
    }
  } catch (e) {
    toast.error(String(e))
  } finally {
    loading.value = false
  }
}

function switchTab(t: 'rules' | 'logs') {
  tab.value = t
  load()
}

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  scopeUrlsText.value = ''
  showEditor.value = true
}

function openEdit(rule: FailureRule) {
  editing.value = rule
  scopeUrlsText.value = (rule.provider_base_urls ?? []).join(', ')
  form.value = {
    name: rule.name,
    priority: rule.priority,
    provider_base_url: rule.provider_base_url,
    scope_mode: rule.scope_mode ?? '',
    provider_base_urls: rule.provider_base_urls ?? [],
    provider_framework: rule.provider_framework ?? '',
    model: rule.model,
    match: JSON.parse(JSON.stringify(rule.match)),
    action: JSON.parse(JSON.stringify(rule.action)),
  }
  showEditor.value = true
}

function addCondition(kind: 'any' | 'all') {
  const list = (form.value.match[kind] ??= [])
  list.push({ field: 'message_text', op: 'contains', value: '' })
}

function removeCondition(kind: 'any' | 'all', idx: number) {
  const list = form.value.match[kind]
  if (list) list.splice(idx, 1)
}

async function save() {
  if (form.value.provider_base_url === '__all__') form.value.provider_base_url = ''
  if (!form.value.name.trim()) {
    toast.error('规则名不能为空')
    return
  }
  if (form.value.scope_mode === 'urls') {
    form.value.provider_base_urls = scopeUrlsText.value
      .split(/[\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean)
    if (!form.value.provider_base_urls.length) {
      toast.error('请至少填写一个平台地址')
      return
    }
  }
  if (form.value.scope_mode === 'framework' && !form.value.provider_framework) {
    toast.error('请选择框架')
    return
  }
  saving.value = true
  try {
    if (editing.value) {
      await updateFailureRule(editing.value.id, form.value)
      toast.success('规则已更新')
    } else {
      await createFailureRule(form.value)
      toast.success('规则已创建')
    }
    showEditor.value = false
    load()
  } catch (e) {
    toast.error(String(e))
  } finally {
    saving.value = false
  }
}

async function toggle(rule: FailureRule) {
  try {
    await patchFailureRule(rule.id, { enabled: !rule.enabled })
    rule.enabled = !rule.enabled
  } catch (e) {
    toast.error(String(e))
  }
}

async function confirmDraft(rule: FailureRule) {
  try {
    await patchFailureRule(rule.id, { confirm: true })
    toast.success('规则已确认生效')
    load()
  } catch (e) {
    toast.error(String(e))
  }
}

async function remove(rule: FailureRule) {
  if (!window.confirm(`确定删除规则「${rule.name}」？`)) return
  try {
    await deleteFailureRule(rule.id)
    toast.success('已删除')
    load()
  } catch (e) {
    toast.error(String(e))
  }
}

// ===== 样本校验 =====
const verifySample = ref<RuleEvidence>({ status_code: 429, message: '' })
const verifyHit = ref<boolean | null>(null)
const verifying = ref(false)
async function runVerify() {
  verifyHit.value = null
  verifying.value = true
  try {
    const res = await verifyFailureRule(form.value as Partial<FailureRule>, verifySample.value)
    verifyHit.value = res.hit
  } catch (e) {
    toast.error(String(e))
  } finally {
    verifying.value = false
  }
}

// ===== 展示辅助 =====
function scopeText(rule: FailureRule) {
  const model = rule.model || '全部模型'
  if (rule.scope_mode === 'urls') {
    const urls = rule.provider_base_urls || []
    return `${urls.length} 个平台 · ${model}`
  }
  if (rule.scope_mode === 'framework') return `框架 ${rule.provider_framework} · ${model}`
  if (rule.provider_base_url) return `${rule.provider_base_url} · ${model}`
  return `全部平台 · ${model}`
}

function matchSummary(rule: FailureRule) {
  const conds = rule.match.any || rule.match.all || []
  return conds
    .map((c) => {
      const f = FIELDS.find((x) => x.value === c.field)?.label ?? c.field
      return `${f}${c.op === 'eq' ? '=' : c.op === 'not_contains' ? '≠' : '∋'} ${c.value}`
    })
    .join(' 或 ')
}

function recoverText(rule: FailureRule) {
  const a = rule.action
  if (a.recover === 'daily') return `次日 ${a.daily_reset_hour || 12} 点`
  if (a.recover === 'fixed') return `${a.cooldown_seconds || 0} 秒`
  if (a.recover === 'never') return '不恢复'
  return '—'
}

function sourceBadge(rule: FailureRule) {
  const s = sourceOf(rule)
  if (s === 'ai_draft') return { text: 'AI 草稿', class: 'border-amber-500/40 text-amber-600 dark:text-amber-400' }
  if (s === 'ai_confirmed') return { text: 'AI 已确认', class: 'border-blue-500/40 text-blue-600 dark:text-blue-400' }
  return { text: '内置/手动', class: 'text-muted-foreground' }
}

async function loadChannels() {
  try {
    channels.value = await api<Array<{ base_url?: string }>>('/api/channels')
  } catch {
    channels.value = []
  }
}

const channels = ref<Array<{ base_url?: string; name?: string; models?: string[] }>>([])
const allModels = computed(() => {
  const set = new Set<string>()
  for (const c of channels.value) for (const m of c.models || []) set.add(m)
  return [...set].sort()
})
const uniqueChannels = computed(() => {
  const seen = new Set<string>()
  const out: Array<{ base_url: string; name: string }> = []
  for (const c of channels.value) {
    const u = (c.base_url || '').replace(/\/+$/, '')
    if (!u || seen.has(u)) continue
    seen.add(u)
    out.push({ base_url: u, name: c.name || u })
  }
  return out
})
const FRAMEWORK_PRESETS = ['newapi', 'one-api', 'done-hub', 'voapi']
const frameworkOptions = computed(() => {
  const set = new Set<string>([...FRAMEWORK_PRESETS, ...frameworks.value])
  return [...set].filter(Boolean).sort()
})
loadChannels()
load()
</script>

<template>
  <div class="mx-auto flex h-full w-full max-w-7xl flex-col gap-4 overflow-y-auto p-4">
    <PageHeader title="失败规则" description="请求失败后的路由裁决规则；未命中规则时由 AI 兜底判定并生成草稿">
      <template #actions>
        <Button variant="outline" size="sm" @click="load">
          <RiRefreshLine size="15" class="mr-1" /> 刷新
        </Button>
        <Button size="sm" @click="openCreate">
          <RiAddLine size="15" class="mr-1" /> 新建规则
        </Button>
      </template>
    </PageHeader>

    <Tabs v-model="tab" class="space-y-3" @update:model-value="(v: unknown) => switchTab(v as 'rules' | 'logs')">
      <TabsList class="h-auto w-fit">
        <TabsTrigger value="rules">规则列表</TabsTrigger>
        <TabsTrigger value="logs">路由判定日志</TabsTrigger>
      </TabsList>

      <!-- ===== 规则列表 ===== -->
      <TabsContent value="rules" class="space-y-3">
        <!-- 过滤栏 -->
        <div class="flex flex-wrap items-end gap-3">
          <div class="min-w-56 space-y-1">
            <Label>搜索</Label>
            <Input v-model="search" placeholder="名称 / 模型 / 平台 / ID" />
          </div>
          <div class="min-w-36 space-y-1">
            <Label>来源</Label>
            <Select v-model="filterSource">
              <SelectTrigger class="w-full"><SelectValue placeholder="全部来源" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="__all__">全部来源</SelectItem>
                  <SelectItem value="manual">内置/手动</SelectItem>
                  <SelectItem value="ai_draft">AI 草稿</SelectItem>
                  <SelectItem value="ai_confirmed">AI 已确认</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="min-w-36 space-y-1">
            <Label>动作</Label>
            <Select v-model="filterVerdict">
              <SelectTrigger class="w-full"><SelectValue placeholder="全部动作" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="__all__">全部动作</SelectItem>
                  <SelectItem v-for="v in VERDICTS" :key="v.value" :value="v.value">{{ v.label }}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="min-w-32 space-y-1">
            <Label>状态</Label>
            <Select v-model="filterEnabled">
              <SelectTrigger class="w-full"><SelectValue placeholder="全部状态" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="__all__">全部状态</SelectItem>
                  <SelectItem value="on">已启用</SelectItem>
                  <SelectItem value="off">已停用</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <Button variant="outline" class="mb-0.5" @click="clearFilters">
            <RiFilter3Line size="15" class="mr-1" /> 重置
          </Button>
          <span class="text-muted-foreground mb-1.5 ml-auto text-xs">{{ filtered.length }} / {{ rules.length }} 条</span>
        </div>

        <LoadingBlock v-if="loading" />
        <EmptyState v-else-if="!filtered.length" title="暂无规则" description="没有匹配当前筛选条件的规则" />

        <div v-else class="overflow-x-auto rounded-lg border">
          <Table class="min-w-[1080px]">
            <TableHeader>
              <TableRow>
                <TableHead class="w-[220px]">名称</TableHead>
                <TableHead class="w-[180px]">作用域</TableHead>
                <TableHead>匹配条件</TableHead>
                <TableHead class="w-[90px]">判定</TableHead>
                <TableHead class="w-[88px]">恢复</TableHead>
                <TableHead class="w-[96px]">来源</TableHead>
                <TableHead class="w-[64px]">优先</TableHead>
                <TableHead class="w-[64px]">命中</TableHead>
                <TableHead class="w-[40px]">开</TableHead>
                <TableHead class="w-[110px] text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="rule in filtered" :key="rule.id" :class="{ 'opacity-50': !rule.enabled }">
                <TableCell>
                  <div class="font-medium">{{ rule.name }}</div>
                  <div class="text-muted-foreground font-mono text-[10px]">{{ rule.id }}</div>
                </TableCell>
                <TableCell>
                  <div class="text-xs">{{ scopeText(rule) }}</div>
                  <div v-if="rule.scope_mode" class="text-muted-foreground text-[10px]">
                    {{ rule.scope_mode === 'framework' ? '按框架' : '多平台' }}
                  </div>
                </TableCell>
                <TableCell>
                  <div class="max-w-[300px] truncate font-mono text-[11px]" :title="matchSummary(rule)">
                    {{ matchSummary(rule) }}
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="text-[11px]">{{ VERDICT_LABELS[rule.action.verdict] || rule.action.verdict }}</Badge>
                </TableCell>
                <TableCell class="text-muted-foreground text-xs">{{ recoverText(rule) }}</TableCell>
                <TableCell>
                  <span class="text-[11px]" :class="sourceBadge(rule).class">{{ sourceBadge(rule).text }}</span>
                </TableCell>
                <TableCell class="font-mono text-xs">{{ rule.priority }}</TableCell>
                <TableCell class="text-xs">{{ rule.hit_count || '—' }}</TableCell>
                <TableCell>
                  <Switch :model-value="rule.enabled" @update:model-value="toggle(rule)" />
                </TableCell>
                <TableCell class="text-right">
                  <div class="flex items-center justify-end gap-0.5">
                    <Button
                      v-if="rule.source === 'ai' && !rule.confirmed"
                      variant="ghost"
                      size="icon"
                      class="size-7"
                      title="确认 AI 草稿"
                      @click="confirmDraft(rule)"
                    >
                      <RiCheckLine size="15" class="text-green-600" />
                    </Button>
                    <Button variant="ghost" size="icon" class="size-7" title="编辑" @click="openEdit(rule)">
                      <RiEditLine size="15" />
                    </Button>
                    <Button
                      variant="ghost"
                      size="icon"
                      class="size-7 text-red-500 hover:text-red-600"
                      title="删除"
                      @click="remove(rule)"
                    >
                      <RiDeleteBinLine size="15" />
                    </Button>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </TabsContent>

      <!-- ===== 判定日志 ===== -->
      <TabsContent value="logs" class="space-y-3">
        <div class="flex items-center gap-2">
          <div class="relative">
            <RiSearchLine size="15" class="text-muted-foreground absolute top-1/2 left-2.5 -translate-y-1/2" />
            <Input v-model="logsSearch" placeholder="搜索模型 / 规则 / 错误" class="w-72 pl-8" />
          </div>
          <span class="text-muted-foreground ml-auto text-xs">{{ filteredLogs.length }} 条</span>
        </div>

        <LoadingBlock v-if="loading" />
        <EmptyState v-else-if="!filteredLogs.length" title="暂无判定记录" description="请求失败后的规则/AI 裁决会记录在这里" />

        <div v-else class="overflow-x-auto rounded-lg border">
          <Table class="min-w-[860px]">
            <TableHeader>
              <TableRow>
                <TableHead class="w-[150px]">时间</TableHead>
                <TableHead class="w-[160px]">模型</TableHead>
                <TableHead class="w-[70px]">状态码</TableHead>
                <TableHead>错误摘要</TableHead>
                <TableHead class="w-[110px]">路由依据</TableHead>
                <TableHead class="w-[90px]">判定</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="d in filteredLogs" :key="d.id">
                <TableCell class="text-muted-foreground font-mono text-[11px]">{{ d.created_at?.slice(5, 19) }}</TableCell>
                <TableCell class="font-mono text-xs">{{ d.model || '—' }}</TableCell>
                <TableCell class="text-xs">{{ d.status_code || '—' }}</TableCell>
                <TableCell>
                  <div class="max-w-[360px] truncate text-xs" :title="d.error_excerpt">{{ d.error_excerpt || '—' }}</div>
                </TableCell>
                <TableCell>
                  <Badge v-if="d.matched_rule_id" variant="outline" class="text-[11px]" :title="d.matched_rule_name">
                    {{ d.matched_rule_name || d.matched_rule_id }}
                  </Badge>
                  <Badge v-else-if="d.ai_model" class="bg-blue-500/10 text-[11px] text-blue-600 dark:text-blue-400">AI</Badge>
                  <span v-else class="text-muted-foreground text-[11px]">默认</span>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="text-[11px]">{{ VERDICT_LABELS[d.verdict] || d.verdict }}</Badge>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
      </TabsContent>
    </Tabs>

    <!-- ===== 编辑弹窗（shadcn Dialog）===== -->
    <Dialog v-model:open="showEditor">
      <DialogContent class="max-h-[92vh] w-[calc(100vw-2rem)] sm:max-w-2xl! overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{{ editing ? '编辑规则' : '新建规则' }}</DialogTitle>
          <DialogDescription>规则按优先级从小到大匹配，首个命中生效</DialogDescription>
        </DialogHeader>

        <div class="grid grid-cols-2 gap-3">
          <div class="col-span-2 space-y-1">
            <Label>规则名</Label>
            <Input v-model="form.name" placeholder="如：额度用尽（次日恢复）" />
          </div>
          <div class="space-y-1">
            <Label>优先级（小者先）</Label>
            <Input v-model.number="form.priority" type="number" />
          </div>
          <div class="space-y-1">
            <Label>模型（空 = 全部）</Label>
            <TargetModelPicker
              v-model="form.model"
              :models="allModels"
              :multiple="false"
              allow-custom
            />
          </div>
          <div class="col-span-2 grid grid-cols-2 gap-3">
            <div class="space-y-1">
              <Label>作用范围</Label>
              <Select v-model="form.scope_mode">
                <SelectTrigger><SelectValue /></SelectTrigger>
                <SelectContent>
                  <SelectItem v-for="m in SCOPE_MODES" :key="m.value" :value="m.value">{{ m.label }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div v-if="form.scope_mode !== 'urls' && form.scope_mode !== 'framework'" class="space-y-1">
              <Label>单平台（兼容，通常留空）</Label>
              <Select v-model="form.provider_base_url">
                <SelectTrigger class="w-full"><SelectValue placeholder="全部平台" /></SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem value="__all__">全部平台</SelectItem>
                    <SelectItem v-for="c in uniqueChannels" :key="c.base_url" :value="c.base_url">{{ c.name || c.base_url }}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div v-if="form.scope_mode === 'framework'" class="col-span-2 space-y-1">
            <Label>框架（同框架平台共用此规则）</Label>
            <Select v-model="form.provider_framework">
              <SelectTrigger class="w-full"><SelectValue placeholder="选择框架" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem v-for="f in frameworkOptions" :key="f" :value="f">{{ f }}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <p class="text-muted-foreground text-xs">渠道编辑里标注了该框架的所有平台都会命中。</p>
          </div>
          <div v-if="form.scope_mode === 'urls'" class="col-span-2 space-y-1">
            <Label>平台地址（每行一个或逗号分隔）</Label>
            <Textarea v-model="scopeUrlsText" :rows="3" placeholder="https://api.a.com/v1, https://b.newapi.top/v1" />
            <div class="flex flex-wrap gap-1">
              <Badge
                v-for="pt in platforms"
                :key="pt.base_url"
                variant="outline"
                class="cursor-pointer select-none"
                @click="scopeUrlsText += (scopeUrlsText ? ', ' : '') + pt.base_url"
              >{{ pt.name || pt.base_url }}</Badge>
            </div>
          </div>
        </div>

        <!-- 匹配条件 -->
        <div v-for="kind in (['any', 'all'] as const)" :key="kind" class="space-y-2">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium">{{ kind === 'any' ? '任一命中（any）' : '全部命中（all）' }}</span>
            <Button variant="outline" size="sm" @click="addCondition(kind)">
              <RiAddLine size="12" class="mr-1" /> 加条件
            </Button>
          </div>
          <div v-for="(cond, i) in form.match[kind]" :key="i" class="flex items-center gap-2">
            <Select v-model="cond.field">
              <SelectTrigger class="w-32"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="f in FIELDS" :key="f.value" :value="f.value">{{ f.label }}</SelectItem>
              </SelectContent>
            </Select>
            <Select v-model="cond.op">
              <SelectTrigger class="w-28"><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="o in OPS" :key="o.value" :value="o.value">{{ o.label }}</SelectItem>
              </SelectContent>
            </Select>
            <Input v-model="cond.value" class="flex-1" placeholder="匹配值" />
            <Button variant="ghost" size="icon" class="text-red-500 hover:text-red-600" @click="removeCondition(kind, i)">
              <RiDeleteBinLine size="15" />
            </Button>
          </div>
        </div>

        <!-- 动作 -->
        <div class="grid grid-cols-3 gap-3">
          <div class="space-y-1">
            <Label>动作</Label>
            <Select v-model="form.action.verdict">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="v in VERDICTS" :key="v.value" :value="v.value">{{ v.label }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1">
            <Label>恢复策略</Label>
            <Select v-model="form.action.recover">
              <SelectTrigger><SelectValue /></SelectTrigger>
              <SelectContent>
                <SelectItem v-for="r in RECOVERS" :key="r.value" :value="r.value">{{ r.label }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1">
            <Label>冷却秒数</Label>
            <Input v-model.number="form.action.cooldown_seconds" type="number" :disabled="form.action.recover !== 'fixed'" />
          </div>
          <div v-if="form.action.recover === 'daily'" class="space-y-1">
            <Label>每日恢复点（小时）</Label>
            <Input v-model.number="form.action.daily_reset_hour" type="number" :min="0" :max="23" />
          </div>
        </div>

        <!-- 样本校验 -->
        <div class="rounded-lg border p-3">
          <div class="mb-2 flex items-center gap-1.5 text-sm font-medium">
            <RiFlaskLine size="14" /> 样本校验（dry-run）
          </div>
          <div class="flex gap-2">
            <Input v-model.number="verifySample.status_code" type="number" class="w-24" placeholder="状态码" />
            <Input v-model="verifySample.body_code" class="w-28" placeholder="业务码" />
            <Input v-model="verifySample.message" class="flex-1" placeholder="错误文案" />
            <Button variant="secondary" :disabled="verifying" @click="runVerify">测试</Button>
          </div>
          <p v-if="verifyHit !== null" class="mt-2 text-sm" :class="verifyHit ? 'text-green-600' : 'text-red-500'">
            {{ verifyHit ? '✓ 命中该规则' : '✗ 未命中' }}
          </p>
        </div>

        <DialogFooter>
          <Button variant="outline" @click="showEditor = false">取消</Button>
          <Button :disabled="saving" @click="save">保存</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
