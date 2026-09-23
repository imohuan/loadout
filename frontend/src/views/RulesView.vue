<script setup lang="ts">
import { computed, onBeforeUnmount, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiCheckLine,
  RiDeleteBinLine,
  RiEditLine,
  RiFlaskLine,
  RiRefreshLine,
  RiFilter3Line,
  RiHistoryLine,
  RiDownload2Line,
  RiSparklingLine,
  RiLoader4Line,
} from '@remixicon/vue'
import {
  createFailureRule,
  deleteFailureRule,
  getProviderFrameworks,
  listFailureRules,
  listRuleDecisions,
  patchFailureRule,
  restoreDefaultFailureRules,
  updateFailureRule,
  verifyFailureRule,
  type FailureRule,
  type RuleDecision,
  type RuleEvidence,
  type RuleInput,
} from '@/lib/failureRules'
import { api } from '@/lib/api'
import { formatDateTimeCN } from '@/lib/format'
import TargetModelPicker from '@/components/TargetModelPicker.vue'
import AxTable from '@/components/ui/AxTable.vue'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import HoverTextCard from '@/components/ui/HoverTextCard.vue'
import { useConfirm } from '@/composables/useConfirm'
import {
  authorRuleFromSample,
  deleteRuleSample,
  importRuleSamples,
  listRuleSamples,
  replayRuleSamples,
  type AuthorSession,
  type RuleSample,
  type SampleReplayResult,
  getRuleAuthorSession,
} from '@/lib/ruleSamples'

const { confirmDialog } = useConfirm()

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
  { value: '__none__', label: '不适用' },
  { value: 'never', label: '永久禁用' },
  { value: 'daily', label: '每日恢复' },
  { value: 'fixed', label: '定时恢复' },
]
const FIELDS = [
  { value: 'status_code', label: '状态码' },
  { value: 'body_code', label: '业务码' },
  { value: 'message_text', label: '错误文案' },
]
const OPS = [
  { value: 'contains', label: '包含' },
  { value: 'not_contains', label: '不包含' },
  { value: 'eq', label: '等于' },
  { value: 'regex', label: '正则' },
]
// FIELD_OPS 每个字段允许的操作。
//
// 以前这个下拉对所有字段都列同样的操作，于是很容易配出「配了却永远不命中」的组合
// （比如状态码 + 正则）。这里按字段收窄，从源头避免死组合。
//
// 「正则」只挂在错误文案上——旧的独立「正则」字段（message_regex）在界面上
// 合并成了「错误文案 + 正则」这一种更好懂的写法；后端仍认 message_regex，
// 打开旧规则时 frontend 会把它规范化成新写法，不会丢数据。
const FIELD_OPS: Record<string, string[]> = {
  status_code: ['eq'],
  body_code: ['eq'],
  message_text: ['contains', 'not_contains', 'eq', 'regex'],
  message_regex: ['regex'], // 兼容旧数据（界面上不再出现）
}
function opsFor(field: string) {
  const allowed = FIELD_OPS[field] ?? ['contains']
  return OPS.filter((o) => allowed.includes(o.value))
}
// onFieldChange 换字段时把不合法/无意义的操作自动纠正成该字段的第一个合法操作，
// 否则用户会看到「字段改了、操作还停在旧值」，保存下来就是一条不生效的规则。
function onFieldChange(cond: { field: string; op: string }) {
  const allowed = FIELD_OPS[cond.field] ?? []
  if (!allowed.includes(cond.op)) cond.op = allowed[0] ?? 'contains'
}
const SCOPE_MODES = [
  { value: '__all__', label: '全部平台' },
  { value: 'urls', label: '指定平台' },
  { value: 'framework', label: '按框架' },
]

// 配色对齐项目规范（RouteLogTable 同款 tone 写法）：
//   red=破坏性禁用 / amber=冷却等待 / blue=切换重试 / slate=忽略，emerald=恢复成功
const VERDICT_TONES: Record<string, string> = {
  disable_key: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  disable_model: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  disable_provider: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  cooldown: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  ignore: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
  retry_same: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  switch_next: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
}
function verdictTone(v?: string) {
  return VERDICT_TONES[v || ''] || ''
}

// 恢复策略配色：永久禁用=red（不可逆）/ 每日=amber / 定时=blue / 不适用=slate
const RECOVER_TONES: Record<string, string> = {
  never: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  daily: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  fixed: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
}
function recoverTone(recover?: string) {
  return RECOVER_TONES[recover || ''] || 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20'
}

// 状态码配色：2xx=emerald 成功 / 4xx=amber 客户端 / 5xx=red 服务端
function statusTone(code?: number) {
  if (!code) return 'text-muted-foreground'
  if (code < 300) return 'text-emerald-600 dark:text-emerald-400'
  if (code < 500) return 'text-amber-600 dark:text-amber-400'
  return 'text-red-600 dark:text-red-400'
}

const VERDICT_LABELS: Record<string, string> = Object.fromEntries(VERDICTS.map((v) => [v.value, v.label]))

