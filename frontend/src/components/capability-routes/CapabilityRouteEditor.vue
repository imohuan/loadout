<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowUpLine,
  RiCheckLine,
  RiCloseLine,
  RiDeleteBinLine,
  RiSearchLine,
} from '@remixicon/vue'
import type { CapabilityRoute, Channel, ViaOption } from '@/lib/types'

const props = defineProps<{
  route?: CapabilityRoute
  channels: Channel[]
  pending?: boolean
}>()
const emit = defineEmits<{ save: [value: CapabilityRoute]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })

const form = reactive<{
  models: string[]
  capability: string
  route: string
  viaOptions: ViaOption[]
}>({
  models: [],
  capability: 'vision',
  route: 'proxy',
  viaOptions: [{ via_model: '', channel_id: '' }],
})

// 全部可用模型：所有渠道模型目录去重排序（目标模型与候选下拉共用）。
const allModels = computed(() => {
  const set = new Set<string>()
  for (const channel of props.channels) {
    for (const model of channel.models || []) set.add(model)
  }
  return [...set].sort()
})
function modelsForChannel(channelId: string): string[] {
  if (channelId) {
    const ch = props.channels.find((c) => c.id === channelId)
    if (ch?.models?.length) return ch.models
  }
  return allModels.value
}

// 视觉候选每行的下拉开关与搜索词（必须在 watch 前声明，immediate 回调会用到）。
const viaOpen = reactive<boolean[]>([false])
const viaSearch = reactive<string[]>([''])

watch(
  () => props.route,
  (route) => {
    Object.assign(form, {
      models: route?.models ? [...route.models] : [],
      capability: route?.capability || 'vision',
      route: route?.route || 'proxy',
      viaOptions: route?.via_options?.length
        ? route.via_options.map((o) => ({
            via_model: o.via_model || '',
            channel_id: o.channel_id || '',
          }))
        : [{ via_model: '', channel_id: '' }],
    })
    viaOpen.length = 0
    viaSearch.length = 0
    form.viaOptions.forEach(() => {
      viaOpen.push(false)
      viaSearch.push('')
    })
  },
  { immediate: true },
)

// ===== 目标模型（下拉多选 + 搜索 + 自定义）=====
const targetOpen = ref(false)
const targetSearch = ref('')
function toggleModel(model: string) {
  const i = form.models.indexOf(model)
  if (i >= 0) form.models.splice(i, 1)
  else form.models.push(model)
}
function addCustomModel() {
  const name = targetSearch.value.trim()
  if (!name || form.models.includes(name)) return
  form.models.push(name)
  targetSearch.value = ''
}
// 回车直接添加（避免误触表单提交）。
function onTargetSearchEnter() {
  if (!targetSearch.value.trim()) return
  addCustomModel()
}
// 目标模型下拉内的过滤列表（tag 网格用）。
const filteredModels = computed(() => {
  const q = targetSearch.value.trim().toLowerCase()
  if (!q) return allModels.value
  return allModels.value.filter((m) => m.toLowerCase().includes(q))
})

// ===== 视觉候选（下拉单选 + 搜索 + 自定义）=====
function addViaOption() {
  form.viaOptions.push({ via_model: '', channel_id: '' })
  viaOpen.push(false)
  viaSearch.push('')
}
function removeViaOption(index: number) {
  if (form.viaOptions.length > 1) {
    form.viaOptions.splice(index, 1)
    viaOpen.splice(index, 1)
    viaSearch.splice(index, 1)
  }
}
// 上下移动候选（排序），viaOpen/viaSearch 为并行数组需同步移动。
function moveViaOption(index: number, direction: -1 | 1) {
  const target = index + direction
  if (target < 0 || target >= form.viaOptions.length) return
  const [option] = form.viaOptions.splice(index, 1)
  form.viaOptions.splice(target, 0, option)
  const [open] = viaOpen.splice(index, 1)
  viaOpen.splice(target, 0, open)
  const [search] = viaSearch.splice(index, 1)
  viaSearch.splice(target, 0, search)
}
function modelsFor(index: number): string[] {
  return modelsForChannel(form.viaOptions[index]?.channel_id || '')
}
function selectVia(index: number, model: string) {
  form.viaOptions[index]!.via_model = model
  viaOpen[index] = false
}
function addCustomVia(index: number) {
  const name = viaSearch[index].trim()
  if (!name) return
  form.viaOptions[index]!.via_model = name
  viaOpen[index] = false
}
function onViaSearchEnter(index: number) {
  if (!viaSearch[index].trim()) return
  addCustomVia(index)
}

const capabilityOptions = [{ value: 'vision', label: 'vision（视觉）' }]
const routeOptions = [
  { value: 'proxy', label: '附加代理' },
  { value: 'native', label: '原生透传' },
]
// 路由方式的一句话说明，footer 左侧动态展示。
const routeHint: Record<string, string> = {
  proxy: '图片被拦截，候选视觉模型看图生成文字描述后再转发给目标模型。',
  native: '图片原样透传给目标模型自行处理（适合支持视觉的模型，或通配规则下的精确豁免）。',
}

