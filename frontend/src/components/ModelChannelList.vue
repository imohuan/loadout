<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiCheckLine,
  RiDeleteBinLine,
  RiKey2Line,
  RiStackLine,
} from '@remixicon/vue'
import type { Channel, ModelChannelItem } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from '@/composables/useChannels'
import { formatChannelRef } from '@/composables/useChannelRef'

const props = withDefaults(
  defineProps<{
    channels: Channel[]
    // 渠道选择是否含「自动路由」（channel_id 为空）。
    allowAutoChannel?: boolean
    // 模型是否允许自定义输入（搜索无结果时可手动添加）。
    allowCustomModel?: boolean
    // 未选渠道时模型候选是否为空（true = 必须先选渠道才能选模型）。
    requireChannelForModel?: boolean
    // 切换渠道后是否清空已选模型。
    clearModelOnChannelChange?: boolean
    // 渠道选择是否多选（false = 单选，勾选新 Key 自动取消旧 Key）。
    multiSelect?: boolean
    // 是否允许整组全勾时折叠为渠道级（channel_base_url）。
    enableChannelLevel?: boolean
    showMove?: boolean
    showRemove?: boolean
    showAdd?: boolean
    showIndex?: boolean
    addLabel?: string
  }>(),
  {
    allowAutoChannel: true,
    allowCustomModel: true,
    requireChannelForModel: false,
    clearModelOnChannelChange: false,
    multiSelect: true,
    enableChannelLevel: true,
    showMove: true,
    showRemove: true,
    showAdd: true,
    showIndex: false,
    addLabel: '添加候选',
  },
)

const model = defineModel<ModelChannelItem[]>({ required: true })

// 全部可用模型：所有渠道模型目录去重排序（未指定渠道时的候选）。
const allModels = computed(() => {
  const set = new Set<string>()
  for (const channel of props.channels) {
    for (const m of channel.models || []) set.add(m)
  }
  return [...set].sort()
})

// 渠道按 base_url 分组（渠道组 = 一个渠道；组内 = 各 Key）。
const channelGroups = computed(() => groupChannelsByBaseURL(props.channels))

// 组标题：渠道名 · Key 数量。
function groupLabel(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const first = group.keys[0]
  const title = first?.channel_name || first?.name || group.baseUrl
  return `${title} · ${group.keys.length} 个 Key`
}

// 每行的下拉开关与搜索词（并行数组；只在行数变化时重建，
// 不能 deep watch——toggle 渠道/模型会改 item 属性，deep watch 会把 open 重置导致 popover 闪关）。
const modelOpen = reactive<boolean[]>([])
const modelSearch = reactive<string[]>([])
const channelOpen = reactive<boolean[]>([])

watch(
  () => model.value.length,
  (len) => {
    modelOpen.length = len
    modelSearch.length = len
    channelOpen.length = len
  },
  { immediate: true },
)

const placeholder = computed(() =>
  props.allowCustomModel ? '选择模型（可搜索 / 自定义）' : '选择模型（可搜索）',
)

function addItem() {
  model.value.push({ model: '', channel_id: '', channel_ids: [] })
}
function removeItem(index: number) {
  if (model.value.length > 1) model.value.splice(index, 1)
}
// 上下移动（排序）。
function moveItem(index: number, direction: -1 | 1) {
  const target = index + direction
  if (target < 0 || target >= model.value.length) return
  const [item] = model.value.splice(index, 1)
  model.value.splice(target, 0, item)
}
function selectModel(index: number, item: ModelChannelItem, name: string) {
  item.model = name
  modelOpen[index] = false
}
function addCustomModel(index: number, item: ModelChannelItem) {
  if (!props.allowCustomModel) return
  const name = modelSearch[index].trim()
  if (!name) return
  item.model = name
  modelOpen[index] = false
}
function onModelSearchEnter(index: number, item: ModelChannelItem) {
  if (!modelSearch[index].trim()) return
  addCustomModel(index, item)
}

// ===== 渠道选择：按渠道分组的多选（沿用 ModelTestView 预设下拉风格）=====

