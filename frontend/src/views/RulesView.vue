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
} from '@remixicon/vue'
import {
  createFailureRule,
  deleteFailureRule,
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
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'

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

const tab = ref<'rules' | 'logs'>('rules')
const rules = ref<FailureRule[]>([])
const decisions = ref<RuleDecision[]>([])
const loading = ref(false)
const search = ref('')

const showEditor = ref(false)
const editing = ref<FailureRule | null>(null)
const form = ref<RuleInput>(emptyForm())
const verifySample = ref<RuleEvidence>({ status_code: 429, message: '' })
const verifyHit = ref<boolean | null>(null)

function emptyForm(): RuleInput {
  return {
    name: '',
    priority: 100,
    provider_base_url: '',
    model: '',
    match: { any: [{ field: 'status_code', op: 'eq', value: 429 }] },
    action: { verdict: 'cooldown', recover: 'fixed', cooldown_seconds: 120 },
  }
}

async function load() {
  loading.value = true
  try {
    rules.value = await listFailureRules()
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
  if (t === 'logs') load()
}

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return rules.value
  return rules.value.filter(
    (r) => r.name.toLowerCase().includes(q) || r.model.toLowerCase().includes(q) || r.provider_base_url.toLowerCase().includes(q),
  )
})

function openCreate() {
  editing.value = null
  form.value = emptyForm()
  showEditor.value = true
}