const tab = ref<'rules' | 'logs' | 'samples'>('rules')
const rules = ref<FailureRule[]>([])
const decisions = ref<RuleDecision[]>([])
const loading = ref(false)

// ===== 过滤/搜索（参考转发日志页的筛选约定）=====
const search = ref('')
const filterSource = ref('__all__') // '' 全部 | manual | ai_draft | ai_confirmed
const filterVerdict = ref('__all__') // '' 全部 | verdict
const filterEnabled = ref('__all__') // '' 全部 | on | off
const logsSearch = ref('')
const logsPlatform = ref('__all__')

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
  return decisions.value.filter((d) => {
    if (logsPlatform.value !== '__all__' && d.provider_base_url !== logsPlatform.value) return false
    if (!q) return true
    const hay =
      `${d.model} ${d.channel_name} ${d.matched_rule_name} ${d.verdict} ${d.error_excerpt} ${platformLabel(d.provider_base_url)}`.toLowerCase()
    return hay.includes(q)
  })
})

// 日志里出现过的平台（按 base_url 去重），用于筛选下拉。
const logPlatforms = computed(() => {
  const seen = new Set<string>()
  const out: Array<{ base_url: string; name: string }> = []
  for (const d of decisions.value) {
    const u = d.provider_base_url
    if (!u || seen.has(u)) continue
    seen.add(u)
    out.push({ base_url: u, name: platformLabel(u) })
  }
  return out
})

// 平台配色：按 base_url 稳定散列到 6 色，同一平台始终同色（便于扫读区分）。
const PLATFORM_TONES = [
  'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20',
  'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  'bg-rose-500/15 text-rose-700 dark:text-rose-300 border-rose-500/20',
  'bg-cyan-500/15 text-cyan-700 dark:text-cyan-300 border-cyan-500/20',
]
function platformTone(url?: string) {
  if (!url) return PLATFORM_TONES[0]
  let h = 0
  for (let i = 0; i < url.length; i++) h = (h * 31 + url.charCodeAt(i)) >>> 0
  return PLATFORM_TONES[h % PLATFORM_TONES.length]
}

// platformLabel base_url → 渠道组名（展示用；找不到则回退显示域名）。
function platformLabel(url?: string) {
  if (!url) return '—'
  const hit = platforms.value.find((p) => p.base_url === url)
  if (hit) return hit.name || url
  try {
    return new URL(url).host
  } catch {
    return url
  }
}

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

// Select 组件不允许空字符串选项值（reka-ui 把 "" 当"未选中"），故用哨兵值，
// 提交前再映射回后端需要的空串。
const scopeModeProxy = computed({
  get: () => form.value.scope_mode || '__all__',
  set: (v: string) => {
    form.value.scope_mode = v === '__all__' ? '' : v
  },
})
const recoverProxy = computed({
  get: () => form.value.action.recover || '__none__',
  set: (v: string) => {
    form.value.action.recover = v === '__none__' ? '' : v
  },
})

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

function switchTab(t: 'rules' | 'logs' | 'samples') {
  tab.value = t
  load()
  if (t === 'samples') {
    void loadSamples()
  }
}

// ===== 样本回放（「回撤」）=====
// 流程：导入历史失败 → 批量回放（规则匹配）→ 表格看通过/未命中/不一致
//      → 对不一致的样本点「AI 生成规则」→ 产出草稿待人工确认。
const samples = ref<RuleSample[]>([])
const replayMap = ref<Record<string, SampleReplayResult>>({})
const replaySummary = ref<{ matched: number; unmatched: number; confirmedOk: number; confirmedTotal: number } | null>(null)
const samplesLoading = ref(false)
const replaying = ref(false)
const importing = ref(false)
const samplesSearch = ref('')
const samplesFilter = ref<'all' | 'matched' | 'unmatched' | 'inconsistent'>('all')
// 每条样本的 AI 生成会话（sampleId → 会话），表格里显示轮次进度。
const authorSessions = ref<Record<string, AuthorSession>>({})
const authorTimers = new Map<string, number>()

// 离开页面时清掉所有生成进度轮询：AI 生成是后台任务，最长可跑 10 分钟，
// 用户切走后定时器若继续跑会一直发请求（内存与网络都白耗）。
onBeforeUnmount(() => {
  for (const t of authorTimers.values()) window.clearInterval(t)
  authorTimers.clear()
})

async function loadSamples() {
  samplesLoading.value = true
  try {
    // 与后端「回放全部」上限保持一致（2000），避免列表显示 500 条、
    // 实际回放范围却与用户预期不符（早期两处口径不同）。
    samples.value = (await listRuleSamples({ limit: 2000 })) ?? []
  } catch (e) {
    toast.error(String(e))
  } finally {
    samplesLoading.value = false
  }
}

