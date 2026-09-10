<script setup lang="ts">
// 渠道模型同步弹窗：把「一份模型清单」同步到选中的多个 Key 上。
//
// 结构（单栏自上而下）：
//   1. 加载源：可从某个 Key 载入其模型清单作为底稿（每个 Key 的模型数可见，悬停可看模型明细）。
//   2. 模型清单编辑区：与编辑器里的模型 Popover 内容区一致（搜索 / 批量粘贴 / 自定义 /
//      全选 / 反选 / 清空），区别是直接铺在弹窗上而非藏在下拉里。
//   3. 目标 Key 多选：全选 / 反选 / 清空 + 列表、标签两种显示模式。
//
// 同步语义：全量替换 —— 目标 Key 的模型目录被写成这份清单（新模型统一启用），
// 不改动目标 Key 的 API Key、开关与费用设置。
import { computed, reactive, ref, watch } from 'vue'
import { RiAddLine, RiFileCopyLine, RiLoader4Line } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from '@/composables/useChannels'
import BulkSelectButtons from '@/components/BulkSelectButtons.vue'

const props = defineProps<{
  /** 全部渠道（Key）列表 */
  channels: Channel[]
  /** 触发按钮所在的渠道组（base_url）：载入源与同步目标都只在这个平台内选 */
  baseUrl?: string
  pending?: boolean
}>()
const emit = defineEmits<{
  sync: [payload: { channelIds: string[]; models: string[] }]
}>()
const open = defineModel<boolean>('open', { required: true })

// ===== 模型清单（弹窗里的「我的模型列表」）=====
const form = reactive<{ models: string[]; candidates: string[] }>({
  models: [],
  candidates: [],
})
const modelSearch = ref('')
const sourceId = ref('')

// 当前平台 = 同 Base URL 的渠道组。同步、载入源、模型候选全部限定在这个组内。
// 以「载入源」为准；源未选时退回按钮所在的渠道组。
const currentGroupUrl = computed(() => {
  const source = sourceId.value ? findChannel(sourceId.value) : undefined
  if (source) return normalizeBaseURL(source.base_url)
  return props.baseUrl ? normalizeBaseURL(props.baseUrl) : ''
})

// 目标 Key：勾选顺序即写入顺序。
const selectedIds = ref<string[]>([])
// 显示模式：列表（每行平铺该 Key 的模型）/ 标签（hover tooltip 看模型）。
const displayMode = ref<'list' | 'tag'>('list')

// 候选池 = 已载入的清单 ∪ 同名渠道其他 Key 已配置的模型（∪ 自定义），去重排序。
const channelGroups = computed(() => groupChannelsByBaseURL(props.channels || []))
const poolModels = computed(() => {
  const set = new Set<string>(form.candidates)
  for (const ch of props.channels || []) {
    for (const m of ch.models || []) set.add(m)
  }
  return [...set].sort()
})
const filteredModels = computed(() => {
  const q = modelSearch.value.trim().toLowerCase()
  if (!q) return poolModels.value
  return poolModels.value.filter((m) => m.toLowerCase().includes(q))
})
const selectedModelCount = computed(() => form.models.length)
// 所有 Key 的模型并集，作为「从其他 Key 载入模型」的候选池（与 ChannelEditor 的导入入口一致）。
// 同步限同平台，所以只并同一渠道组（同 Base URL）下的 Key，跨平台模型名不通用、混进来只会误选。
const allModels = computed(() => {
  const target = currentGroupUrl.value
  const set = new Set<string>()
  for (const ch of props.channels || []) {
    if (target && normalizeBaseURL(ch.base_url) !== target) continue
    for (const m of ch.models || []) set.add(m)
  }
  return [...set].sort()
})
// 载入源的候选 Key：只能从同一平台（同 Base URL）的其他 Key 载入。
const sourceOptions = computed(() => {
  const target = currentGroupUrl.value
  return (props.channels || []).filter(
    (ch) => !!ch.id && (!target || normalizeBaseURL(ch.base_url) === target),
  )
})

// 统一解析入口：空格 / Tab / 换行 / 英文逗号 / 中文逗号
function parseModelTokens(raw: string): string[] {
  return raw
    .split(/[\s,，]+/)
    .map((s) => s.trim())
    .filter(Boolean)
}
const parsedSearchTokens = computed(() => parseModelTokens(modelSearch.value))