// 当前行的选中态：{ auto, ids }。ids = 已勾选 Key 的 id 集合。
function selectionOf(index: number): { auto: boolean; ids: Set<string> } {
  const item = model.value[index]
  if (!item) return { auto: true, ids: new Set() }
  if (item.channel_base_url) {
    const group = channelGroups.value.find(
      (g) => normalizeBaseURL(g.baseUrl) === normalizeBaseURL(item.channel_base_url!),
    )
    return { auto: false, ids: new Set((group?.keys || []).map((k) => k.id)) }
  }
  const ids = item.channel_ids?.length
    ? item.channel_ids
    : item.channel_id
      ? [item.channel_id]
      : []
  return { auto: ids.length === 0, ids: new Set(ids) }
}

// 当前行是否渠道级选中了指定组（只有 channel_base_url === group.baseUrl 才算渠道级）。
function isChannelLevelSelected(index: number, group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const item = model.value[index]
  if (!item?.channel_base_url) return false
  return normalizeBaseURL(item.channel_base_url) === normalizeBaseURL(group.baseUrl)
}

// 把勾选态写回 item（后端字段归一化）。
// 重要：渠道级（channel_base_url）与 Key 多选（channel_ids）是两个独立概念，互不转换。
//   - 渠道级：只能由点击渠道组标题触发。
//   - Key 多选：即使全部 Key 恰好被勾满，也仍然是 channel_ids，不会自动折叠为渠道级。
//   - 空：自动路由。
function applySelection(index: number, ids: Set<string>, auto: boolean) {
  const item = model.value[index]
  if (!item) return
  if (auto || ids.size === 0) {
    item.channel_id = ''
    item.channel_ids = []
    item.channel_base_url = ''
    if (props.clearModelOnChannelChange) item.model = ''
    return
  }
  // 单选模式：只保留最后勾选的 Key。
  if (!props.multiSelect && ids.size > 1) {
    const last = [...ids][ids.size - 1]
    ids = new Set([last])
  }
  // Key 多选：按渠道表顺序排列；绝不折叠为 channel_base_url。
  const ordered = channelGroups.value
    .flatMap((g) => g.keys.map((k) => k.id))
    .filter((id) => ids.has(id))
  item.channel_id = ''
  item.channel_ids = ordered
  item.channel_base_url = ''
  if (props.clearModelOnChannelChange) item.model = ''
}

// 设为渠道级（点击组标题触发）。再次点击同组 = 取消渠道级。
function setChannelLevel(index: number, group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const item = model.value[index]
  if (!item) return
  item.channel_id = ''
  item.channel_ids = []
  item.channel_base_url = group.baseUrl
  if (props.clearModelOnChannelChange) item.model = ''
}

// 当前勾选所属的渠道组（单渠道约束：一个目标只允许一个渠道组，组内可多选 Key）。
function activeGroupOf(index: number): ReturnType<typeof groupChannelsByBaseURL>[number] | undefined {
  const { ids } = selectionOf(index)
  for (const g of channelGroups.value) {
    if (g.keys.some((k) => ids.has(k.id))) return g
  }
  return undefined
}

function toggleKey(index: number, id: string) {
  const sel = selectionOf(index)
  const keyGroup = channelGroups.value.find((g) => g.keys.some((k) => k.id === id))
  const active = activeGroupOf(index)
  // 跨渠道勾选：切换到新渠道组（清空旧组选择），只保留新勾选的 Key。
  if (keyGroup && active && keyGroup.baseUrl !== active.baseUrl) {
    applySelection(index, new Set([id]), false)
    return
  }
  // 当前是渠道级 → 切到 Key 多选模式（保留组内所有 Key 的勾选视觉，仅切换当前 Key）。
  if (model.value[index]?.channel_base_url) {
    const groupIds = (active?.keys || []).map((k) => k.id)
    const next = new Set(groupIds)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    applySelection(index, next, false)
    return
  }
  if (sel.ids.has(id)) sel.ids.delete(id)
  else sel.ids.add(id)
  applySelection(index, sel.ids, false)
}
// 点击组标题 = 设为渠道级（再次点同组 = 取消）。
// 渠道级是独立概念：点组标题直接写 channel_base_url、清空 channel_ids / channel_id；
// Key 多选即使全部勾满也不会升级为渠道级。
function toggleGroup(index: number, group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (!group.keys.length) return
  if (isChannelLevelSelected(index, group)) {
    applySelection(index, new Set(), true)
    return
  }
  setChannelLevel(index, group)
}
function toggleAuto(index: number) {
  applySelection(index, new Set(), true)
}