function submit() {
  if (!form.models.length) return
  if (form.route === 'proxy' && !form.viaOptions.some((o) => o.via_model.trim())) return
  const viaOptions =
    form.route === 'proxy'
      ? form.viaOptions
          .map((o) => ({ via_model: o.via_model.trim(), channel_id: o.channel_id || '' }))
          .filter((o) => o.via_model)
      : []
  emit('save', {
    models: [...form.models],
    capability: form.capability,
    route: form.route,
    via_options: viaOptions,
  })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>{{ route ? '编辑能力路由' : '添加能力路由' }}</DialogTitle>
        <DialogDescription>
          给不支持视觉的模型附加视觉能力：拦截图片 → 按候选依次调视觉模型 → 文字描述替换图片。
        </DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="space-y-2">
          <Label>目标模型</Label>
          <Popover v-model:open="targetOpen">
            <PopoverTrigger as-child>
              <Button type="button" variant="outline" class="w-full justify-between font-normal">
                <span v-if="form.models.length" class="text-muted-foreground"
                  >已选 {{ form.models.length }} 个模型</span
                ><span v-else class="text-muted-foreground">选择目标模型（可搜索 / 自定义）</span
                ><RiSearchLine class="size-4 shrink-0 opacity-50" />
              </Button>
            </PopoverTrigger>
            <PopoverContent class="w-[var(--reka-popper-anchor-width)] p-2" align="start">
              <div class="space-y-2">
                <Input
                  v-model="targetSearch"
                  placeholder="搜索模型…"
                  @keydown.esc="targetOpen = false"
                  @keydown.enter.prevent="onTargetSearchEnter"
                />
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
                  <p class="text-xs text-muted-foreground">未找到「{{ targetSearch }}」</p>
                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    :disabled="!targetSearch.trim()"
                    @click="addCustomModel"
                    >自定义添加</Button
                  >
                </div>
              </div>
            </PopoverContent>
          </Popover>
          <div v-if="form.models.length" class="flex flex-wrap gap-1.5">
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
          <p class="text-xs text-muted-foreground">
            支持 <code class="font-mono">*</code> 通配与前缀匹配；未命中默认原生透传。
          </p>
        </div>
        <div class="grid gap-4 sm:grid-cols-2">
          <div class="space-y-2">
            <Label>能力</Label>
            <Select v-model="form.capability">
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
        <div v-if="form.route === 'proxy'" class="space-y-2">
          <Label>视觉候选（从上到下依次请求，失败换下一个）</Label>
          <div v-for="(option, index) in form.viaOptions" :key="index" class="flex gap-2">
            <Popover v-model:open="viaOpen[index]">
              <PopoverTrigger as-child>
                <Button
                  type="button"
                  variant="outline"
                  class="flex-1 justify-start font-normal"
                  :class="option.via_model ? '' : 'text-muted-foreground'"
                >
                  {{ option.via_model || '选择视觉模型（可搜索 / 自定义）' }}
                </Button>
              </PopoverTrigger>
              <PopoverContent class="w-80 p-0" align="start">
                <Command>
                  <CommandInput
                    v-model="viaSearch[index]"
                    placeholder="搜索模型…"
                    @keydown.enter.prevent="onViaSearchEnter(index)"
                  />
                  <CommandList>
                    <CommandEmpty>
                      <div class="flex items-center justify-between gap-2 px-2 py-1.5">
                        <span class="min-w-0 truncate text-xs text-muted-foreground"
                          >未找到「{{ viaSearch[index] }}」</span
                        >
                        <Button
                          type="button"
                          size="sm"
                          variant="outline"
                          :disabled="!viaSearch[index].trim()"
                          @click="addCustomVia(index)"
                          >自定义添加</Button
                        >
                      </div>
                    </CommandEmpty>
                    <CommandGroup>
                      <CommandItem
                        v-for="m in modelsFor(index)"
                        :key="m"
                        :value="m"
                        @select="selectVia(index, m)"
                      >
                        <RiCheckLine
                          :class="option.via_model === m ? 'opacity-100' : 'opacity-0'"
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
              :model-value="option.channel_id || 'auto'"
              @update:model-value="
                (value: string) => (option.channel_id = value === 'auto' ? '' : value)
              "
            >
              <SelectTrigger class="w-56"><SelectValue placeholder="自动路由" /></SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem value="auto">自动路由（按视觉模型找渠道）</SelectItem>
                  <SelectItem v-for="channel in channels" :key="channel.id" :value="channel.id">
                    {{ channel.name }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <TooltipProvider
              ><Tooltip
                ><TooltipTrigger as-child
                  ><Button
                    variant="ghost"
                    size="icon"
                    type="button"
                    :disabled="index === 0"
                    aria-label="上移候选"
                    @click="moveViaOption(index, -1)"
                    ><RiArrowUpLine size="16" /></Button></TooltipTrigger
                ><TooltipContent>上移</TooltipContent></Tooltip
              ></TooltipProvider
            >
            <TooltipProvider
              ><Tooltip
                ><TooltipTrigger as-child
                  ><Button
                    variant="ghost"
                    size="icon"
                    type="button"
                    :disabled="index === form.viaOptions.length - 1"
                    aria-label="下移候选"
                    @click="moveViaOption(index, 1)"
                    ><RiArrowDownLine size="16" /></Button></TooltipTrigger
                ><TooltipContent>下移</TooltipContent></Tooltip
              ></TooltipProvider
            >
            <TooltipProvider
              ><Tooltip
                ><TooltipTrigger as-child
                  ><Button
                    variant="ghost"
                    size="icon"
                    type="button"
                    :disabled="form.viaOptions.length === 1"
                    aria-label="移除候选"
                    @click="removeViaOption(index)"
                    ><RiDeleteBinLine size="16" /></Button></TooltipTrigger
                ><TooltipContent>移除候选</TooltipContent></Tooltip
              ></TooltipProvider
            >
          </div>
          <Button type="button" variant="outline" size="sm" @click="addViaOption"
            ><RiAddLine size="16" />添加候选</Button
          >
        </div>
        <DialogFooter class="sm:justify-between">
          <p class="text-xs text-muted-foreground">{{ routeHint[form.route] }}</p>
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