async function importSamples() {
  importing.value = true
  try {
    // limit=0 表示「尽可能多」；后端用 maxImportLimit 兜底并回带 truncated。
    const res = await importRuleSamples(0)
    // 关键：历史失败里绝大多数是同一类错误（实测 605 条 → 8 条样本），
    // 只说「导入 8 条」用户会以为漏导了。把「扫描 → 并库 → 新增」讲清楚。
    //
    // 措辞要区分两种 merged：
    //   - 本次新增 > 0：说明确实把一批同类失败收敛成了少量新样本；
    //   - 本次新增 = 0：说明扫到的都已在库里（重复点导入就是这种），
    //     不能说「合并了 605 条」——那是「全部已在库中」的意思。
    if (res.imported === 0 && res.merged > 0) {
      toast.info(`已扫描 ${res.scanned} 条历史失败，均已在样本库中（库内共 ${res.total} 条）`)
    } else {
      const parts = [`已扫描 ${res.scanned} 条历史失败`]
      if (res.merged > 0) parts.push(`其中 ${res.merged} 条同类已合并`)
      parts.push(`新增 ${res.imported} 条样本（库内共 ${res.total} 条）`)
      toast.success(parts.join('，'))
    }
    if (res.truncated) {
      toast.warning(`历史失败超过 ${res.limit} 条，本次只扫描了最新 ${res.limit} 条`)
    }
    await loadSamples()
  } catch (e) {
    toast.error(String(e))
  } finally {
    importing.value = false
  }
}

async function runReplay() {
  replaying.value = true
  try {
    const sum = await replayRuleSamples([])
    const map: Record<string, SampleReplayResult> = {}
    for (const r of sum.results ?? []) map[r.sample_id] = r
    replayMap.value = map
    replaySummary.value = {
      matched: sum.matched,
      unmatched: sum.unmatched,
      confirmedOk: sum.confirmed_ok,
      confirmedTotal: sum.confirmed_total,
    }
    toast.success(
      `回放完成：共 ${sum.total} 条，命中 ${sum.matched}，未命中 ${sum.unmatched}` +
        (sum.confirmed_total ? `，预期一致 ${sum.confirmed_ok}/${sum.confirmed_total}` : ''),
    )
  } catch (e) {
    toast.error(String(e))
  } finally {
    replaying.value = false
  }
}

// replayOf 该样本的回放结果（未回放返回 null）。
function replayOf(id: string): SampleReplayResult | null {
  return replayMap.value[id] ?? null
}

// sampleTone 结果态：不一致=红（预期≠实际）；未命中=琥珀；命中=绿。
function sampleResultTone(id: string): string {
  const r = replayOf(id)
  if (!r) return 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20'
  if (r.expected && !r.expected_ok) return 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20'
  if (!r.matched_rule_id) return 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20'
  return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20'
}

function sampleResultLabel(id: string): string {
  const r = replayOf(id)
  if (!r) return '未回放'
  if (r.expected && !r.expected_ok) return `不一致（预期 ${VERDICT_LABELS[r.expected] ?? r.expected}）`
  if (!r.matched_rule_id) return '未命中规则'
  return `已匹配 · ${VERDICT_LABELS[r.verdict] ?? r.verdict}`
}

const filteredSamples = computed(() => {
  const q = samplesSearch.value.trim().toLowerCase()
  return samples.value.filter((s) => {
    if (q) {
      const hay = `${s.model} ${s.status_code} ${s.body_code} ${s.message} ${s.provider_base_url}`.toLowerCase()
      if (!hay.includes(q)) return false
    }
    if (samplesFilter.value === 'all') return true
    const r = replayOf(s.id)
    if (!r) return false
    if (samplesFilter.value === 'matched') return !!r.matched_rule_id
    if (samplesFilter.value === 'unmatched') return !r.matched_rule_id
    return !!r.expected && !r.expected_ok
  })
})

async function removeSample(s: RuleSample) {
  const ok = await confirmDialog({ title: '删除这条样本？', description: '仅从样本库移除，不影响规则与运行状态。' })
  if (!ok) return
  try {
    await deleteRuleSample(s.id)
    samples.value = samples.value.filter((x) => x.id !== s.id)
    // 同步清掉这条样本的回放结果与生成会话，否则筛选/计数会算进已删除的行。
    const nextReplay = { ...replayMap.value }
    delete nextReplay[s.id]
    replayMap.value = nextReplay
    const nextSessions = { ...authorSessions.value }
    delete nextSessions[s.id]
    authorSessions.value = nextSessions
    const timer = authorTimers.get(s.id)
    if (timer) {
      window.clearInterval(timer)
      authorTimers.delete(s.id)
    }
  } catch (e) {
    toast.error(String(e))
  }
}

// authorForSample 让 AI 对该样本「多轮」生成规则；轮询会话进度。
async function authorForSample(s: RuleSample) {
  try {
    const sess = await authorRuleFromSample(s.id)
    authorSessions.value = { ...authorSessions.value, [s.id]: sess }
    toast.success('AI 生成已启动（进度见表格）')
    pollAuthor(s.id, sess.id)
  } catch (e) {
    toast.error(String(e))
  }
}

