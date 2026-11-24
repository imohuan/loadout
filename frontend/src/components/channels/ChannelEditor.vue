<script setup lang="ts">
import { computed, nextTick, reactive, ref, watch } from 'vue'
import {
  RiCloseLine,
  RiFlaskLine,
  RiLoader4Line,
  RiRefreshLine,
  RiSearchLine,
} from '@remixicon/vue'
import { VueDraggable } from 'vue-draggable-plus'
import type { Channel } from '@/lib/types'
import { useChannels, normalizeBaseURL, type ChannelInput } from '@/composables/useChannels'
import { useModelTest } from '@/composables/useModelTest'

const service = useChannels()
const { probe } = useModelTest()
const props = defineProps<{
  channel?: Channel
  pending?: boolean
  /** 非空 = "添加 Key" 模式：base_url 锁定为该值（同组渠道），只需填 Key 名与 API Key */
  lockBaseUrl?: string
  /** 所属渠道组的名称（添加 Key 模式只读展示，来自同组首个 Key） */
  groupName?: string
}>()
const emit = defineEmits<{ save: [value: ChannelInput]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })
const form = reactive<{
  channel_name: string
  name: string
  base_url: string
  api_key: string
  manual_enabled: boolean
  sync_billing: boolean
  framework: string
  models: string[]
  model_candidates: string[]
}>({
  channel_name: '',
  name: '',
  base_url: '',
  api_key: '',
  manual_enabled: true,
  sync_billing: false,
  framework: '',
  models: [],
  model_candidates: [],
})
// 模型搜索词与下拉开关（必须在 watch 前声明，immediate 回调会用到）。
const modelOpen = ref(false)
const modelSearch = ref('')
// "获取模型"按钮状态：探测中 / 探测错误。
const fetchingModels = ref(false)
const probeError = ref('')
// 测试卡片的状态：选中的测试模型、是否正在测、最近一次结果。
// 必须声明在 resetForm 之前：resetForm 会清结果，而它在 watch(immediate) 里
// 于 setup 阶段就会跑，声明在后面的 const 此时还在 TDZ。
const testModel = ref('')
const testing = ref(false)
const testResult = ref<{ model: string; ok: boolean; error?: string } | null>(null)

function resetForm() {
  const channel = props.channel
  const detail = channel?.models_detail
  Object.assign(form, {
    channel_name: channel?.channel_name || props.groupName || '',
    name: channel?.name || '',
    base_url: props.lockBaseUrl || channel?.base_url || '',
    framework: channel?.framework ?? '',
    api_key: '',
    manual_enabled: channel?.manual_enabled ?? channel?.enabled ?? true,
    sync_billing: channel?.sync_billing ?? false,
    models: detail
      ? detail.filter((d) => d.enabled).map((d) => d.model)
      : [...(channel?.models || [])],
    model_candidates: detail ? detail.map((d) => d.model) : [...(channel?.models || [])],
  })
  modelSearch.value = ''
  probeError.value = ''
  // 上一次的测试结果属于上一个 Key：换一个渠道/Key 打开时必须清掉，
  // 否则会出现「新 Key 还没测，界面已经写着测试通过」。
  testResult.value = null
}
// 每次打开时重置表单：连续两次"添加 Key"（editing 恒 undefined，channel 不变化）
// 也必须清空残留，不能只 watch channel。
watch(
  () => open.value,
  (isOpen) => {
    if (isOpen) {
      resetForm()
      loadSiblings()
    }
  },
  { immediate: true },
)
// 编辑时切换目标渠道（channel 变化且弹窗开着）也要重新填充。
watch(
  () => props.channel,
  () => {
    if (open.value) resetForm()
  },
)
// 添加 Key 模式：base_url 锁定为组 base_url（编辑已有 Key 时不受影响）。
watch(
  () => props.lockBaseUrl,
  (baseUrl) => {
    if (baseUrl) form.base_url = baseUrl
  },
)

