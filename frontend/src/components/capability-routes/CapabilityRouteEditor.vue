<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import { RiClipboardLine, RiCloseLine, RiSearchLine, RiUploadLine } from '@remixicon/vue'
import ModelChannelList from '@/components/ModelChannelList.vue'
import SensitiveWordList from '@/components/capability-routes/SensitiveWordList.vue'
import MessageInjectList from '@/components/capability-routes/MessageInjectList.vue'
import ChannelGroupPicker, { type ChannelSelection } from '@/components/ChannelGroupPicker.vue'
import TargetModelPicker from '@/components/TargetModelPicker.vue'
import {
  channelLevelSegments,
  formatChannelGroupLabel,
  formatChannelRef,
  groupSegmentsFor,
  mergeSegments,
  type ChannelGroupSegment,
} from '@/composables/useChannelRef'
import { normalizeBaseURL } from '@/composables/useChannels'
import type {
  Aggregate,
  CapabilityRoute,
  Channel,
  FieldRules,
  MessageInjection,
  ModelChannelItem,
  SensitiveReplacement,
} from '@/lib/types'

const props = defineProps<{
  route?: CapabilityRoute
  channels: Channel[]
  /** 聚合（虚拟）模型：它们不在任何渠道的真实模型清单里，需要单独并入候选。 */
  aggregates?: Aggregate[]
  pending?: boolean
}>()
const emit = defineEmits<{ save: [value: CapabilityRoute]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })

// formError 保存校验失败的提示（在弹窗底部显性展示）。
// 这些校验早前是静默 return，用户点保存毫无反应也不知原因（见 submit）。
const formError = ref('')
// footerRef 用于校验失败时把提示滚进视野：弹窗内容整体滚动，表单长时底部提示
// 可能落在视口之外，用户仍会觉得「点了没反应」。
const footerRef = ref<HTMLElement | null>(null)

// fail 记录校验失败原因并把提示滚到可见处。
function fail(message: string) {
  formError.value = message
  void nextTick(() => footerRef.value?.scrollIntoView({ block: 'nearest', behavior: 'smooth' }))
}

// 敏感词列表子组件引用：Label 行右侧的「导出 / 导入」按钮调用其暴露的方法。
type SensitiveWordListHandle = {
  exportToClipboard: () => Promise<void> | void
  importFromClipboard: () => Promise<void> | void
}
const sensitiveListRef = ref<SensitiveWordListHandle | null>(null)

// 消息注入列表子组件引用：导出/导入按钮调用其暴露的方法。
type MessageInjectListHandle = {
  exportToClipboard: () => Promise<void> | void
  importFromClipboard: () => Promise<void> | void
}
const messageInjectRef = ref<MessageInjectListHandle | null>(null)

// 能力常量与文案。
const CAP_VISION = 'vision'
const CAP_SENSITIVE = 'sensitive_filter'
const CAP_FIELD_FILTER = 'field_filter'
const CAP_MESSAGE_INJECT = 'message_inject'
const CAP_REQUEST_LOG = 'request_log'
const CAP_REQUEST_CAPTURE = 'request_capture'
const CAP_FORCE_STREAM = 'force_stream'
const CAP_KEY_RATE_LIMIT = 'key_rate_limit'

// 限流参数编辑态：模板绑定原始文本，提交时才解析为数字——避免输入过程中被受控转换吞字符。
type RateLimitForm = {
  maxRequests: string
  windowSeconds: string
  maxConcurrency: string
  onExhausted: string
}
// 默认：按次数限流（每 60 秒 20 次），并发留空 = 不限并发。
const emptyRateLimitForm = (): RateLimitForm => ({
  maxRequests: '20',
  windowSeconds: '60',
  maxConcurrency: '',
  onExhausted: 'reject',
})

// 字段过滤规则编辑态：模板用 v-model 绑定原始文本（每行一个字段路径），
// 提交时才 split/trim 解析为数组——避免输入时受控转换吞字符/丢空格。
// 注意：shadcn Textarea 是受控组件（modelValue），必须用 v-model，不能 :value/@input。
type FieldRulesKey =
  | 'request_strip'
  | 'request_keep'
  | 'request_header_strip'
  | 'response_strip'
  | 'response_keep'
  | 'response_header_strip'
const emptyFieldRulesText = (): Record<FieldRulesKey, string> => ({
  request_strip: '',
  request_keep: '',
  request_header_strip: '',
  response_strip: '',
  response_keep: '',
  response_header_strip: '',
})

