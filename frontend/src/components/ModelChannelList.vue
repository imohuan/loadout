<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowUpLine,
  RiCheckLine,
  RiDeleteBinLine,
} from '@remixicon/vue'
import type { Channel, ModelChannelItem } from '@/lib/types'
import { normalizeBaseURL } from '@/composables/useChannels'
import { formatChannelRef } from '@/composables/useChannelRef'
import ChannelGroupPicker from '@/components/ChannelGroupPicker.vue'

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

// clearModelOnChannelChange：渠道选择变化时清空该行模型。
// 渠道字段通过 ChannelGroupPicker 写回 item，这里监听行级字段变化触发清空。
watch(
  () =>
    model.value.map((item) => [
      item.channel_id,
      item.channel_ids?.join(','),
      item.channel_base_url,
    ]),
  (curr, prev) => {
    if (!props.clearModelOnChannelChange) return
    prev.forEach((ch, i) => {
      if (ch.join('|') !== (curr[i] || []).join('|') && model.value[i]) {
        model.value[i].model = ''
      }
    })
  },
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

// ===== 渠道选择：由 ChannelGroupPicker 组件负责（按 base_url 分组的多选）=====

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
  const item = model.value[index]
  if (!item) return props.requireChannelForModel ? [] : allModels.value
  let ids: string[]
  if (item.channel_base_url) {
    // 渠道级：组内所有 Key。
    ids = props.channels
      .filter((c) => normalizeBaseURL(c.base_url) === normalizeBaseURL(item.channel_base_url!))
      .map((c) => c.id)
  } else {
    ids = item.channel_ids?.length ? item.channel_ids : item.channel_id ? [item.channel_id] : []
  }
  if (!ids.length) return props.requireChannelForModel ? [] : allModels.value
  const set = new Set<string>()
  for (const ch of props.channels) {
    if (!ids.includes(ch.id)) continue
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
            :class="
              item.channel_id || item.channel_ids?.length || item.channel_base_url
                ? ''
                : 'text-muted-foreground'
            "
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
          <ChannelGroupPicker
            :model-value="item"
            @update:model-value="Object.assign(item, $event)"
            :channels="channels"
            :allow-auto="allowAutoChannel"
            :multi-select="multiSelect"
            layout="vertical"
          />
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
            :disabled="
              requireChannelForModel &&
              !item.channel_id &&
              !item.channel_ids?.length &&
              !item.channel_base_url
            "
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
      <TooltipProvider v-if="showMove" :delay-duration="1000">
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
      <TooltipProvider v-if="showMove" :delay-duration="1000">
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
      <TooltipProvider v-if="showRemove" :delay-duration="1000">
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