// ===== 模型列表（候选 tag 网格 + 搜索 + 全选/反选 + 自定义）=====
const modelsError = computed(() => props.channel?.models_error || '')
// 候选池：已获取模型 ∪ 自定义模型，去重**保序**。
// 这里不能排序：用户拖拽调出来的先后就是保存进库的顺序，排一次字母序等于
// 把刚才的拖拽全抹掉。
const candidateModels = computed(() => [...new Set(form.model_candidates)])
const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  if (!q) return candidateModels.value
  return candidateModels.value.filter((m) => m.toLowerCase().includes(q))
})
// 统一解析入口：空格 / Tab / 换行 / 英文逗号 / 中文逗号
function parseModelTokens(raw: string): string[] {
  return raw
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}
// 当前搜索框按批量分隔符拆出的 token 列表（供「未找到」占位区预览）
const parsedSearchTokens = computed(() => parseModelTokens(modelSearch.value))
function toggleModel(model: string) {
  const i = form.models.indexOf(model)
  if (i >= 0) form.models.splice(i, 1)
  else form.models.push(model)
}
function selectAll() {
  form.models = [...candidateModels.value]
}
function invertAll() {
  form.models = candidateModels.value.filter((m) => !form.models.includes(m))
}
function clearAll() {
  form.models = []
}
// 支持空格 / 换行 / 逗号分隔批量添加自定义模型
function addCustomModel() {
  const tokens = parsedSearchTokens.value
  if (!tokens.length) return
  for (const name of tokens) {
    if (!candidateModels.value.includes(name)) form.model_candidates.push(name)
    if (!form.models.includes(name)) form.models.push(name)
  }
  modelSearch.value = ''
}
function onModelSearchEnter() {
  if (!modelSearch.value.trim()) return
  addCustomModel()
}
// 按表单当前值探测上游 /v1/models，结果并入候选池（不落库，保存时才生效）。
// 新建：传 base_url + api_key；编辑：Key 不回显，传 id 让后台取已存 Key。
async function fetchModels() {
  const baseUrl = form.base_url.trim()
  if (!baseUrl) return
  fetchingModels.value = true
  probeError.value = ''
  try {
    const result = await service.probe({
      id: props.channel?.id,
      base_url: baseUrl,
      api_key: form.api_key,
    })
    probeError.value = result.models_error || ''
    if (result.models?.length) {
      const known = new Set(form.model_candidates)
      for (const m of result.models) {
        if (!known.has(m)) {
          known.add(m)
          form.model_candidates.push(m)
          // 新探测到的模型只入候选池，不自动勾选，避免一次拉上百个误启用。
        }
      }
    }
  } catch (err) {
    probeError.value = err instanceof Error ? err.message : String(err)
  } finally {
    fetchingModels.value = false
  }
}

// ===== 模型清单拖拽排序 =====
/**
 * 已选模型的可拖拽副本。
 *
 * 拖拽组件需要可写数组承载内部移动，所以用一份本地副本，松手后按新顺序写回
 * form.models。保存时这个顺序就是渠道模型清单的顺序（后端按数组下标落库）。
 */
const dragModels = ref<string[]>([])
/** 是否正在拖拽（官方「混合动画」示例的 drag 标记）。 */
const dragging = ref(false)
/** 松手后 nextTick 复位，避免与 Sortable 收尾抢 DOM（官方示例写法）。 */
function endDrag() {
  nextTick(() => {
    dragging.value = false
    // 拖拽期间跳过了「form.models → dragModels」的同步，这里补一次，
    // 保证后续在别处改模型清单时列表能跟上。
    if (!sameModels(dragModels.value, form.models)) dragModels.value = form.models.slice()
  })
}
function sameModels(a: string[], b: string[]) {
  return a.length === b.length && a.every((m, i) => m === b[i])
}
watch(
  () => form.models,
  (models) => {
    // 拖拽期间绝不同步：Sortable 正在做位移动画，此时给 dragModels 重新赋一个
    // 新数组会让 Vue 立刻按新顺序重排 DOM，Sortable 抓到的「旧位置」就变成了新位置，
    // 补间位移恒为 0 —— 表现就是「拖动时没有动画」。拖拽结束（endDrag）后再同步。
    if (dragging.value) return
    // 内容或顺序变了才同步；拖拽过程中父级刷新不该把列表弹回旧顺序。
    if (!sameModels(dragModels.value, models)) dragModels.value = models.slice()
  },
  { immediate: true },
)
// 补一个深度监听：上面的 getter 写法在 form.models 被整体替换成新数组时
// 不会触发（reactive 解引用 getter 不标脏），表现为勾选/取消/导入模型后
// 已选列表不动、出现「已选 N 个但列表空了/少一截」的错位。深度监听按内
// 容比对，只有真的变了才重置拖拽副本。
watch(
  form.models,
  (models) => {
    if (dragging.value) return
    if (!sameModels(dragModels.value, models)) dragModels.value = models.slice()
  },
  { deep: true, immediate: true },
)
function onModelReorder(models: string[]) {
  dragModels.value = models
  form.models = models.slice()
}