const form = reactive<{
  models: string[]
  capability: string
  route: string
  viaOptions: ModelChannelItem[]
  replacements: SensitiveReplacement[]
  injections: MessageInjection[]
  fieldRulesText: Record<FieldRulesKey, string>
  rateLimit: RateLimitForm
  requestLogKeepBase64: boolean
}>({
  models: [],
  capability: CAP_VISION,
  route: 'proxy',
  viaOptions: [{ model: '', channel_id: '', channel_ids: [] }],
  replacements: [{ from: '', to: '', regex: false }],
  injections: [{ role: 'system', content: '', position: 'prepend' }],
  fieldRulesText: emptyFieldRulesText(),
  rateLimit: emptyRateLimitForm(),
  requestLogKeepBase64: false,
})

// 目标渠道选择态（由 ChannelGroupPicker 直接 v-model；空 = 全渠道生效）。
// channel_base_urls = 渠道级多选（点过渠道名，显示无括号）；channel_ids = Key 级多选（显示带括号）。
const channelSel = reactive<ChannelSelection>({
  channel_id: '',
  channel_ids: [],
  channel_base_url: '',
  channel_base_urls: [],
})

// 全部可用模型：所有渠道模型目录去重排序（目标模型与候选下拉共用）。
const allModels = computed(() => {
  const set = new Set<string>()
  for (const channel of props.channels) {
    for (const model of channel.models || []) set.add(model)
  }
  return [...set].sort()
})

watch(
  () => props.route,
  (route) => {
    Object.assign(form, {
      models: route?.models ? [...route.models] : [],
      capability: route?.capability || CAP_VISION,
      route: route?.route || 'proxy',
      viaOptions: route?.via_options?.length
        ? route.via_options.map((o) => ({
            model: o.via_model || '',
            channel_id: o.channel_id || '',
            channel_ids: o.channel_ids?.length ? o.channel_ids : [],
            channel_base_url: o.channel_base_url || '',
          }))
        : [{ model: '', channel_id: '', channel_ids: [] }],
      replacements: route?.replacements?.length
        ? route.replacements.map((r) => ({ from: r.from || '', to: r.to || '', regex: !!r.regex }))
        : [{ from: '', to: '', regex: false }],
      injections: route?.injections?.length
        ? route.injections.map((i) => ({
            role: i.role || 'system',
            content: i.content || '',
            position: i.position || 'prepend',
          }))
        : [{ role: 'system', content: '', position: 'prepend' }],
      fieldRulesText: {
        request_strip: (route?.field_rules?.request_strip || []).join('\n'),
        request_keep: (route?.field_rules?.request_keep || []).join('\n'),
        request_header_strip: (route?.field_rules?.request_header_strip || []).join('\n'),
        response_strip: (route?.field_rules?.response_strip || []).join('\n'),
        response_keep: (route?.field_rules?.response_keep || []).join('\n'),
        response_header_strip: (route?.field_rules?.response_header_strip || []).join('\n'),
      },
      rateLimit: {
        maxRequests:
          route?.rate_limit?.max_requests != null ? String(route.rate_limit.max_requests) : '20',
        windowSeconds:
          route?.rate_limit?.window_seconds != null
            ? String(route.rate_limit.window_seconds)
            : '60',
        maxConcurrency:
          route?.rate_limit?.max_concurrency != null
            ? String(route.rate_limit.max_concurrency)
            : '',
        onExhausted: route?.rate_limit?.on_exhausted || 'reject',
      },
      requestLogKeepBase64: !!route?.request_log_keep_base64,
    })
    // 老数据 `*`（通用全匹配）归一化为空 = 全渠道生效，语义一致。
    // channel_base_urls 在 CapabilityRoute 里持久化（渠道级原意图）；新版本才写入，老数据为空是预期。
    // channel_ids 单独还原：与 channel_base_urls 并存，没点过渠道名就不算选了渠道级。
    const raw = route?.channel_ids || []
    const rawBaseURLs = route?.channel_base_urls || []
    Object.assign(channelSel, {
      channel_id: '',
      channel_ids: raw.includes('*') ? [] : [...raw],
      channel_base_url: '',
      channel_base_urls: [...rawBaseURLs],
    })
  },
  { immediate: true },
)

// 能力切换时重置路由方式与列表到默认值。
function onCapabilityChange(value: string) {
  form.capability = value
  form.route = 'proxy'
  if (value === CAP_SENSITIVE) {
    form.replacements = [{ from: '', to: '', regex: false }]
  } else if (value === CAP_MESSAGE_INJECT) {
    form.injections = [{ role: 'system', content: '', position: 'prepend' }]
  } else if (value === CAP_FIELD_FILTER) {
    form.fieldRulesText = emptyFieldRulesText()
  } else if (value === CAP_KEY_RATE_LIMIT) {
    form.rateLimit = emptyRateLimitForm()
  } else {
    form.viaOptions = [{ model: '', channel_id: '', channel_ids: [] }]
  }
  // 保留 base64 只属于 request_log：切到别的能力就复位，避免残留配置写进无关路由。
  if (value !== CAP_REQUEST_LOG) form.requestLogKeepBase64 = false
}