function openEdit(rule: FailureRule) {
  editing.value = rule
  form.value = {
    name: rule.name,
    priority: rule.priority,
    provider_base_url: rule.provider_base_url,
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
  if (!form.value.name.trim()) {
    toast.error('规则名不能为空')
    return
  }
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

async function runVerify() {
  verifyHit.value = null
  try {
    const res = await verifyFailureRule(form.value as Partial<FailureRule>, verifySample.value)
    verifyHit.value = res.hit
  } catch (e) {
    toast.error(String(e))
  }
}

function sourceBadge(rule: FailureRule) {
  if (rule.source === 'ai') return rule.confirmed ? 'AI 已确认' : 'AI 草稿'
  return '内置/手动'
}

async function loadChannels() {
  try {
    channels.value = await api<Array<{ base_url?: string }>>('/api/channels')
  } catch {
    channels.value = []
  }
}

const channels = ref<Array<{ base_url?: string }>>([])
loadChannels()
load()
</script>

<template>
  <div class="mx-auto w-full max-w-6xl space-y-4 p-4">
    <PageHeader title="失败规则" description="请求失败后的路由裁决规则；未命中规则时由 AI 兜底判定并生成草稿">
      <template #actions>
        <button class="btn" @click="load"><RiRefreshLine size="15" /> 刷新</button>
        <button class="btn btn-primary" @click="openCreate"><RiAddLine size="15" /> 新建规则</button>
      </template>
    </PageHeader>

    <div class="flex gap-2">
      <button class="tab" :class="{ active: tab === 'rules' }" @click="switchTab('rules')">规则列表</button>
      <button class="tab" :class="{ active: tab === 'logs' }" @click="switchTab('logs')">AI / 规则路由日志</button>
    </div>

    <LoadingBlock v-if="loading" />

    <template v-else>
      <div v-if="tab === 'rules'" class="space-y-3">
        <input v-model="search" class="input max-w-xs" placeholder="搜索规则 / 模型 / 平台" />
        <div v-if="!filtered.length" class="rounded-lg border p-8 text-center text-muted-foreground">暂无规则</div>
        <div v-for="rule in filtered" :key="rule.id" class="rounded-lg border p-4">
          <div class="flex items-center gap-3">
            <input type="checkbox" :checked="rule.enabled" @change="toggle(rule)" />
            <span class="font-medium">{{ rule.name }}</span>
            <span class="badge" :class="rule.source === 'ai' ? 'badge-ai' : ''">{{ sourceBadge(rule) }}</span>
            <span class="badge">P{{ rule.priority }}</span>
            <span v-if="rule.hit_count" class="badge">命中 {{ rule.hit_count }}</span>
            <span class="ml-auto text-sm text-muted-foreground">{{ rule.action.verdict }}</span>
            <button v-if="rule.source === 'ai' && !rule.confirmed" class="btn" @click="confirmDraft(rule)"><RiCheckLine size="14" /> 确认</button>
            <button class="btn" @click="openEdit(rule)"><RiEditLine size="14" /> 编辑</button>
            <button class="btn btn-danger" @click="remove(rule)"><RiDeleteBinLine size="14" /></button>
          </div>
          <div class="mt-2 text-sm text-muted-foreground">
            作用域：{{ rule.provider_base_url || '全部平台' }} / {{ rule.model || '全部模型' }}
            · 匹配：{{ (rule.match.any || rule.match.all || []).length }} 条件
          </div>
        </div>
      </div>

      <div v-else class="space-y-2">
        <div v-if="!decisions.length" class="rounded-lg border p-8 text-center text-muted-foreground">暂无判定记录</div>
        <div v-for="d in decisions" :key="d.id" class="rounded-lg border p-3 text-sm">
          <div class="flex items-center gap-2">
            <span class="badge" :class="d.matched_rule_id ? '' : 'badge-ai'">{{ d.matched_rule_id ? '规则路由' : 'AI 路由' }}</span>
            <span class="font-medium">{{ d.matched_rule_name || d.ai_model || '默认' }}</span>
            <span class="badge">{{ d.verdict }}</span>
            <span class="ml-auto text-xs text-muted-foreground">{{ d.created_at }}</span>
          </div>
          <div class="mt-1 text-muted-foreground">
            {{ d.model }} · HTTP {{ d.status_code || '-' }} · {{ d.error_excerpt }}
          </div>
        </div>
      </div>
    </template>

    <!-- 编辑弹窗 -->
    <div v-if="showEditor" class="fixed inset-0 z-50 flex items-center justify-center bg-black/40 p-4" @click.self="showEditor = false">
      <div class="max-h-[90vh] w-full max-w-2xl overflow-auto rounded-xl bg-background p-5 shadow-xl">
        <h3 class="mb-4 text-lg font-semibold">{{ editing ? '编辑规则' : '新建规则' }}</h3>
        <div class="grid grid-cols-2 gap-3">
          <label class="col-span-2 block text-sm">规则名 <input v-model="form.name" class="input w-full" /></label>
          <label class="block text-sm">优先级（小者先）<input v-model.number="form.priority" type="number" class="input w-full" /></label>
          <label class="block text-sm">模型（空 = 全部）<input v-model="form.model" class="input w-full" /></label>
          <label class="col-span-2 block text-sm">
            平台 base_url（空 = 全部）
            <input v-model="form.provider_base_url" list="channel-urls" class="input w-full" />
            <datalist id="channel-urls">
              <option v-for="c in channels" :key="c.base_url" :value="c.base_url" />
            </datalist>
          </label>
        </div>

        <div v-for="kind in (['any', 'all'] as const)" :key="kind" class="mt-4">
          <div class="mb-1 flex items-center gap-2 text-sm font-medium">
            {{ kind === 'any' ? '任一命中（any）' : '全部命中（all）' }}
            <button class="btn btn-sm" @click="addCondition(kind)"><RiAddLine size="12" /> 加条件</button>
          </div>
          <div v-for="(cond, i) in form.match[kind]" :key="i" class="mb-2 flex gap-2">
            <select v-model="cond.field" class="input">
              <option v-for="f in FIELDS" :key="f.value" :value="f.value">{{ f.label }}</option>
            </select>
            <select v-model="cond.op" class="input">
              <option v-for="o in OPS" :key="o.value" :value="o.value">{{ o.label }}</option>
            </select>
            <input v-model="cond.value" class="input flex-1" placeholder="匹配值" />
            <button class="btn btn-danger" @click="removeCondition(kind, i)"><RiDeleteBinLine size="14" /></button>
          </div>
        </div>

        <div class="mt-4 grid grid-cols-3 gap-3">
          <label class="block text-sm">动作
            <select v-model="form.action.verdict" class="input w-full">
              <option v-for="v in VERDICTS" :key="v.value" :value="v.value">{{ v.label }}</option>
            </select>
          </label>
          <label class="block text-sm">恢复策略
            <select v-model="form.action.recover" class="input w-full">
              <option v-for="r in RECOVERS" :key="r.value" :value="r.value">{{ r.label }}</option>
            </select>
          </label>
          <label class="block text-sm">冷却秒数
            <input v-model.number="form.action.cooldown_seconds" type="number" class="input w-full" :disabled="form.action.recover !== 'fixed'" />
          </label>
          <label v-if="form.action.recover === 'daily'" class="block text-sm">每日恢复点（小时）
            <input v-model.number="form.action.daily_reset_hour" type="number" min="0" max="23" class="input w-full" />
          </label>
        </div>

        <!-- 样本校验 -->
        <div class="mt-4 rounded-lg border p-3">
          <div class="mb-2 flex items-center gap-2 text-sm font-medium"><RiFlaskLine size="14" /> 样本校验</div>
          <div class="flex gap-2">
            <input v-model.number="verifySample.status_code" type="number" class="input w-28" placeholder="状态码" />
            <input v-model="verifySample.body_code" class="input w-28" placeholder="业务码" />
            <input v-model="verifySample.message" class="input flex-1" placeholder="错误文案" />
            <button class="btn btn-primary" @click="runVerify">测试</button>
          </div>
          <div v-if="verifyHit !== null" class="mt-2 text-sm" :class="verifyHit ? 'text-green-600' : 'text-red-500'">
            {{ verifyHit ? '✓ 命中该规则' : '✗ 未命中' }}
          </div>
        </div>

        <div class="mt-5 flex justify-end gap-2">
          <button class="btn" @click="showEditor = false">取消</button>
          <button class="btn btn-primary" @click="save">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>
