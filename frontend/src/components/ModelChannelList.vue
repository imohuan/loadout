<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowUpLine,
  RiCheckLine,
  RiDeleteBinLine,
} from '@remixicon/vue'
import type { Channel, ModelChannelItem } from '@/lib/types'

const props = withDefaults(
  defineProps<{
    channels: Channel[]
    // 渠道下拉是否含「自动路由」（channel_id 为空）。
    allowAutoChannel?: boolean
    // 模型是否允许自定义输入（搜索无结果时可手动添加）。
    allowCustomModel?: boolean
    // 未选渠道时模型候选是否为空（true = 必须先选渠道才能选模型）。
    requireChannelForModel?: boolean
    // 切换渠道后是否清空已选模型。
    clearModelOnChannelChange?: boolean
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
function modelCandidates(channelId: string): string[] {
  if (channelId) {
    const ch = props.channels.find((c) => c.id === channelId)
    if (ch?.models?.length) return ch.models
  }
  return props.requireChannelForModel ? [] : allModels.value
}

// 每行的下拉开关与搜索词（并行数组；deep watch 同步 v-model 的变化）。
const modelOpen = reactive<boolean[]>([])
const modelSearch = reactive<string[]>([])

watch(
  () => model.value,
  (list) => {
    modelOpen.length = 0
    modelSearch.length = 0
    list.forEach(() => {
      modelOpen.push(false)
      modelSearch.push('')
    })
  },
  { immediate: true, deep: true },
)

const placeholder = computed(() =>
  props.allowCustomModel ? '选择模型（可搜索 / 自定义）' : '选择模型（可搜索）',
)

function addItem() {
  model.value.push({ model: '', channel_id: '' })
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
function onChannelChange(item: ModelChannelItem, value: string) {
  const channelId = props.allowAutoChannel && value === 'auto' ? '' : value
  if (channelId === item.channel_id) return
  item.channel_id = channelId
  if (props.clearModelOnChannelChange) item.model = ''
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
      <Popover v-model:open="modelOpen[index]">
        <PopoverTrigger as-child>
          <Button
            type="button"
            variant="outline"
            class="flex-1 justify-start font-normal"
            :class="item.model ? '' : 'text-muted-foreground'"
            :disabled="requireChannelForModel && !item.channel_id"
          >
            {{ item.model || placeholder }}
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
                  v-for="m in modelCandidates(item.channel_id)"
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
      <Select
        :model-value="allowAutoChannel ? item.channel_id || 'auto' : item.channel_id"
        @update:model-value="(value: string) => onChannelChange(item, value)"
      >
        <SelectTrigger class="w-56">
          <SelectValue :placeholder="allowAutoChannel ? '自动路由' : '选择渠道'" />
        </SelectTrigger>
        <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
          <SelectGroup>
            <SelectItem v-if="allowAutoChannel" value="auto">自动路由（按模型找渠道）</SelectItem>
            <SelectItem v-for="channel in channels" :key="channel.id" :value="channel.id">
              {{ channel.name }}
            </SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
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