// 把 field_rules 原始文本解析为数组（按行 split/trim，忽略空行）。
function parseFieldRules(): FieldRules {
  const parse = (key: FieldRulesKey): string[] =>
    (form.fieldRulesText[key] || '')
      .split('\n')
      .map((s) => s.trim())
      .filter(Boolean)
  return {
    request_strip: parse('request_strip'),
    request_keep: parse('request_keep'),
    request_header_strip: parse('request_header_strip'),
    response_strip: parse('response_strip'),
    response_keep: parse('response_keep'),
    response_header_strip: parse('response_header_strip'),
  }
}

// 字段过滤规则是否至少有一条配置（proxy 路由校验用）。
function hasFieldRulesText(): boolean {
  return Object.values(form.fieldRulesText).some((v) => v.trim().length > 0)
}

// parsePositiveInt 把输入框文本解析为正整数；非法（空/0/负数/非数字）返回 undefined。
function parsePositiveInt(raw: string): number | undefined {
  const n = Number.parseInt((raw || '').trim(), 10)
  if (!Number.isFinite(n) || n <= 0) return undefined
  return n
}

// rateLimitSummary 当前限流设置的一句话摘要（编辑时即时反馈，避免填错单位）。
const rateLimitSummary = computed(() => {
  const max = parsePositiveInt(form.rateLimit.maxRequests)
  const secs = parsePositiveInt(form.rateLimit.windowSeconds) ?? 60
  const conc = parsePositiveInt(form.rateLimit.maxConcurrency)
  if (!max && !conc) return '请至少填写一项：窗口内最大请求数 或 并发上限'
  const windowText =
    secs % 86400 === 0
      ? `${secs / 86400} 天`
      : secs % 3600 === 0
        ? `${secs / 3600} 小时`
        : secs % 60 === 0
          ? `${secs / 60} 分钟`
          : `${secs} 秒`
  const parts: string[] = []
  if (max) parts.push(`每 ${windowText} 每个 Key 最多 ${max} 次`)
  if (conc) parts.push(`每个 Key 同时最多 ${conc} 个请求`)
  const tail = form.rateLimit.onExhausted === 'reuse' ? '超额继续使用' : '全部超额返回 429'
  return parts.join('，') + `，${tail}`
})

// ===== 目标渠道（ChannelGroupPicker 多选；空 = 全渠道生效）=====
const channelOpen = ref(false)
// 已选 Key id 并集（候选模型过滤用）：渠道级组展开为组内所有 Key + Key 级多选。
// 注意：仅用于 candidateModels 计算候选模型；保存时 channel_ids 不展开渠道级（见 submit）。
const selectedKeyIds = computed(() => {
  const ids = new Set<string>()
  for (const bu of channelSel.channel_base_urls || []) {
    for (const c of props.channels) {
      if (normalizeBaseURL(c.base_url) === normalizeBaseURL(bu)) ids.add(c.id)
    }
  }
  for (const id of channelSel.channel_ids || []) ids.add(id)
  return [...ids]
})
const channelTriggerLabel = computed(
  () => formatChannelRef(props.channels, channelSel) || '通用（全匹配）',
)
// 已选渠道分组：渠道级段（无括号）在前 + Key 级段（带括号）在后，badge 按段渲染。
const selectedGroups = computed<ChannelGroupSegment[]>(() =>
  mergeSegments(
    channelLevelSegments(props.channels, channelSel.channel_base_urls || []),
    groupSegmentsFor(props.channels, channelSel.channel_ids || []),
  ),
)
// 移除一个展示段：渠道级段从 channel_base_urls 删；Key 级段从 channel_ids 删该组。
function removeChannelGroup(seg: ChannelGroupSegment) {
  if (seg.level) {
    channelSel.channel_base_urls = (channelSel.channel_base_urls || []).filter(
      (bu) => normalizeBaseURL(bu) !== seg.baseUrl,
    )
    return
  }
  const idsInGroup = new Set(seg.ids)
  channelSel.channel_ids = (channelSel.channel_ids || []).filter((id) => !idsInGroup.has(id))
}
// 目标模型候选：按所选渠道过滤（空 = 全渠道模型并集）。
const candidateModels = computed(() => {
  const ids = selectedKeyIds.value
  const set = new Set<string>()
  if (!ids.length) {
    for (const model of allModels.value) set.add(model)
  } else {
    for (const id of ids) {
      const ch = props.channels.find((c) => c.id === id)
      for (const model of ch?.models || []) set.add(model)
    }
  }
  // 虚拟（聚合）模型：按聚合目标实际绑定的渠道过滤；通用/空 = 全部展示。
  // 不并入 allModels，保持「渠道真实模型」这个变量的原语义，避免其他用途误用。
  for (const agg of props.aggregates || []) {
    if (agg.enabled === false || !agg.name) continue
    if (virtualModelMatchesChannels(agg, ids, channelSel.channel_base_urls || [])) {
      set.add(agg.name)
    }
  }
  return [...set].sort()
})