// 渠道按钮展示文本：统一走 formatChannelRef 规范。
//   渠道级 → NewAPi；单 Key → NewAPi(Key1)；多 Key → NewAPi(Key1, Key2)
function channelTriggerLabel(index: number) {
  const item = model.value[index]
  if (!item) return props.allowAutoChannel ? '自动路由' : '选择渠道'
  const text = formatChannelRef(props.channels, item)
  if (text) return text
  return props.allowAutoChannel ? '自动路由（按模型找渠道）' : '选择渠道'
}

// 模型候选：按当前勾选的 Key 过滤（并集）。
function modelCandidates(index: number): string[] {
  const { auto, ids } = selectionOf(index)
  if (auto || ids.size === 0) return props.requireChannelForModel ? [] : allModels.value
  const set = new Set<string>()
  for (const ch of props.channels) {
    if (!ids.has(ch.id)) continue
    for (const m of ch.models || []) set.add(m)
  }
  return [...set].sort()
}
</script>

<template>
  <div class="space-y-2">
    <div v-for="(item, index) in model" :key="index" class="flex items-center gap-2">
      <span
        v-if="showIndex"
        class="flex h-9 w-8 shrink-0 items-center justify-center text-sm tabular-nums text-muted-foreground"
        >{{ index + 1 }}</span
      >
      <!-- 渠道选择：Popover + 渠道分组 tag 网格（多选） -->
      <Popover v-model:open="channelOpen[index]">
        <PopoverTrigger as-child>
          <Button
            type="button"
            variant="outline"
            class="w-56 shrink-0 justify-start font-normal"
            :class="item.channel_id || item.channel_ids?.length || item.channel_base_url ? '' : 'text-muted-foreground'"
          >
            <span class="truncate">{{ channelTriggerLabel(index) }}</span>
            <RiArrowDropDownLine size="24" class="ml-auto shrink-0 opacity-50" />
            <RiArrowDownSLine class="ml-auto size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent
          class="w-[var(--reka-popper-anchor-width)] p-3 max-h-80 overflow-y-auto"
          align="start"
          :side-offset="2"
        >
          <div v-if="allowAutoChannel" class="mb-3">
            <button
              type="button"
              class="w-full rounded-md border px-2 py-1.5 text-xs font-medium transition-colors"
              :class="
                !item.channel_id && !item.channel_ids?.length && !item.channel_base_url
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'border-border bg-background hover:bg-muted'
              "
              @click="toggleAuto(index)"
            >
              自动路由（按模型找渠道）
            </button>
          </div>
          <div class="space-y-1">
            <template v-for="group in channelGroups" :key="group.baseUrl">
              <div
                class="rounded-md border p-1 transition-colors"
                :class="
                  isChannelLevelSelected(index, group)
                    ? 'border-primary/40 bg-primary/5'
                    : 'border-transparent'
                "
              >
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <button
                      type="button"
                      class="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium transition-colors"
                      :class="
                        isChannelLevelSelected(index, group)
                          ? 'text-primary'
                          : 'text-muted-foreground hover:bg-muted/50'
                      "
                      :aria-label="`选择整组：${groupLabel(group)}`"
                      @click="toggleGroup(index, group)"
                    >
                      <RiStackLine
                        v-if="isChannelLevelSelected(index, group)"
                        class="size-3.5 shrink-0"
                      />
                      <RiKey2Line v-else class="size-3.5 shrink-0 opacity-50" />
                      <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
                      <RiCheckLine
                        v-if="isChannelLevelSelected(index, group)"
                        class="size-3.5 shrink-0"
                      />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent>点击选择整个渠道（含未来新增的 Key）</TooltipContent>
                </Tooltip>
              </TooltipProvider>
              <div class="mt-0.5 flex flex-wrap gap-1">
                <button
                  v-for="key in group.keys"
                  :key="key.id"
                  type="button"
                  class="rounded border px-1.5 py-0.5 text-xs font-medium transition-colors"
                  :class="
                    selectionOf(index).ids.has(key.id)
                      ? 'border-primary bg-primary text-primary-foreground'
                      : 'border-border bg-background hover:bg-muted'
                  "
                  @click="toggleKey(index, key.id)"
                >
                  {{ key.name }}
                </button>
              </div>
              </div>
            </template>
          </div>
          <p v-if="!channelGroups.length" class="px-2 py-3 text-sm text-muted-foreground">
            暂无可用渠道
          </p>
        </PopoverContent>
      </Popover>
      <!-- 模型选择 -->
      <Popover v-model:open="modelOpen[index]">
        <PopoverTrigger as-child>
          <Button
            type="button"
            variant="outline"
            class="flex-1 justify-start font-normal"
            :class="item.model ? '' : 'text-muted-foreground'"
            :disabled="requireChannelForModel && !item.channel_id && !item.channel_ids?.length && !item.channel_base_url"
          >
            <span class="truncate">{{ item.model || placeholder }}</span>
            <RiArrowDownSLine class="ml-auto size-4 shrink-0 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent class="w-80 p-0" align="start">
          <Command>
            <CommandInput
              v-model="modelSearch[index]"
              placeholder="搜索模型…"
              @keydown.enter.prevent="onModelSearchEnter(index, item)"
            />
            <CommandList>
              <CommandEmpty>
                <div
                  v-if="allowCustomModel"
                  class="flex items-center justify-between gap-2 px-2 py-1.5"
                >
                  <span class="min-w-0 truncate text-xs text-muted-foreground"
                    >未找到「{{ modelSearch[index] }}」</span
                  >
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    :disabled="!modelSearch[index].trim()"
                    @click="addCustomModel(index, item)"
                    >自定义添加</Button
                  >
                </div>
                <span v-else class="block px-2 py-1.5 text-xs text-muted-foreground"
                  >未找到「{{ modelSearch[index] }}」</span
                >
              </CommandEmpty>
              <CommandGroup>
                <CommandItem
                  v-for="m in modelCandidates(index)"
                  :key="m"
                  :value="m"
                  @select="selectModel(index, item, m)"
                >
                  <RiCheckLine
                    :class="item.model === m ? 'opacity-100' : 'opacity-0'"
                    class="mr-2 size-4"
                  />
                  <span class="truncate">{{ m }}</span>
                </CommandItem>
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
      <TooltipProvider v-if="showMove">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              type="button"
              :disabled="index === 0"
              aria-label="上移"
              @click="moveItem(index, -1)"
              ><RiArrowUpLine size="16" /></Button
          ></TooltipTrigger>
          <TooltipContent>上移</TooltipContent>
        </Tooltip>
      </TooltipProvider>
      <TooltipProvider v-if="showMove">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              type="button"
              :disabled="index === model.length - 1"
              aria-label="下移"
              @click="moveItem(index, 1)"
              ><RiArrowDownLine size="16" /></Button
          ></TooltipTrigger>
          <TooltipContent>下移</TooltipContent>
        </Tooltip>
      </TooltipProvider>
      <TooltipProvider v-if="showRemove">
        <Tooltip>
          <TooltipTrigger as-child>
            <Button
              variant="ghost"
              size="icon"
              type="button"
              :disabled="model.length === 1"
              aria-label="移除"
              @click="removeItem(index)"
              ><RiDeleteBinLine size="16" /></Button
          ></TooltipTrigger>
          <TooltipContent>移除</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
    <Button v-if="showAdd" type="button" variant="outline" size="sm" @click="addItem"
      ><RiAddLine size="16" />{{ addLabel }}</Button
    >
  </div>
</template>