function pollAuthor(sampleId: string, sessionId: string) {
  const existing = authorTimers.get(sampleId)
  if (existing) window.clearInterval(existing)
  const timer = window.setInterval(async () => {
    try {
      const sess = await getRuleAuthorSession(sessionId)
      authorSessions.value = { ...authorSessions.value, [sampleId]: sess }
      if (sess.status !== 'running') {
        window.clearInterval(timer)
        authorTimers.delete(sampleId)
        if (sess.draft_rule_id) {
          toast.success('AI 已生成规则草稿，请在「规则列表」确认后生效')
          void load()
        } else if (sess.status === 'failed') {
          toast.error(`AI 生成失败：${sess.error ?? '未知错误'}`)
        } else if (sess.status === 'exhausted') {
          // 跑满轮次仍未自检通过：不落草稿，必须明确告知，否则用户只看到按钮
          // 文案变成「N 轮未收敛」却不知道发生了什么。
          toast.warning(
            `AI 生成 ${sess.rounds} 轮仍未收敛（草稿无法命中该样本），已放弃；` +
              '可调整兜底模型或手动新建规则',
          )
        }
      }
    } catch {
      window.clearInterval(timer)
      authorTimers.delete(sampleId)
    }
  }, 3000)
  authorTimers.set(sampleId, timer)
}

function authorText(sampleId: string): string {
  const sess = authorSessions.value[sampleId]
  if (!sess) return 'AI 生成规则'
  if (sess.status === 'running') return `生成中 · 第 ${sess.rounds || 1}/${sess.max_rounds} 轮`
  if (sess.status === 'done') return '已生成草稿'
  if (sess.status === 'exhausted') return `${sess.rounds} 轮未收敛`
  return '生成失败'
}

// canAuthor 这条样本能不能点「AI 生成规则」。
// 判据 = 已回放且没有规则命中。不能用 expected_ok 参与判断：
// 「预期 cooldown 且已确认」的样本，即使没命中任何规则，回放默认动作也是
// cooldown，expected_ok 恰好为 true——用户实测看到 3 条未命中只有 1 条有按钮，
// 另外 2 条（带预期的 502）就被这个条件挡住了，而它们恰恰最需要 AI 兜底。
function canAuthor(s: RuleSample): boolean {
  const r = replayOf(s.id)
  return !!r && !r.matched_rule_id
}

// authorTone 生成状态色：与「结果列」同一种视觉语言（胶囊标签 + 下方灰色小字）。
// 运行中=天蓝（进行中）、收敛=绿、未收敛=琥珀、失败=红。
function authorTone(sampleId: string): string {
  const status = authorSessions.value[sampleId]?.status
  if (status === 'running') return 'bg-sky-500/15 text-sky-700 dark:text-sky-300 border-sky-500/25'
  if (status === 'done') return 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/25'
  if (status === 'exhausted') return 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/25'
  if (status === 'failed') return 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/25'
  return 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20'
}

// authorDetail 胶囊标签下面那行灰色小字。结束态给出「最后一轮为什么没通过」
// 或失败原因——用户看进度时最想知道的就是 AI 卡在哪。
function authorDetail(sampleId: string): string {
  const sess = authorSessions.value[sampleId]
  if (!sess || sess.status === 'running') return ''
  if (sess.status === 'done') return '待人工确认后生效'
  if (sess.status === 'failed') return sess.error ?? '未知错误'
  const rounds = sess.round_detail ?? []
  const last = rounds[rounds.length - 1]
  return last?.note ?? '未能命中该样本'
}

// streamKindLabel 正在吐出的这一段是哪一类。
// 用户要求：不管是思考、文本还是 MCP/工具内容，都直接原样输出，
// 但要能一眼看出是哪一种（推理模型会先想一大段再给结果，不标就看不懂片段）。
function streamKindLabel(sampleId: string): string {
  const kind = authorSessions.value[sampleId]?.stream_kind
  if (kind === 'reasoning') return '思考'
  if (kind === 'content') return '文本'
  if (kind === 'tool') return '工具'
  return ''
}