// virtualModelMatchesChannels 判断虚拟模型是否可能落在已选渠道上：
// 任一聚合目标命中已选 Key / 渠道组即算命中；目标完全未限定渠道时视为通用。
function virtualModelMatchesChannels(
  agg: Aggregate,
  ids: string[],
  baseURLs: string[],
): boolean {
  if (!ids.length && !baseURLs.length) return true
  const selectedBaseURLs = new Set(baseURLs.map((bu) => normalizeBaseURL(bu)))
  for (const target of agg.targets || []) {
    const targetIDs = target.channel_ids || []
    if (target.channel_id && ids.includes(target.channel_id)) return true
    if (targetIDs.some((id) => ids.includes(id))) return true
    if (target.channel_base_url && selectedBaseURLs.has(normalizeBaseURL(target.channel_base_url))) {
      return true
    }
    // 目标没写任何渠道 = 自动路由，可能落到已选渠道上。
    if (!target.channel_id && !targetIDs.length && !target.channel_base_url) return true
  }
  return false
}

// ===== 能力与路由方式选项 =====
const capabilityOptions = [
  { value: CAP_VISION, label: 'vision（视觉）' },
  { value: CAP_SENSITIVE, label: 'sensitive_filter（敏感词过滤）' },
  { value: CAP_FIELD_FILTER, label: 'field_filter（字段过滤）' },
  { value: CAP_MESSAGE_INJECT, label: 'message_inject（消息注入）' },
  { value: CAP_REQUEST_LOG, label: 'request_log（完整请求日志）' },
  { value: CAP_REQUEST_CAPTURE, label: 'request_capture（请求抓包捕获）' },
  { value: CAP_FORCE_STREAM, label: 'force_stream（强制流式·非流式客户端兼容）' },
  { value: CAP_KEY_RATE_LIMIT, label: 'key_rate_limit（Key 限流·账号轮换）' },
]
// 路由方式选项：sensitive_filter 三态里 error（命中拒绝）已废弃移除——
// 「不支持就不管他」：命中敏感词不再直接拒绝，只能替换（proxy）或透传（native）。
const routeOptions = computed(() =>
  form.capability === CAP_SENSITIVE
    ? [
        { value: 'proxy', label: '附加代理（替换）' },
        { value: 'native', label: '原生透传' },
      ]
    : [
        { value: 'proxy', label: '附加代理' },
        { value: 'native', label: '原生透传' },
      ],
)
// 路由方式的一句话说明，footer 左侧动态展示。
const routeHint = computed(() => {
  if (form.capability === CAP_SENSITIVE) {
    return {
      proxy:
        '请求体按替换规则整体过滤：敏感词被替换后再转发给目标模型；整体替换若破坏 JSON，自动降级为只替换 messages 文本。',
      native: '请求体原样透传，不做敏感词过滤（适合通配规则下的精确豁免）。',
    }[form.route]
  }
  if (form.capability === CAP_FIELD_FILTER) {
    return {
      proxy:
        '按 field_rules 配置剔除/保留请求头与请求体、剔除响应头与响应体字段（如腾讯 copilot 网关严格解析或 agent 携带上游不支持的字段）。',
      native: '请求/响应方向原样透传，不做字段过滤（适合通配规则下的精确豁免）。',
    }[form.route]
  }
  if (form.capability === CAP_REQUEST_LOG) {
    return {
      proxy:
        '记录该模型/渠道下每次请求的完整输入输出（独立库 request-log.db，脱敏后落库）；转发日志页可从该行跳转查看详情。',
      native: '不记录完整请求日志（请求体/响应体均不落库）。',
    }[form.route]
  }
  if (form.capability === CAP_REQUEST_CAPTURE) {
    return {
      proxy:
        '在请求进入中转的最前端把完整原始报文（含 Authorization 与图片 base64）打包落盘；「请求抓包捕获」页可查看、下载并生成 Node 复现脚本。',
      native: '不抓包：原始报文不落盘（适合通配规则下的精确豁免）。',
    }[form.route]
  }
  if (form.capability === CAP_FORCE_STREAM) {
    return {
      proxy:
        '该渠道/模型只接受流式时使用：客户端发非流式(stream:false)请求，网关内部转流式请求、缓冲完整段后整包按非流式 JSON 返回，客户端无感知。',
      native: '按原始方式透传，不做强制流式转换（适合已原生支持非流式的模型）。',
    }[form.route]
  }
  if (form.capability === CAP_MESSAGE_INJECT) {
    return {
      proxy:
        '按注入列表把自定义内容加到请求 messages（新增消息，或拼到原始第一条开头/结尾），再转发给目标模型。',
      native: '请求体原样透传，不做消息注入（适合通配规则下的精确豁免）。',
    }[form.route]
  }
  if (form.capability === CAP_KEY_RATE_LIMIT) {
    return {
      proxy:
        '在窗口内限制单个渠道 Key 的请求数：用满后该 Key 暂时跳过，请求自动落到同平台的另一个账号；全部账号都用满时按「超额处理」配置。',
      native: '该模型/渠道不限流（适合通配规则下的精确豁免）。',
    }[form.route]
  }
  return {
    proxy: '图片被拦截，候选视觉模型看图生成文字描述后再转发给目标模型。',
    native: '图片原样透传给目标模型自行处理（适合支持视觉的模型，或通配规则下的精确豁免）。',
  }[form.route]
})