function toggleModel(model: string) {
  const i = form.models.indexOf(model)
  if (i >= 0) form.models.splice(i, 1)
  else form.models.push(model)
}
function selectAllModels() {
  form.models = [...poolModels.value]
}
function invertAllModels() {
  form.models = poolModels.value.filter((m) => !form.models.includes(m))
}
function clearAllModels() {
  form.models = []
}
// 支持空格 / 换行 / 逗号分隔批量添加自定义模型
function addCustomModel() {
  const tokens = parsedSearchTokens.value
  if (!tokens.length) return
  for (const name of tokens) {
    if (!form.candidates.includes(name)) form.candidates.push(name)
    if (!form.models.includes(name)) form.models.push(name)
  }
  modelSearch.value = ''
}
function onModelSearchEnter() {
  if (!modelSearch.value.trim()) return
  addCustomModel()
}

// 从某个 Key 载入模型清单：整份替换（这会清掉当前编辑内容，所以用「载入」而非「追加」的语义）。
function loadFromChannel(id: string) {
  if (!id) return
  sourceId.value = id
  const channel = findChannel(id)
  const models = (channel?.models || []).slice()
  form.models = models
  form.candidates = models.slice()
  modelSearch.value = ''
}
function reloadFromSource() {
  if (sourceId.value) loadFromChannel(sourceId.value)
}
// 纯模型清单的 Picker 不分组，这里把「全部 Key 的模型并集」当作一个伪 Key 的目录载入。
const ALL_SOURCE_ID = '__all__'
function importAllModels() {
  sourceId.value = ALL_SOURCE_ID
  form.models = [...allModels.value]
  form.candidates = [...allModels.value]
  modelSearch.value = ''
}

function findChannel(id: string): Channel | undefined {
  return (props.channels || []).find((ch) => ch.id === id)
}
// 载入源下拉里展示的模型预览（复用 ChannelEditor「从其他 Key 导入」的呈现方式）。
function sourcePreview(models?: string[]) {
  const list = models || []
  if (!list.length) return '尚未配置模型'
  return list.slice(0, 30).join('、') + (list.length > 30 ? '…' : '')
}

// ===== 目标 Key 多选 =====
// 目标限定在同一渠道组（同 Base URL = 同一平台）内，并排除「载入源」本身：
//   - 跨平台同步没有意义（不同上游的模型名不通用），所以只列同组。
//   - 源只提供清单，同步回源 Key 属误操作，排除掉。
// 组标识由按钮所在渠道组给出，所以即使尚未选源也不会跨平台。
const targetGroups = computed(() => {
  const target = currentGroupUrl.value
  const groups = target
    ? channelGroups.value.filter((g) => normalizeBaseURL(g.baseUrl) === target)
    : channelGroups.value
  return groups
    .map((group) => ({ ...group, keys: group.keys.filter((k) => k.id !== sourceId.value) }))
    .filter((group) => group.keys.length > 0)
})
const targetKeys = computed(() => targetGroups.value.flatMap((group) => group.keys))
const targetIds = computed(() => targetKeys.value.map((k) => k.id))
const selectedTargets = computed(() =>
  targetKeys.value.filter((k) => selectedIds.value.includes(k.id)),
)
const allSelected = computed(
  () => targetIds.value.length > 0 && selectedIds.value.length === targetIds.value.length,
)
const groupTitle = (group: { baseUrl: string; keys: Channel[] }) => {
  const first = group.keys[0]
  return first?.channel_name || first?.name || group.baseUrl
}
function keyLabel(key: Channel) {
  return key.name || key.id
}
function keyModels(key: Channel) {
  return key.models || []
}
function isTargetSelected(id: string) {
  return selectedIds.value.includes(id)
}
function toggleTarget(id: string) {
  const i = selectedIds.value.indexOf(id)
  if (i >= 0) selectedIds.value.splice(i, 1)
  else selectedIds.value.push(id)
}
function selectAllTargets() {
  selectedIds.value = [...targetIds.value]
}
function invertTargets() {
  selectedIds.value = targetIds.value.filter((id) => !selectedIds.value.includes(id))
}
function clearTargets() {
  selectedIds.value = []
}

// 每次打开重置：不预设载入源（按钮属于整组、不跟某个 Key），模型清单留空由用户
// 自己载入或直接编辑；目标默认全选本平台（同 Base URL）的全部 Key。
watch(
  () => open.value,
  (isOpen) => {
    if (!isOpen) return
    const groupUrl = props.baseUrl ? normalizeBaseURL(props.baseUrl) : ''
    sourceId.value = ''
    form.models = []
    form.candidates = []
    modelSearch.value = ''
    displayMode.value = 'list'
    selectedIds.value = groupUrl
      ? (props.channels || [])
          .filter((ch) => !!ch.id && normalizeBaseURL(ch.base_url) === groupUrl)
          .map((ch) => ch.id)
      : []
  },
  { immediate: true },
)

