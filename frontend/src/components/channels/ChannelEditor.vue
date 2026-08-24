<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { RiCloseLine, RiRefreshLine, RiSearchLine } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { useChannels, normalizeBaseURL, type ChannelInput } from '@/composables/useChannels'

const service = useChannels()
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
  models: string[]
  model_candidates: string[]
}>({
  channel_name: '',
  name: '',
  base_url: '',
  api_key: '',
  manual_enabled: true,
  sync_billing: false,
  models: [],
  model_candidates: [],
})
// 模型搜索词与下拉开关（必须在 watch 前声明，immediate 回调会用到）。
const modelOpen = ref(false)
const modelSearch = ref('')
// "获取模型"按钮状态：探测中 / 探测错误。
const fetchingModels = ref(false)
const probeError = ref('')

function resetForm() {
  const channel = props.channel
  const detail = channel?.models_detail
  Object.assign(form, {
    channel_name: channel?.channel_name || props.groupName || '',
    name: channel?.name || '',
    base_url: props.lockBaseUrl || channel?.base_url || '',
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
// 候选池：已获取模型 ∪ 自定义模型，去重排序。
const candidateModels = computed(() => [...new Set(form.model_candidates)].sort())
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
    model_candidates: [...candidateModels.value],
  })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl!">
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
            基础 URL 请填写到接口前缀的完整路径，如需 /v1 前缀请自行包含在 URL 中，系统不会自动补全。
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
          <div class="flex items-center gap-2">
            <Switch id="sync-billing" v-model="form.sync_billing" /><Label for="sync-billing"
              >同步渠道费用状态</Label
            >
          </div>
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
                    <SelectItem
                      v-for="s in siblingChannels"
                      :key="s.id"
                      :value="s.id"
                    >
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
          <div
            v-if="form.models.length"
            class="flex max-h-40 flex-wrap gap-1.5 overflow-y-auto rounded-md border border-border p-2"
          >
            <Badge v-for="m in form.models" :key="m" variant="secondary" class="gap-1 py-0 pr-1">
              {{ m }}
              <button
                type="button"
                class="rounded-full p-0.5 hover:bg-muted hover:text-destructive"
                aria-label="移除"
                @click="toggleModel(m)"
              >
                <RiCloseLine size="12" />
              </button>
            </Badge>
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