function submit() {
  // 校验失败原因：写进 formError 并在弹窗底部显性提示。
  // 早前这些分支都是「静默 return」——按钮能点、页面毫无反应、也没有任何说明，
  // 用户完全不知道少了什么。
  // 注意：目标模型**允许为空**——空 = 全匹配（等价 `*`），与目标渠道「通用（全匹配）」
  // 同一套语义，因此这里不做非空校验（早前误加过一次，会把合法配置挡在门外）。
  formError.value = ''
  const isSensitive = form.capability === CAP_SENSITIVE
  const isFieldFilter = form.capability === CAP_FIELD_FILTER
  const isMessageInject = form.capability === CAP_MESSAGE_INJECT
  // 遗留 route="error" 数据归一化为 native（语义即「不支持就不管他」降级透传），
  // 避免编辑保存时把已废弃的 error 值写回 DB。
  if (form.route === 'error') form.route = 'native'
  if (form.route === 'proxy') {
    if (isSensitive) {
      if (!form.replacements.some((r) => r.from.trim())) {
        fail('请至少填写一条敏感词替换规则（原文不能为空）')
        return
      }
    } else if (isFieldFilter) {
      if (!hasFieldRulesText()) {
        fail('请至少填写一条字段过滤规则')
        return
      }
    } else if (isMessageInject) {
      if (!form.injections.some((i) => i.content.trim())) {
        fail('请至少填写一条消息注入内容')
        return
      }
    } else if (form.capability === CAP_REQUEST_LOG) {
      // request_log 无额外配置（脱敏开关在独立 config 表），直接放行
    } else if (form.capability === CAP_REQUEST_CAPTURE) {
      // request_capture 无额外配置（纯 on/off 能力），直接放行
    } else if (form.capability === CAP_FORCE_STREAM) {
      // force_stream 无额外配置（纯 on/off 能力），直接放行
    } else if (form.capability === CAP_KEY_RATE_LIMIT) {
      // key_rate_limit 两个维度（次数 / 并发）至少要配一个，否则规则没有意义。
      // 注意：格子填了内容但解析不出正整数（如手滑打成 "faf"）必须明确指出是哪一格，
      // 否则用户看到的就是「点保存毫无反应」，与早前那批静默 return 是同一个坑。
      const maxReqRaw = form.rateLimit.maxRequests.trim()
      const concRaw = form.rateLimit.maxConcurrency.trim()
      if (maxReqRaw && !parsePositiveInt(form.rateLimit.maxRequests)) {
        fail('「单个 Key 窗口内最大请求数」需为正整数（留空 = 不限次数）')
        return
      }
      if (concRaw && !parsePositiveInt(form.rateLimit.maxConcurrency)) {
        fail('「单个 Key 并发上限」需为正整数（留空 = 不限并发）')
        return
      }
      if (
        !parsePositiveInt(form.rateLimit.maxRequests) &&
        !parsePositiveInt(form.rateLimit.maxConcurrency)
      ) {
        fail('请至少填写一项限制：窗口内最大请求数 或 并发上限')
        return
      }
    } else if (!form.viaOptions.some((o) => o.model.trim())) {
      fail('请至少填写一个视觉候选模型')
      return
    }
  }

  const rate_limit =
    form.route !== 'native' && form.capability === CAP_KEY_RATE_LIMIT
      ? {
          // 次数维度：两个字段要么都给，要么都不给（只填一个没有意义）。
          ...(parsePositiveInt(form.rateLimit.maxRequests)
            ? {
                max_requests: parsePositiveInt(form.rateLimit.maxRequests),
                window_seconds: parsePositiveInt(form.rateLimit.windowSeconds) ?? 60,
              }
            : {}),
          // 并发维度：留空 = 不限并发。
          ...(parsePositiveInt(form.rateLimit.maxConcurrency)
            ? { max_concurrency: parsePositiveInt(form.rateLimit.maxConcurrency) }
            : {}),
          on_exhausted: form.rateLimit.onExhausted === 'reuse' ? 'reuse' : 'reject',
        }
      : undefined
  const viaOptions =
    form.route === 'proxy' && form.capability === CAP_VISION
      ? form.viaOptions
          .map((o) => ({
            via_model: o.model.trim(),
            channel_id: o.channel_id || '',
            channel_ids: o.channel_ids?.length ? o.channel_ids : undefined,
            channel_base_url: o.channel_base_url || '',
          }))
          .filter((o) => o.via_model)
      : []
  const replacements =
    form.route !== 'native' && isSensitive
      ? form.replacements
          .map((r) => ({ from: r.from.trim(), to: r.to || '', regex: !!r.regex }))
          .filter((r) => r.from)
      : []
  // field_rules：仅 field_filter 且非 native 时输出；全空则省略（后端 nil = 透传）。
  const injections =
    form.route !== 'native' && isMessageInject
      ? form.injections
          .map((i) => ({
            role: i.role || 'system',
            content: i.content.trim(),
            position: i.position || 'prepend',
          }))
          .filter((i) => i.content)
      : []
  const field_rules =
    form.route !== 'native' && isFieldFilter && hasFieldRulesText() ? parseFieldRules() : undefined
  // channel_ids 只存「显式勾选的 Key」；渠道级由 channel_base_urls 单独承载。
  // 两者互斥（同一渠道不会同时出现在两个字段），避免渲染成「workbuddy, workbuddy(workbuddy)」这类重复。
  // 后端 DecideRoute 对 channel_ids（Key 级精确匹配）+ channel_base_urls（渠道级按 base_url 匹配）取 OR，
  // 渠道级新增 Key 仍能命中，无需在前端展开。
  const channel_ids = [...(channelSel.channel_ids || [])]
  emit('save', {
    // 目标模型留空 = 全部模型：保存时直接落成 "*"，与「目标渠道」留空=通用（全匹配）同一套语义。
    models: form.models.length ? [...form.models] : ['*'],
    channel_ids,
    channel_base_urls: [...(channelSel.channel_base_urls || [])],
    capability: form.capability,
    route: form.route,
    via_options: viaOptions,
    replacements,
    field_rules,
    injections,
    rate_limit,
    // 只有 request_log 才有意义：其他能力保存时省略（omitempty 不落库）。
    request_log_keep_base64:
      form.capability === CAP_REQUEST_LOG && form.route !== 'native'
        ? form.requestLogKeepBase64
        : undefined,
  })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>{{ route ? '编辑能力路由' : '添加能力路由' }}</DialogTitle>
        <DialogDescription>
          给目标模型附加能力：视觉（图片识别替换）、敏感词过滤（请求体整体替换）或字段过滤（请求/响应字段剔除与保留）。
        </DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="space-y-2">
          <Label>目标渠道</Label>
          <Popover v-model:open="channelOpen">
            <PopoverTrigger as-child>
              <Button type="button" variant="outline" class="w-full justify-between font-normal">
                <span class="truncate text-foreground">{{ channelTriggerLabel }}</span>
                <RiSearchLine class="size-4 shrink-0 opacity-50" />
              </Button>
            </PopoverTrigger>
            <PopoverContent
              class="max-h-80 w-[var(--reka-popper-anchor-width)] overflow-y-auto p-3"
              align="start"
            >
              <ChannelGroupPicker
                v-model="channelSel"
                :channels="channels"
                :allow-auto="false"
                :single-channel-group="false"
                layout="horizontal"
              />
              <p class="mt-2 text-xs text-muted-foreground">
                不选 = 路由对所有渠道生效；可单选/多选渠道或具体 Key，支持跨渠道组合。
              </p>
            </PopoverContent>
          </Popover>
          <div
            v-if="!channelSel.channel_base_url && selectedGroups.length"
            class="flex flex-wrap gap-1.5"
          >
            <Badge
              v-for="g in selectedGroups"
              :key="g.baseUrl + (g.level ? ':l' : ':k')"
              variant="secondary"
              class="gap-1 py-0 pr-1"
            >
              {{ formatChannelGroupLabel(g) }}
              <button
                type="button"
                class="rounded-full p-0.5 hover:bg-muted hover:text-destructive"
                aria-label="移除"
                @click="removeChannelGroup(g)"
              >
                <RiCloseLine size="12" />
              </button>
            </Badge>
          </div>
        </div>
        <div class="space-y-2">
          <Label>目标模型</Label>
          <TargetModelPicker
            v-model="form.models"
            :models="candidateModels"
            multiple
            allow-custom
          />
          <p class="text-xs text-muted-foreground">
            <b>留空 = 全部模型</b>（等价 <code class="font-mono">*</code>）；也支持
            <code class="font-mono">*</code> 通配与前缀匹配。候选随目标渠道过滤（通用/空 =
            全部渠道模型）；聚合（虚拟）模型也会一并列出。 未命中默认原生透传。
          </p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label>能力</Label>
            <Select :model-value="form.capability" @update:model-value="onCapabilityChange">
              <SelectTrigger><SelectValue placeholder="选择能力" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem
                    v-for="option in capabilityOptions"
                    :key="option.value"
                    :value="option.value"
                  >
                    {{ option.label }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-2">
            <Label>路由方式</Label>
            <Select v-model="form.route">
              <SelectTrigger><SelectValue placeholder="选择路由方式" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem
                    v-for="option in routeOptions"
                    :key="option.value"
                    :value="option.value"
                  >
                    {{ option.label }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
        </div>
        <div
          v-if="form.capability === CAP_FIELD_FILTER && form.route !== 'native'"
          class="space-y-2"
        >
          <Label>字段过滤规则（field_rules，每行一项）</Label>
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >request_strip（请求体字段剔除，点路径支持嵌套如 a.b.c）</Label
              >
              <Textarea
                v-model="form.fieldRulesText.request_strip"
                rows="2"
                placeholder="client_metadata"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >request_keep（请求体白名单，仅顶层 key）</Label
              >
              <Textarea
                v-model="form.fieldRulesText.request_keep"
                rows="2"
                placeholder="model&#10;messages"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >request_header_strip（请求头剔除，大小写不敏感）</Label
              >
              <Textarea
                v-model="form.fieldRulesText.request_header_strip"
                rows="1"
                placeholder="X-Api-Key&#10;Api-Key"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >response_strip（非流式响应体字段剔除）</Label
              >
              <Textarea v-model="form.fieldRulesText.response_strip" rows="2" placeholder="usage" />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >response_keep（非流式响应体白名单，仅顶层 key）</Label
              >
              <Textarea
                v-model="form.fieldRulesText.response_keep"
                rows="2"
                placeholder="choices"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground"
                >response_header_strip（响应头剔除，大小写不敏感）</Label
              >
              <Textarea
                v-model="form.fieldRulesText.response_header_strip"
                rows="1"
                placeholder="X-Internal-Header"
              />
            </div>
          </div>
          <p class="text-xs text-muted-foreground">
            keep 非空时走白名单（只保留，忽略同方向
            strip）；无字段命中时原字节透传；流式响应（SSE）不支持字段级过滤。典型场景： Codex 等
            agent 携带 <code class="font-mono">client_metadata</code> 被腾讯 copilot
            网关严格解析拒绝时，配
            <code class="font-mono">request_strip: client_metadata</code>；腾讯 copilot 网关优先用
            <code class="font-mono">x-api-key/api-key</code> 做认证会覆盖渠道 Authorization 时， 配
            <code class="font-mono">request_header_strip: X-Api-Key, Api-Key</code>。
          </p>
        </div>
        <div
          v-else-if="form.capability === CAP_SENSITIVE && form.route !== 'native'"
          class="space-y-2"
        >
          <div class="flex items-center justify-between gap-2">
            <Label>敏感词过滤列表（按从上到下顺序替换 / 命中判断）</Label>
            <div class="flex shrink-0 items-center gap-1">
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2 text-xs"
                      aria-label="导出到剪贴板"
                      @click="sensitiveListRef?.exportToClipboard?.()"
                    >
                      <RiClipboardLine size="14" />导出
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>把当前规则以 JSON 复制到剪贴板</TooltipContent>
                </Tooltip>
              </TooltipProvider>
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2 text-xs"
                      aria-label="从剪贴板导入"
                      @click="sensitiveListRef?.importFromClipboard?.()"
                    >
                      <RiUploadLine size="14" />导入
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>从剪贴板读取 JSON 覆盖当前规则</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </div>
          <SensitiveWordList
            ref="sensitiveListRef"
            v-model="form.replacements"
            add-label="添加规则"
          />
        </div>
        <div
          v-else-if="form.capability === CAP_MESSAGE_INJECT && form.route !== 'native'"
          class="space-y-2"
        >
          <div class="flex items-center justify-between gap-2">
            <Label>消息注入列表（按从上到下顺序依次注入）</Label>
            <div class="flex shrink-0 items-center gap-1">
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2 text-xs"
                      aria-label="导出到剪贴板"
                      @click="messageInjectRef?.exportToClipboard?.()"
                    >
                      <RiClipboardLine size="14" />导出
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>把当前注入以 JSON 复制到剪贴板</TooltipContent>
                </Tooltip>
              </TooltipProvider>
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2 text-xs"
                      aria-label="从剪贴板导入"
                      @click="messageInjectRef?.importFromClipboard?.()"
                    >
                      <RiUploadLine size="14" />导入
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>从剪贴板读取 JSON 覆盖当前注入</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </div>
          <MessageInjectList
            ref="messageInjectRef"
            v-model="form.injections"
            add-label="添加注入"
          />
        </div>
        <div
          v-else-if="form.capability === CAP_KEY_RATE_LIMIT && form.route !== 'native'"
          class="space-y-3"
        >
          <div class="grid gap-3 sm:grid-cols-2">
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">单个 Key 窗口内最大请求数</Label>
              <Input v-model="form.rateLimit.maxRequests" inputmode="numeric" placeholder="20" />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">窗口长度（秒）</Label>
              <Input v-model="form.rateLimit.windowSeconds" inputmode="numeric" placeholder="60" />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">单个 Key 并发上限（留空 = 不限）</Label>
              <Input
                v-model="form.rateLimit.maxConcurrency"
                inputmode="numeric"
                placeholder="不限"
              />
            </div>
            <div class="space-y-1">
              <Label class="text-xs text-muted-foreground">所有账号都用满时</Label>
              <Select v-model="form.rateLimit.onExhausted">
                <SelectTrigger><SelectValue placeholder="选择处理方式" /></SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem value="reject">返回 429（推荐）</SelectItem>
                    <SelectItem value="reuse">忽略限流，继续使用</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <p class="text-xs text-muted-foreground">
            两个维度可单独启用或同时启用：<b>次数</b>=滑动窗口看「从此刻往回数这么多秒」，用完暂时跳过；
            <b>并发</b>=同一时刻这个 Key 上最多有几个请求在跑。超额都会自动落到同平台的另一个账号，
            计数只存在内存里、重启后清零。当前设置 = {{ rateLimitSummary }}。
          </p>
        </div>
        <div
          v-else-if="form.capability === CAP_REQUEST_LOG && form.route !== 'native'"
          class="space-y-2"
        >
          <div class="flex items-center justify-between gap-3">
            <div class="space-y-1">
              <Label>保留原始 base64（图片不再打码）</Label>
              <p class="text-xs text-muted-foreground">
                开启后日志里的图片 data URI 原文照存，方便重放调试；代价是日志体积明显变大。
                关闭时按图片占位符
                <code class="font-mono">[image: mime, NB]</code>
                记录。无论开关如何，<b>sk- 密钥始终打码</b>。
              </p>
            </div>
            <Switch v-model="form.requestLogKeepBase64" />
          </div>
        </div>
        <div v-else-if="form.capability === CAP_VISION && form.route === 'proxy'" class="space-y-2">
          <Label>视觉候选（从上到下依次请求，失败换下一个）</Label>
          <ModelChannelList v-model="form.viaOptions" :channels="channels" add-label="添加候选" />
        </div>
        <DialogFooter ref="footerRef" class="sm:justify-between">
          <p v-if="formError" class="text-xs font-medium text-destructive">{{ formError }}</p>
          <p v-else class="text-xs text-muted-foreground">{{ routeHint }}</p>
          <div class="flex gap-2">
            <Button type="submit" :disabled="pending">{{ pending ? '正在保存' : '保存' }}</Button>
            <Button type="button" variant="outline" :disabled="pending" @click="open = false"
              >取消</Button
            >
          </div>
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