const canSync = computed(
  () => selectedModelCount.value > 0 && selectedTargets.value.length > 0 && !props.pending,
)
function submit() {
  if (!canSync.value) return
  emit('sync', { channelIds: [...selectedIds.value], models: [...form.models] })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>同步模型列表</DialogTitle>
        <DialogDescription>
          把下面这份模型列表全量写入选中的 Key：目标 Key
          的模型目录会被替换成这份清单（新模型默认启用）， 不影响它们的 API Key
          与开关。想保留目标原有模型，请先在上方把它们加进列表。 目标只能是同一个平台（同 Base
          URL）下的其他 Key。
        </DialogDescription>
      </DialogHeader>

      <div class="space-y-5">
        <!-- 1. 模型列表编辑区（可从某个 Key 载入底稿） -->
        <div class="space-y-2">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <Label>模型列表</Label>
            <div class="flex items-center gap-2">
              <Select :model-value="sourceId" @update:model-value="loadFromChannel">
                <SelectTrigger class="w-[200px]" aria-label="从某个 Key 载入模型列表">
                  <SelectValue placeholder="从某个 Key 载入模型" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="end" :side-offset="4">
                  <SelectGroup>
                    <SelectItem :value="ALL_SOURCE_ID">
                      <span class="flex w-full items-center justify-between gap-2">
                        <span class="truncate">本平台全部 Key 的模型合集</span>
                        <span class="shrink-0 text-xs text-muted-foreground"
                          >{{ allModels.length }} 个</span
                        >
                      </span>
                    </SelectItem>
                    <SelectItem v-for="s in sourceOptions" :key="s.id" :value="s.id">
                      <TooltipProvider :delay-duration="150" :skip-delay-duration="100">
                        <Tooltip>
                          <TooltipTrigger as-child>
                            <span class="flex w-full items-center justify-between gap-2">
                              <span class="truncate">{{ keyLabel(s) }}</span>
                              <span class="shrink-0 text-xs text-muted-foreground"
                                >{{ keyModels(s).length }} 个</span
                              >
                            </span>
                          </TooltipTrigger>
                          <TooltipContent
                            side="right"
                            class="max-w-xs whitespace-normal"
                            :side-offset="6"
                          >
                            <div class="text-xs text-muted-foreground">
                              {{ sourcePreview(keyModels(s)) }}
                            </div>
                          </TooltipContent>
                        </Tooltip>
                      </TooltipProvider>
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Button
                type="button"
                variant="outline"
                size="sm"
                :disabled="!sourceId || sourceId === ALL_SOURCE_ID"
                title="重新载入该 Key 当前的模型清单（会覆盖当前编辑内容）"
                @click="reloadFromSource"
              >
                <RiFileCopyLine size="14" />载入
              </Button>
            </div>
          </div>

          <!-- 搜索 / 批量粘贴 / 自定义添加（与编辑器里的模型区一致，只是不藏在下拉里） -->
          <div class="space-y-2 rounded-md border border-border p-2">
            <div class="flex items-center gap-2">
              <Input
                v-model="modelSearch"
                placeholder="搜索或批量粘贴模型名（空格 / 换行 / 逗号分隔，回车添加自定义…）"
                class="flex-1"
                @keydown.enter.prevent="onModelSearchEnter"
              />
              <Button
                type="button"
                variant="outline"
                size="sm"
                class="shrink-0"
                :disabled="!allModels.length"
                title="把本平台全部 Key 已配置的模型并入列表"
                @click="importAllModels"
              >
                <RiFileCopyLine size="14" />获取模型
              </Button>
              <div class="flex items-center gap-1">
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  :disabled="!poolModels.length"
                  @click="selectAllModels"
                  >全选</Button
                >
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  :disabled="!poolModels.length"
                  @click="invertAllModels"
                  >反选</Button
                >
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  :disabled="!form.models.length"
                  @click="clearAllModels"
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
                  未在候选中找到，将作为自定义模型添加（共 {{ parsedSearchTokens.length }} 个）：
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
                <RiAddLine size="14" />自定义添加<span v-if="parsedSearchTokens.length > 1">
                  （{{ parsedSearchTokens.length }}）
                </span>
              </Button>
            </div>
          </div>
          <p class="text-xs text-muted-foreground">
            已选 {{ selectedModelCount }} 个模型；这些模型会被写入下面的目标 Key。
          </p>
        </div>

        <!-- 2. 目标 Key 多选 -->
        <div class="space-y-2">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <Label>同步到这些 Key</Label>
            <div class="flex items-center gap-3">
              <div class="flex rounded-md border border-border p-0.5">
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  :class="displayMode === 'list' ? 'bg-muted' : ''"
                  @click="displayMode = 'list'"
                  >列表</Button
                >
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  :class="displayMode === 'tag' ? 'bg-muted' : ''"
                  @click="displayMode = 'tag'"
                  >标签</Button
                >
              </div>
              <BulkSelectButtons
                :all-selected="allSelected"
                :can-operate="targetIds.length > 0"
                :has-selection="selectedIds.length > 0"
                @select-all="selectAllTargets"
                @invert="invertTargets"
                @clear="clearTargets"
              />
            </div>
          </div>

          <div v-if="targetKeys.length" class="space-y-2">
            <!-- 列表模式：一行一个 Key，行内平铺该 Key 当前的模型 -->
            <div v-if="displayMode === 'list'" class="space-y-1">
              <div
                v-for="group in targetGroups"
                :key="group.baseUrl"
                class="rounded-md border border-border"
              >
                <div
                  class="border-b border-border px-2 py-1 text-xs font-medium text-muted-foreground"
                >
                  {{ groupTitle(group) }}
                  <span class="font-mono">{{ group.baseUrl }}</span>
                </div>
                <div class="divide-y divide-border">
                  <label
                    v-for="key in group.keys"
                    :key="key.id"
                    class="flex cursor-pointer items-start gap-3 px-3 py-2 transition-colors hover:bg-muted/60"
                  >
                    <Checkbox
                      class="mt-0.5"
                      :model-value="isTargetSelected(key.id)"
                      @update:model-value="toggleTarget(key.id)"
                    />
                    <span class="min-w-0 flex-1">
                      <span class="block truncate text-sm font-medium">{{ keyLabel(key) }}</span>
                      <span class="mt-1 flex flex-wrap gap-1">
                        <Badge
                          v-for="m in keyModels(key)"
                          :key="m"
                          variant="secondary"
                          class="font-mono text-[11px]"
                          >{{ m }}</Badge
                        >
                        <span v-if="!keyModels(key).length" class="text-xs text-muted-foreground"
                          >尚未配置模型</span
                        >
                      </span>
                    </span>
                  </label>
                </div>
              </div>
            </div>

            <!-- 标签模式：一个 Key 一个标签，悬停看模型清单 -->
            <div
              v-else
              class="flex max-h-56 flex-wrap gap-2 overflow-y-auto rounded-md border border-border p-3"
            >
              <template v-for="key in targetKeys" :key="key.id">
                <Tooltip :delay-duration="300">
                  <TooltipTrigger as-child>
                    <Button
                      type="button"
                      size="sm"
                      :variant="isTargetSelected(key.id) ? 'default' : 'outline'"
                      @click="toggleTarget(key.id)"
                    >
                      {{ keyLabel(key) }}
                      <span class="ml-1 text-xs opacity-70">{{ keyModels(key).length }}</span>
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent side="bottom" class="max-w-md whitespace-normal break-words">
                    <div class="text-xs">
                      <div class="mb-1 font-mono text-muted-foreground">{{ key.base_url }}</div>
                      <div v-if="keyModels(key).length" class="flex flex-wrap gap-1">
                        <span
                          v-for="m in keyModels(key).slice(0, 40)"
                          :key="m"
                          class="rounded bg-muted px-1 font-mono"
                          >{{ m }}</span
                        >
                        <span v-if="keyModels(key).length > 40" class="opacity-70"
                          >…等 {{ keyModels(key).length }} 个</span
                        >
                      </div>
                      <span v-else class="opacity-70">尚未配置模型</span>
                    </div>
                  </TooltipContent>
                </Tooltip>
              </template>
            </div>
          </div>
          <p v-else class="rounded-md border border-border px-3 py-4 text-sm text-muted-foreground">
            没有可同步的目标 Key（这个平台下只有这一个 Key）。
          </p>
          <p class="text-xs text-muted-foreground">
            已选 {{ selectedTargets.length }} / {{ targetIds.length }} 个 Key；载入源的那个 Key
            不在目标里。
          </p>
        </div>
      </div>

      <DialogFooter>
        <Button type="button" :disabled="!canSync" @click="submit">
          <RiLoader4Line v-if="pending" class="animate-spin" size="16" />{{
            pending ? '同步中' : `同步到 ${selectedTargets.length} 个 Key`
          }}
        </Button>
        <Button type="button" variant="outline" :disabled="pending" @click="open = false"
          >取消</Button
        >
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