// streamKindTone 思考=灰紫（旁白感）、文本=天蓝（正式输出）、工具=琥珀（外部调用）。
function streamKindTone(sampleId: string): string {
  const kind = authorSessions.value[sampleId]?.stream_kind
  if (kind === 'reasoning') return 'shrink-0 rounded bg-violet-500/15 px-1 text-violet-600 dark:text-violet-300'
  if (kind === 'content') return 'shrink-0 rounded bg-sky-500/15 px-1 text-sky-600 dark:text-sky-300'
  if (kind === 'tool') return 'shrink-0 rounded bg-amber-500/15 px-1 text-amber-600 dark:text-amber-300'
  return ''
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
  // 规范化旧写法：老规则里的 message_regex 字段合并成「错误文案 + 正则」，
  // 否则界面上字段下拉没有这一项、会显示成空，用户一保存就丢条件。
  const normalizedMatch: RuleInput['match'] = JSON.parse(JSON.stringify(rule.match))
  for (const kind of ['any', 'all'] as const) {
    for (const cond of normalizedMatch[kind] ?? []) {
      if (cond.field === 'message_regex') {
        cond.field = 'message_text'
        cond.op = 'regex'
      }
    }
  }
  form.value = {
    name: rule.name,
    priority: rule.priority,
    provider_base_url: rule.provider_base_url,
    scope_mode: rule.scope_mode ?? '',
    provider_base_urls: rule.provider_base_urls ?? [],
    provider_framework: rule.provider_framework ?? '',
    model: rule.model,
    match: normalizedMatch,
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

// restoreDefaults 一键把内置默认规则还原成出厂状态（含被删掉的 seed-001..014）。
// 用户自建/AI 草稿不受影响；这里是破坏性操作，先二次确认。
async function restoreDefaults() {
  const ok = await confirmDialog({
    title: '恢复默认规则？',
    description:
      '将 14 条内置默认规则还原为出厂设置（被删除的会重新加回，被改过的会改回默认）。你自建和 AI 生成的规则不受影响。',
    confirmText: '恢复',
    destructive: true,
  })
  if (!ok) return
  try {
    const res = await restoreDefaultFailureRules()
    toast.success(`已恢复 ${res.restored} 条默认规则`)
    clearFilters()
    await load()
  } catch (e) {
    toast.error(String(e))
  }
}

async function save() {
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
  const ok = await confirmDialog({
    title: `删除规则「${rule.name}」？`,
    description: '删除后该错误将不再被此规则处置（未命中时交给 AI 兜底判定）。',
    confirmText: '删除',
  })
  if (!ok) return
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
  if (s === 'ai_draft')
    return {
      text: 'AI 草稿',
      class: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
    }
  if (s === 'ai_confirmed')
    return {
      text: 'AI 已确认',
      class: 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20',
    }
  return {
    text: '内置/手动',
    class: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
  }
}

async function loadChannels() {
  try {
    channels.value = await api<Array<{ base_url?: string }>>('/api/channels')
  } catch {
    channels.value = []
  }
}

const channels = ref<
  Array<{ base_url?: string; name?: string; channel_name?: string; models?: string[] }>
>()
const allModels = computed(() => {
  const set = new Set<string>()
  for (const c of channels.value ?? []) for (const m of c.models || []) set.add(m)
  return [...set].sort()
})
const FRAMEWORK_PRESETS = ['newapi', 'one-api', 'done-hub', 'voapi']
const frameworkOptions = computed(() => {
  const set = new Set<string>([...FRAMEWORK_PRESETS, ...frameworks.value])
  return [...set].filter(Boolean).sort()
})
loadChannels()
load()

// ===== 深链：从模型状态页「查看规则」跳进来 =====
// URL 形如 /failure-rules?rule=<id>。打开后自动切到规则列表、清空筛选并打开该规则
// 的编辑器，用户可当场改恢复策略（例如把「额度用尽」从禁模型改成禁 Key）。
const route = useRoute()
const router = useRouter()

async function openRuleFromQuery() {
  const id = typeof route.query.rule === 'string' ? route.query.rule : ''
  if (!id) return
  tab.value = 'rules'
  if (!rules.value.length) {
    await load()
  }
  const rule = rules.value.find((r) => r.id === id)
  if (rule) {
    clearFilters()
    openEdit(rule)
  } else {
    toast.error(`未找到规则 ${id}（可能已被删除）`)
  }
  // 用完即清：避免用户手动关闭编辑器后又因为 query 还在被反复打开。
  const next = { ...route.query }
  delete next.rule
  router.replace({ query: next })
}

openRuleFromQuery()
</script>

<template>
  <div class="mx-auto flex h-full w-full max-w-7xl flex-col gap-4 overflow-y-auto p-4">
    <PageHeader title="失败规则" description="请求失败后的路由裁决规则；未命中规则时由 AI 兜底判定并生成草稿">
      <template #actions>
        <Button variant="outline" size="sm" @click="load">
          <RiRefreshLine size="15" class="mr-1" /> 刷新
        </Button>
        <Button variant="outline" size="sm" @click="restoreDefaults">
          <RiHistoryLine size="15" class="mr-1" /> 恢复默认规则
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
        <TabsTrigger value="samples">样本回放</TabsTrigger>
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
          <AxTable>
          <Table class="min-w-[1080px]">
            <TableHeader>
              <TableRow>
                <TableHead class="w-[220px]">名称</TableHead>
                <TableHead class="w-[180px]">作用域</TableHead>
                <TableHead>匹配条件</TableHead>
                <TableHead class="w-[84px]">判定</TableHead>
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
                  <Badge
                    v-if="rule.scope_mode"
                    variant="outline"
                    class="mt-0.5 border text-[10px]"
                    :class="rule.scope_mode === 'framework'
                      ? 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20'
                      : 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20'"
                  >
                    {{ rule.scope_mode === 'framework' ? '按框架' : '多平台' }}
                  </Badge>
                </TableCell>
                <TableCell>
                  <div class="max-w-[300px] truncate font-mono text-[11px]" :title="matchSummary(rule)">
                    {{ matchSummary(rule) }}
                  </div>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="border text-[11px]" :class="verdictTone(rule.action.verdict)">
                    {{ VERDICT_LABELS[rule.action.verdict] || rule.action.verdict }}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="border text-[11px]" :class="recoverTone(rule.action.recover)">
                    {{ recoverText(rule) }}
                  </Badge>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="border text-[11px]" :class="sourceBadge(rule).class">
                    {{ sourceBadge(rule).text }}
                  </Badge>
                </TableCell>
                <TableCell class="font-mono text-xs">{{ rule.priority }}</TableCell>
                <TableCell class="text-xs">
                  <span v-if="rule.hit_count" class="font-medium text-emerald-600 dark:text-emerald-400">
                    {{ rule.hit_count }}
                  </span>
                  <span v-else class="text-muted-foreground">—</span>
                </TableCell>
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
          </AxTable>
        </div>
      </TabsContent>

      <!-- ===== 判定日志 ===== -->
      <TabsContent value="logs" class="space-y-3">
        <div class="flex flex-wrap items-end gap-3">
          <div class="min-w-56 space-y-1">
            <Label>搜索</Label>
            <Input v-model="logsSearch" placeholder="模型 / 平台 / 规则 / 错误" />
          </div>
          <div class="min-w-40 space-y-1">
            <Label>平台</Label>
            <Select v-model="logsPlatform">
              <SelectTrigger class="w-full"><SelectValue placeholder="全部平台" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="__all__">全部平台</SelectItem>
                  <SelectItem v-for="p in logPlatforms" :key="p.base_url" :value="p.base_url">{{ p.name }}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <span class="text-muted-foreground mb-1.5 ml-auto text-xs">{{ filteredLogs.length }} / {{ decisions.length }} 条</span>
        </div>

        <LoadingBlock v-if="loading" />
        <EmptyState v-else-if="!filteredLogs.length" title="暂无判定记录" description="请求失败后的规则/AI 裁决会记录在这里" />

        <template v-else>
          <!-- 判定日志表：列宽由内容自己决定，只给「错误摘要」一个最大宽度（超出换行，
               不再被裁掉）。表头列边界可拖拽调宽，双击恢复自动宽度。 -->
          <AxTable class="rounded-lg border">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="min-w-[130px]">时间</TableHead>
                <TableHead>平台</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>状态码</TableHead>
                <TableHead class="max-w-[320px]">错误摘要</TableHead>
                <TableHead>路由依据</TableHead>
                <TableHead>判定</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="d in filteredLogs" :key="d.id">
                <TableCell class="text-muted-foreground font-mono text-[11px] whitespace-nowrap">{{ formatDateTimeCN(d.created_at) }}</TableCell>
                <TableCell>
                  <Badge variant="outline" class="border text-[11px]" :class="platformTone(d.provider_base_url)" :title="d.provider_base_url">
                    {{ platformLabel(d.provider_base_url) }}
                  </Badge>
                </TableCell>
                <TableCell class="font-mono text-xs" :title="d.channel_id">
                  {{ d.channel_name || d.channel_id || '—' }}
                </TableCell>
                <TableCell class="font-mono text-xs">{{ d.model || '—' }}</TableCell>
                <TableCell class="font-mono text-xs font-medium" :class="statusTone(d.status_code)">
                  {{ d.status_code || '—' }}
                </TableCell>
                <TableCell>
                  <HoverTextCard :text="d.error_excerpt" label="错误详情" :mono="true">
                    <div class="cursor-default truncate text-xs">{{ d.error_excerpt || '—' }}</div>
                  </HoverTextCard>
                </TableCell>
                <TableCell>
                  <Badge v-if="d.matched_rule_id" variant="outline" class="text-[11px] whitespace-normal">
                    {{ d.matched_rule_name || d.matched_rule_id }}
                  </Badge>
                  <Badge v-else-if="d.ai_model" variant="outline" class="border border-violet-500/20 bg-violet-500/15 text-[11px] text-violet-700 dark:text-violet-300">AI 判定</Badge>
                  <span v-else class="text-muted-foreground text-[11px]">默认</span>
                </TableCell>
                <TableCell>
                  <Badge variant="outline" class="border text-[11px] whitespace-normal" :class="verdictTone(d.verdict)">
                    {{ VERDICT_LABELS[d.verdict] || d.verdict }}
                  </Badge>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
          </AxTable>
        </template>
      </TabsContent>

      <!-- ===== 样本回放（「回撤」）=====
           三步：导入历史失败 → 批量回放（规则匹配）→ 表格看通过/未命中/不一致
           → 对不通过的样本点「AI 生成规则」（多轮，产出草稿待人工确认）。 -->
      <TabsContent value="samples" class="space-y-3">
        <div class="flex flex-wrap items-end gap-3">
          <div class="min-w-56 space-y-1">
            <Label>搜索</Label>
            <Input v-model="samplesSearch" placeholder="模型 / 状态码 / 错误文案" />
          </div>
          <div class="min-w-40 space-y-1">
            <Label>结果</Label>
            <Select v-model="samplesFilter">
              <SelectTrigger class="w-full"><SelectValue placeholder="全部结果" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="all">全部结果</SelectItem>
                  <SelectItem value="matched">已匹配</SelectItem>
                  <SelectItem value="unmatched">未命中</SelectItem>
                  <SelectItem value="inconsistent">不一致</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="flex shrink-0 items-center gap-2">
            <Button variant="outline" size="sm" :disabled="importing" @click="importSamples">
              <RiLoader4Line v-if="importing" class="animate-spin mr-1" size="15" />
              <RiDownload2Line v-else size="15" class="mr-1" />导入历史失败
            </Button>
            <Button variant="outline" size="sm" :disabled="replaying" @click="runReplay">
              <RiLoader4Line v-if="replaying" class="animate-spin mr-1" size="15" />
              <RiFlaskLine v-else size="15" class="mr-1" />批量回放
            </Button>
            <Button variant="outline" size="sm" @click="loadSamples">
              <RiRefreshLine size="15" class="mr-1" />刷新
            </Button>
          </div>
          <span class="text-muted-foreground mb-1.5 ml-auto text-xs tabular-nums">
            {{ filteredSamples.length }} / {{ samples.length }} 条
            <template v-if="replaySummary">
              · 命中 {{ replaySummary.matched }} · 未命中 {{ replaySummary.unmatched }}
              <template v-if="replaySummary.confirmedTotal">
                · 预期一致 {{ replaySummary.confirmedOk }}/{{ replaySummary.confirmedTotal }}
              </template>
            </template>
          </span>
        </div>

        <LoadingBlock v-if="samplesLoading" />
        <EmptyState
          v-else-if="!filteredSamples.length"
          title="暂无样本"
          description="点「导入历史失败」把判定日志沉淀成可复用的测试样本"
        />
        <template v-else>
          <!-- 样本表也走 AxTable：列宽跟随内容、表头边界可拖拽、max-w 真正生效。
               之前这里只有裸 Table，比另外两张表少了一整套能力。
               列宽只给「下限 / 上限」，不再写死像素值（写死会出现「结果列 121px
               装不下『未命中规则 no_rule_matched』、而操作列空着 190px」）。 -->
          <AxTable class="rounded-lg border">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="min-w-[72px]">来源</TableHead>
                  <TableHead class="min-w-[70px]">状态码</TableHead>
                  <TableHead class="min-w-[76px]">业务码</TableHead>
                  <TableHead class="min-w-[120px] max-w-[200px]">模型</TableHead>
                  <TableHead class="max-w-[420px]">错误摘要</TableHead>
                  <TableHead class="min-w-[150px] max-w-[240px]">匹配规则</TableHead>
                  <TableHead class="min-w-[170px]">结果</TableHead>
                  <TableHead class="min-w-[176px]">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow v-for="s in filteredSamples" :key="s.id">
                  <TableCell>
                    <Badge variant="outline" class="text-[11px]">{{ s.source === 'builtin' ? '自带' : '真实' }}</Badge>
                  </TableCell>
                  <TableCell class="font-mono text-xs" :class="statusTone(s.status_code)">{{ s.status_code || '—' }}</TableCell>
                  <TableCell class="font-mono text-xs">{{ s.body_code || '—' }}</TableCell>
                  <TableCell class="font-mono text-xs">{{ s.model || '—' }}</TableCell>
                  <TableCell class="max-w-[360px]">
                    <HoverTextCard :text="s.message" label="错误详情" :mono="true">
                      <div class="truncate text-xs text-muted-foreground">{{ s.message || '—' }}</div>
                    </HoverTextCard>
                  </TableCell>
                  <TableCell class="text-xs">
                    <span v-if="replayOf(s.id)?.matched_rule_name" class="text-foreground">
                      {{ replayOf(s.id)?.matched_rule_name }}
                    </span>
                    <span v-else class="text-muted-foreground">—</span>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" class="border text-[11px] whitespace-normal" :class="sampleResultTone(s.id)">
                      {{ sampleResultLabel(s.id) }}
                    </Badge>
                    <p v-if="replayOf(s.id)?.reason" class="text-muted-foreground mt-0.5 truncate text-[10px]">
                      {{ replayOf(s.id)?.reason }}
                    </p>
                  </TableCell>
                  <TableCell>
                    <div class="flex items-center justify-end gap-1">
                      <!-- AI 生成进度 / 结果：与「结果列」同一视觉语言 ——
                           上面一枚胶囊标签，下面一行灰色小字。
                           运行中第二行是流式尾部（打字机），结束后第二行是
                           「最后一轮为什么没通过」，让用户知道 AI 卡在哪。 -->
                      <div v-if="authorSessions[s.id]" class="flex min-w-0 flex-col items-start gap-0.5">
                        <Badge variant="outline" class="border text-[11px] whitespace-nowrap" :class="authorTone(s.id)">
                          <RiLoader4Line
                            v-if="authorSessions[s.id]?.status === 'running'"
                            class="animate-spin mr-1"
                            size="12"
                          />
                          <RiCheckLine v-else-if="authorSessions[s.id]?.status === 'done'" class="mr-1" size="12" />
                          <RiRefreshLine v-else class="mr-1" size="12" />
                          {{ authorText(s.id) }}
                        </Badge>
                        <!-- 第二行 = 正在吐出的文字（尾部 10 字）+ 它属于「思考」还是「文本」。
                             用户要求：不管是思考还是正文，直接原样输出即可，但要能看出是哪一种。 -->
                        <span
                          v-if="authorSessions[s.id]?.status === 'running'"
                          class="flex max-w-[168px] items-center gap-1 truncate text-left text-[10px]"
                          :title="authorSessions[s.id]?.stream_tail || ''"
                        >
                          <span v-if="streamKindLabel(s.id)" :class="streamKindTone(s.id)">
                            {{ streamKindLabel(s.id) }}
                          </span>
                          <span class="font-mono truncate text-muted-foreground"
                            >{{ authorSessions[s.id]?.stream_tail || '…' }}<span class="animate-pulse">▍</span></span
                          >
                        </span>
                        <span
                          v-else-if="authorDetail(s.id)"
                          class="max-w-[168px] truncate text-left text-[10px] text-muted-foreground"
                          :title="authorDetail(s.id)"
                        >{{ authorDetail(s.id) }}</span>
                      </div>
                      <!-- AI 生成规则：没有结果时是带文字的按钮，跑过之后再点就是「重新生成」 -->
                      <Button
                        v-if="canAuthor(s)"
                        :variant="authorSessions[s.id] ? 'ghost' : 'outline'"
                        :size="authorSessions[s.id] ? 'icon' : 'sm'"
                        :class="authorSessions[s.id] ? 'size-7' : ''"
                        :disabled="authorSessions[s.id]?.status === 'running'"
                        :title="authorSessions[s.id] ? '重新生成规则' : 'AI 生成规则'"
                        @click="authorForSample(s)"
                      >
                        <RiSparklingLine size="14" :class="authorSessions[s.id] ? '' : 'mr-1'" />
                        <template v-if="!authorSessions[s.id]">AI 生成规则</template>
                      </Button>
                      <Button variant="ghost" size="icon" class="size-7" title="删除样本" @click="removeSample(s)">
                        <RiDeleteBinLine size="14" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </AxTable>
        </template>
      </TabsContent>
    </Tabs>


    <!-- ===== 编辑弹窗（shadcn Dialog）===== -->
    <Dialog v-model:open="showEditor">
      <DialogContent class="max-h-[92vh] w-[calc(100vw-2rem)] sm:max-w-2xl! overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{{ editing ? '编辑规则' : '新建规则' }}</DialogTitle>
          <DialogDescription>规则按优先级从小到大匹配，首个命中生效</DialogDescription>
        </DialogHeader>

        <!-- 小窗口（<640px）全部单列排布，避免字段被挤扁；sm 起才两列。 -->
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-2">
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
          <!-- 作用范围只有「全部平台 / 指定平台 / 按框架」三种。
               旧的「单平台（兼容）」下拉已下线——要锁单个平台，用「指定平台」填一个即可
               （后端仍保留对历史 provider_base_url 数据的读取兼容，只是不再暴露这个输入口）。 -->
          <div class="col-span-2 space-y-1">
            <Label>作用范围</Label>
            <Select v-model="scopeModeProxy">
              <SelectTrigger class="w-full"><SelectValue placeholder="选择作用范围" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem v-for="m in SCOPE_MODES" :key="m.value" :value="m.value">{{ m.label }}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
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
          <!-- 条件行：小窗口换行堆叠，sm 起一行放下 -->
          <div v-for="(cond, i) in form.match[kind]" :key="i" class="flex flex-wrap items-center gap-2">
            <Select :model-value="cond.field" @update:model-value="(v: string) => { cond.field = v; onFieldChange(cond) }">
              <SelectTrigger class="w-28 sm:w-32"><SelectValue placeholder="字段" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem v-for="f in FIELDS" :key="f.value" :value="f.value">{{ f.label }}</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <Select v-model="cond.op">
              <SelectTrigger class="w-24 sm:w-28"><SelectValue placeholder="操作" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectItem v-for="o in opsFor(cond.field)" :key="o.value" :value="o.value">{{ o.label }}</SelectItem>
              </SelectContent>
            </Select>
            <Input
              v-model="cond.value"
              class="min-w-[120px] flex-1"
              :placeholder="cond.op === 'regex' ? '正则，如 额度.*用尽' : '匹配值'"
            />
            <Button variant="ghost" size="icon" class="text-red-500 hover:text-red-600" @click="removeCondition(kind, i)">
              <RiDeleteBinLine size="15" />
            </Button>
          </div>
        </div>

        <!-- 动作：小窗口单列，sm 起三列 -->
        <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
          <div class="space-y-1">
            <Label>动作</Label>
            <Select v-model="form.action.verdict">
              <SelectTrigger class="w-full"><SelectValue placeholder="选择动作" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectItem v-for="v in VERDICTS" :key="v.value" :value="v.value">{{ v.label }}</SelectItem>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1">
            <Label>恢复策略</Label>
            <Select v-model="recoverProxy">
              <SelectTrigger class="w-full"><SelectValue placeholder="选择恢复策略" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
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
          <div class="flex flex-wrap gap-2">
            <Input v-model.number="verifySample.status_code" type="number" class="w-20 sm:w-24" placeholder="状态码" />
            <Input v-model="verifySample.body_code" class="w-24 sm:w-28" placeholder="业务码" />
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