// ===== 就地测试已选模型 =====
/**
 * 当前选中的测试模型 + 测试结果。
 *
 * 这里直接请求上游（不走本机 /v1）：这是「保存前先探一下这个模型能不能用」的
 * 轻量检查，编辑中的 Key 可能还没落库，没必要也不应该影响渠道健康状态。
 * 失败不抛异常，把上游原文带回来显示，用户才知道是限流、欠费还是名字写错。
 */
/** 默认测第一个已选模型：打开就能点「测试」，不用先在下拉里挑。 */
watch(
  () => form.models,
  (models) => {
    if (!models.length) {
      testModel.value = ''
      testResult.value = null
      return
    }
    if (!models.includes(testModel.value)) testModel.value = models[0]
  },
  { immediate: true },
)
async function runModelTest() {
  if (!testModel.value || testing.value) return
  const baseUrl = form.base_url.trim()
  if (!baseUrl) {
    testResult.value = { model: testModel.value, ok: false, error: '请先填写 Base URL' }
    return
  }
  testing.value = true
  const model = testModel.value
  try {
    // 编辑已有 Key：优先用 channel_id 让后端解密已存密钥（Key 输入框留空时也能测）。
    // 新建 / 添加 Key：用表单里刚填的 api_key。
    const target = props.channel?.id
      ? { channel_id: props.channel.id, api_key: form.api_key, base_url: baseUrl }
      : { base_url: baseUrl, api_key: form.api_key }
    const result = await probe(target, model, [{ role: 'user', content: 'ping' }])
    testResult.value = result.ok
      ? { model, ok: true }
      : { model, ok: false, error: result.error || `请求失败（${result.status}）` }
  } catch (err) {
    testResult.value = {
      model,
      ok: false,
      error: err instanceof Error ? err.message : String(err),
    }
  } finally {
    testing.value = false
  }
}

// ===== 从同渠道其他 Key 导入模型（用于探测失败的渠道，参考同组兄弟 Key 的已知模型）=====
// 拉取全量渠道；按当前 base_url normalize 过滤；编辑模式排除自己，添加模式不过滤（新 Key 无 id）。
const allChannels = ref<Channel[]>([])
async function loadSiblings() {
  try {
    const list = await service.list()
    allChannels.value = list || []
  } catch {
    // 列表拉取失败不影响主流程；siblingChannels 退化为空数组，右侧 Select 自动隐藏。
    allChannels.value = []
  }
}
const currentBaseUrl = computed(() =>
  normalizeBaseURL(props.lockBaseUrl || props.channel?.base_url || form.base_url || ''),
)
const siblingChannels = computed<Channel[]>(() => {
  const target = currentBaseUrl.value
  if (!target) return []
  const selfId = props.channel?.id
  return (allChannels.value || []).filter(
    (c) => normalizeBaseURL(c.base_url) === target && (!selfId || c.id !== selfId),
  )
})
// Select 受控的"瞬时选择值"：选中即触发合并，然后把 ref 重置为 '' 让 Select 回到 placeholder。
// 不写入 model，避免 trigger 长期显示某个 Key 名干扰添加流程。
const importKey = ref<string>('')
function importSiblingModels(id: string) {
  if (!id) return
  const sibling = (allChannels.value || []).find((c) => c.id === id)
  if (!sibling) return
  const candidateSet = new Set(form.model_candidates)
  const selectedSet = new Set(form.models)
  let addedSelected = 0
  for (const m of sibling.models || []) {
    // 未在候选池则补入；用户明确要求「导入即全选」，所以候选 + 已选同步追加。
    if (!candidateSet.has(m)) {
      candidateSet.add(m)
      form.model_candidates.push(m)
    }
    if (!selectedSet.has(m)) {
      selectedSet.add(m)
      form.models.push(m)
      addedSelected++
    }
  }
  importKey.value = ''
  if (addedSelected === 0) {
    probeError.value = `「${sibling.name}」的模型已全部选中`
  } else {
    probeError.value = ''
  }
}

function submit() {
  emit('save', {
    ...form,
    // 添加 Key 模式：渠道名跟随所属组，交由后端继承，避免把组名当新值提交。
    channel_name: props.lockBaseUrl ? '' : form.channel_name,
    models: [...form.models],
    // 候选清单的顺序 = 用户拖出来的已选顺序优先，未选的接在后面。
    // 直接提交原始候选顺序（探测顺序）会让拖拽白做：后端按数组下标存 position。
    model_candidates: [
      ...form.models,
      ...candidateModels.value.filter((m) => !form.models.includes(m)),
    ],
  })
}
</script>

<template>
  <Dialog v-model:open="open">
    <!-- overflow-x-hidden：拖拽排序时 Sortable 的克隆块会临时超出内容区，
         不隐藏横向溢出的话，弹窗底部会多出一条横向滚动条。 -->
    <DialogContent
      class="max-h-[calc(100dvh-2rem)] overflow-x-hidden overflow-y-auto sm:max-w-2xl!"
    >
      <DialogHeader>
        <DialogTitle>{{
          lockBaseUrl ? '添加 Key' : channel ? '编辑渠道' : '添加渠道'
        }}</DialogTitle>
        <DialogDescription v-if="lockBaseUrl">
          为 {{ lockBaseUrl }} 添加一个独立账号（Key）。每个 Key 独立探测模型、独立健康状态。
        </DialogDescription>
        <DialogDescription v-else>支持 NewAPI 和 OpenAI 兼容的上游服务。</DialogDescription>
      </DialogHeader>
      <form class="grid gap-4 md:grid-cols-2" @submit.prevent="submit">
        <div class="space-y-2">
          <Label for="channel-name-title">渠道名称</Label
          ><Input
            id="channel-name-title"
            v-model="form.channel_name"
            :disabled="!!lockBaseUrl"
            :placeholder="lockBaseUrl ? '跟随渠道组' : '如：主力 NewAPI'"
          />
        </div>
        <div class="space-y-2">
          <Label for="channel-name">{{ lockBaseUrl ? 'Key 名称' : '名称' }}</Label
          ><Input
            id="channel-name"
            v-model="form.name"
            required
            :placeholder="lockBaseUrl ? '如：主账号 / 备用账号' : '本地 NewAPI'"
          />
        </div>
        <div class="space-y-2">
          <Label for="channel-url">Base URL</Label
          ><Input
            id="channel-url"
            v-model="form.base_url"
            type="url"
            required
            :disabled="!!lockBaseUrl"
            placeholder="http://127.0.0.1:3001/v1"
          />
          <p class="text-xs text-muted-foreground">
            基础 URL 请填写到接口前缀的完整路径，如需 /v1 前缀请自行包含在 URL
            中，系统不会自动补全。
          </p>
        </div>
        <div class="space-y-2" v-if="!lockBaseUrl">
          <Label for="channel-framework">平台框架（可选）</Label>
          <Input
            id="channel-framework"
            v-model="form.framework"
            list="framework-options"
            placeholder="newapi / one-api / 自定义"
          />
          <datalist id="framework-options">
            <option value="newapi" />
            <option value="one-api" />
            <option value="done-hub" />
            <option value="voapi" />
          </datalist>
          <p class="text-xs text-muted-foreground">
            标注框架后，失败规则可按框架匹配（同框架平台共用一条规则，如所有 New API
            站的额度规则）。同组渠道自动继承标注。
          </p>
        </div>
        <div class="space-y-2">
          <Label for="channel-key">API Key{{ channel ? '（留空不修改）' : '' }}</Label
          ><Input id="channel-key" v-model="form.api_key" type="password" autocomplete="off" />
        </div>
        <div
          class="flex flex-col justify-start gap-3 pb-1 sm:flex-row sm:items-center sm:justify-start"
        >
          <div class="flex items-center gap-2">
            <Switch id="channel-enabled" v-model="form.manual_enabled" /><Label
              for="channel-enabled"
              >启用渠道</Label
            >
          </div>
          <!--
            「同步渠道费用状态」（sync_billing）的开关不再暴露：它只服务一条 legacy 兜底 ——
            402 + 余额文案时把整个渠道一起置为不可用。失败规则引擎里已有等价的
            「账户余额不足（禁用key）」规则（402 → disable_key + never），粒度是 Key 级、
            有判定日志、可调；这条路径更粗（整渠道连坐）且不进日志。
            后端字段与逻辑保留不动（历史渠道全部为开启，贸然删除会静默改变路由行为）。
          -->
        </div>
        <div class="space-y-2 md:col-span-2">
          <Label>模型列表</Label>
          <Popover v-model:open="modelOpen">
            <div class="flex items-stretch gap-2">
              <PopoverTrigger as-child class="flex-1">
                <Button type="button" variant="outline" class="w-full justify-between font-normal">
                  <span v-if="form.models.length" class="text-muted-foreground"
                    >已选 {{ form.models.length }} 个模型</span
                  ><span v-else class="text-muted-foreground">选择模型（可搜索 / 自定义）</span
                  ><RiSearchLine class="size-4 shrink-0 opacity-50" />
                </Button>
              </PopoverTrigger>
              <Select
                v-if="siblingChannels.length"
                :model-value="importKey"
                @update:model-value="importSiblingModels"
              >
                <SelectTrigger class="w-[180px] shrink-0" aria-label="从其他 Key 导入模型">
                  <SelectValue placeholder="从其他 Key 导入" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="end" :side-offset="4">
                  <SelectGroup>
                    <SelectItem v-for="s in siblingChannels" :key="s.id" :value="s.id">
                      <TooltipProvider :delay-duration="150" :skip-delay-duration="100">
                        <Tooltip>
                          <TooltipTrigger as-child>
                            <span class="flex w-full items-center justify-between gap-2">
                              <span class="truncate">{{ s.name }}</span>
                              <span class="shrink-0 text-xs text-muted-foreground"
                                >{{ s.models?.length ?? 0 }} 个</span
                              >
                            </span>
                          </TooltipTrigger>
                          <TooltipContent
                            side="right"
                            class="max-w-xs whitespace-normal"
                            :side-offset="6"
                          >
                            <div class="text-xs text-muted-foreground">
                              {{
                                (s.models || []).length
                                  ? (s.models || []).slice(0, 30).join('、') +
                                    ((s.models || []).length > 30 ? '…' : '')
                                  : '尚未配置模型'
                              }}
                            </div>
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
            <PopoverContent class="w-[var(--reka-popper-anchor-width)] p-2" align="start">
              <div class="space-y-2">
                <div class="flex items-center gap-2">
                  <Input
                    v-model="modelSearch"
                    placeholder="搜索或批量粘贴模型名（空格 / 换行 / 逗号分隔，回车添加自定义…）"
                    class="flex-1"
                    @keydown.esc="modelOpen = false"
                    @keydown.enter.prevent="onModelSearchEnter"
                  />
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    class="shrink-0"
                    :disabled="!form.base_url.trim() || fetchingModels"
                    title="按当前 Base URL / API Key 重新探测模型"
                    @click="fetchModels"
                  >
                    <RiRefreshLine :class="{ 'animate-spin': fetchingModels }" size="14" />
                    获取模型
                  </Button>
                  <div class="flex items-center gap-1">
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      :disabled="!candidateModels.length"
                      @click="selectAll"
                      >全选</Button
                    >
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      :disabled="!candidateModels.length"
                      @click="invertAll"
                      >反选</Button
                    >
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      :disabled="!form.models.length"
                      @click="clearAll"
                      >清空</Button
                    >
                  </div>
                </div>
                <div
                  v-if="filteredModels.length"
                  class="flex max-h-56 flex-wrap gap-1.5 overflow-y-auto rounded-md border border-border p-2"
                >
                  <Button
                    v-for="m in filteredModels"
                    :key="m"
                    type="button"
                    size="sm"
                    :variant="form.models.includes(m) ? 'default' : 'outline'"
                    @click="toggleModel(m)"
                    >{{ m }}</Button
                  >
                </div>
                <div
                  v-else
                  class="flex flex-col items-center gap-2 rounded-md border border-border p-3"
                >
                  <p v-if="parsedSearchTokens.length <= 1" class="text-xs text-muted-foreground">
                    未找到「{{ modelSearch }}」
                  </p>
                  <template v-else>
                    <p class="text-xs text-muted-foreground">
                      未在候选中找到，将作为自定义模型添加（共
                      {{ parsedSearchTokens.length }} 个）：
                    </p>
                    <div class="flex max-w-full flex-wrap items-center justify-center gap-1">
                      <Badge v-for="t in parsedSearchTokens" :key="t" variant="secondary">{{
                        t
                      }}</Badge>
                    </div>
                  </template>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    :disabled="!parsedSearchTokens.length"
                    @click="addCustomModel"
                  >
                    自定义添加<span v-if="parsedSearchTokens.length > 1">
                      （{{ parsedSearchTokens.length }}）
                    </span>
                  </Button>
                </div>
              </div>
            </PopoverContent>
          </Popover>
          <!--
            已选模型：直接拖拽调整顺序。这个顺序会随保存写入渠道模型清单，
            也是「模型状态」页、/v1/models 里看到的先后。
            不设独立拖拽手柄：整块标签都可拖，点标签里的 × 仍是删除。
          -->
          <div
            v-if="form.models.length"
            class="relative max-h-40 overflow-x-hidden overflow-y-auto rounded-md border border-border p-2"
          >
            <!--
              官方写法：VueDraggable 负责排序行为，里面放 TransitionGroup 负责列表 DOM。
              target 指向 TransitionGroup 渲染出的那个容器，animation 就是换位动画。
            -->
            <VueDraggable
              :model-value="dragModels"
              target=".channel-editor-model-list"
              :animation="200"
              @start="dragging = true"
              @end="endDrag"
              @update:model-value="onModelReorder"
            >
              <TransitionGroup
                type="transition"
                tag="div"
                :name="!dragging ? 'model-chip' : undefined"
                class="model-chip-list channel-editor-model-list flex flex-wrap gap-1.5"
              >
                <!--
                  每一项都是独立的 button（与 Key 列表同构）。
                  之前直接渲染 Badge（span），Sortable 拖动时既要移动元素又要 Vue 重绘，
                  位移量会算成 0，表现为「拖拽过程中没有换位动画」。
                -->
                <button
                  v-for="m in dragModels"
                  :key="m"
                  type="button"
                  class="model-chip flex cursor-grab select-none items-center gap-1 rounded-md border border-border bg-muted px-2 py-0.5 font-mono text-[11px] text-foreground active:cursor-grabbing"
                  :title="`拖动排序：${m}`"
                >
                  <span>{{ m }}</span>
                  <span
                    class="rounded-full p-0.5 hover:bg-background hover:text-destructive"
                    aria-label="移除"
                    @click.stop="toggleModel(m)"
                  >
                    <RiCloseLine size="12" />
                  </span>
                </button>
              </TransitionGroup>
            </VueDraggable>
          </div>

          <!--
            测试卡片：对当前选中的某个模型真的打一次上游，看它能不能出结果。
            放在模型清单下面（不藏在下拉里），保存前就能确认这个名字上游认不认。
          -->
          <div class="space-y-2 rounded-md border border-border p-2">
            <div class="flex flex-wrap items-center gap-2">
              <Label for="model-test-pick" class="shrink-0 text-xs text-muted-foreground"
                >测试模型</Label
              >
              <Select v-model="testModel">
                <SelectTrigger
                  id="model-test-pick"
                  class="h-8 flex-1"
                  :disabled="!form.models.length || testing"
                >
                  <SelectValue placeholder="先在上面选模型" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="4">
                  <SelectGroup>
                    <SelectItem v-for="m in form.models" :key="m" :value="m">{{ m }}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="outline"
                size="sm"
                class="shrink-0"
                :disabled="!testModel || testing"
                :title="`向 ${form.base_url || 'Base URL'} 发一条 ping，看模型能不能出结果`"
                @click="runModelTest"
              >
                <RiLoader4Line v-if="testing" class="animate-spin" size="14" />
                <RiFlaskLine v-else size="14" />
                {{ testing ? '测试中' : '测试' }}
              </Button>
            </div>
            <p
              v-if="testResult && testResult.ok"
              class="rounded bg-emerald-500/15 px-2 py-1 text-xs text-emerald-700 dark:text-emerald-300"
            >
              「{{ testResult.model }}」测试通过
            </p>
            <p
              v-else-if="testResult"
              class="rounded bg-red-500/15 px-2 py-1 text-xs leading-snug text-red-700 dark:text-red-300"
            >
              「{{ testResult.model }}」测试失败：{{ testResult.error }}
            </p>
            <p v-else class="text-xs text-muted-foreground">
              保存前可以先测一个模型；失败原因会显示上游原文。
            </p>
          </div>
          <p v-if="probeError" class="text-xs text-destructive">获取模型失败：{{ probeError }}</p>
          <p v-else-if="modelsError" class="text-xs text-destructive">
            上次探测模型失败：{{ modelsError }}
          </p>
          <p v-else class="text-xs text-muted-foreground">
            点击切换启用；自定义模型保存后写入渠道清单。
          </p>
        </div>
        <DialogFooter class="md:col-span-2"
          ><Button type="submit" :disabled="pending">{{
            pending ? '正在保存' : lockBaseUrl ? '保存 Key' : '保存渠道'
          }}</Button
          ><Button type="button" variant="outline" :disabled="pending" @click="open = false"
            >取消</Button
          >
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
