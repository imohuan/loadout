<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { RiCloseLine, RiSearchLine } from '@remixicon/vue'
import ModelChannelList from '@/components/ModelChannelList.vue'
import SensitiveWordList from '@/components/capability-routes/SensitiveWordList.vue'
import type { CapabilityRoute, Channel, ModelChannelItem, SensitiveReplacement } from '@/lib/types'

const props = defineProps<{
  route?: CapabilityRoute
  channels: Channel[]
  pending?: boolean
}>()
const emit = defineEmits<{ save: [value: CapabilityRoute]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })

// 通用全匹配渠道：路由对所有渠道生效（与模型 `*` 通配同语义）。
const ALL_CHANNELS = '*'

// 能力常量与文案。
const CAP_VISION = 'vision'
const CAP_SENSITIVE = 'sensitive_filter'

const form = reactive<{
  models: string[]
  channel_ids: string[]
  capability: string
  route: string
  viaOptions: ModelChannelItem[]
  replacements: SensitiveReplacement[]
}>({
  models: [],
  channel_ids: [],
  capability: CAP_VISION,
  route: 'proxy',
  viaOptions: [{ model: '', channel_id: '', channel_ids: [] }],
  replacements: [{ from: '', to: '', regex: false }],
})

// 全部可用模型：所有渠道模型目录去重排序（目标模型与候选下拉共用）。
const allModels = computed(() => {
  const set = new Set<string>()
  for (const channel of props.channels) {
    for (const model of channel.models || []) set.add(model)
  }
  return [...set].sort()
})
function channelName(id: string) {
  return props.channels.find((c) => c.id === id)?.name || id
}

watch(
  () => props.route,
  (route) => {
    Object.assign(form, {
      models: route?.models ? [...route.models] : [],
      channel_ids: route?.channel_ids?.length ? [...route.channel_ids] : [],
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
  } else {
    form.viaOptions = [{ model: '', channel_id: '', channel_ids: [] }]
  }
}

// ===== 目标渠道（多选；`*` = 通用全匹配，与具体渠道互斥；空 = 全渠道，兼容旧数据）=====
const channelOpen = ref(false)
const isAllChannels = computed(() => form.channel_ids.includes(ALL_CHANNELS))
function toggleChannel(id: string) {
  if (id === ALL_CHANNELS) {
    // 通用全匹配独占：选中则清空具体渠道，再次点击回到空（仍为全渠道）。
    form.channel_ids = isAllChannels.value ? [] : [ALL_CHANNELS]
    return
  }
  // 选具体渠道时移除 `*`（从全匹配切到限定渠道）。
  const next = form.channel_ids.filter((c) => c !== ALL_CHANNELS)
  const i = next.indexOf(id)
  if (i >= 0) next.splice(i, 1)
  else next.push(id)
  form.channel_ids = next
}
const channelTriggerLabel = computed(() => {
  if (isAllChannels.value || !form.channel_ids.length) return '通用（全匹配）'
  const names = form.channel_ids.map((id) => channelName(id)).join('、')
  return `已选 ${form.channel_ids.length} 个渠道：${names}`
})
// 目标模型候选：按所选渠道过滤（通用/空 = 全渠道模型并集）。
const candidateModels = computed(() => {
  if (isAllChannels.value || !form.channel_ids.length) return allModels.value
  const set = new Set<string>()
  for (const id of form.channel_ids) {
    const ch = props.channels.find((c) => c.id === id)
    for (const model of ch?.models || []) set.add(model)
  }
  return [...set].sort()
})

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
// 目标模型下拉内的过滤列表（tag 网格用，基于渠道过滤后的候选）。
const filteredModels = computed(() => {
  const q = targetSearch.value.trim().toLowerCase()
  if (!q) return candidateModels.value
  return candidateModels.value.filter((m) => m.toLowerCase().includes(q))
})

// ===== 能力与路由方式选项 =====
const capabilityOptions = [
  { value: CAP_VISION, label: 'vision（视觉）' },
  { value: CAP_SENSITIVE, label: 'sensitive_filter（敏感词过滤）' },
]
// 路由方式选项：vision 保持两态；敏感词过滤提供三态（error = 命中敏感词直接拒绝）。
const routeOptions = computed(() =>
  form.capability === CAP_SENSITIVE
    ? [
        { value: 'proxy', label: '附加代理（替换）' },
        { value: 'native', label: '原生透传' },
        { value: 'error', label: '命中拒绝' },
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
      proxy: '请求体按替换规则整体过滤：敏感词被替换后再转发给目标模型；整体替换若破坏 JSON，自动降级为只替换 messages 文本。',
      native: '请求体原样透传，不做敏感词过滤（适合通配规则下的精确豁免）。',
      error: '请求体命中任一敏感词规则直接拒绝，不转发上游（依赖下方规则列表）。',
    }[form.route]
  }
  return {
    proxy: '图片被拦截，候选视觉模型看图生成文字描述后再转发给目标模型。',
    native: '图片原样透传给目标模型自行处理（适合支持视觉的模型，或通配规则下的精确豁免）。',
  }[form.route]
})

function submit() {
  if (!form.models.length) return
  const isSensitive = form.capability === CAP_SENSITIVE
  if (form.route === 'proxy') {
    if (isSensitive) {
      if (!form.replacements.some((r) => r.from.trim())) return
    } else if (!form.viaOptions.some((o) => o.model.trim())) {
      return
    }
  }
  if (form.route === 'error' && isSensitive) {
    // error 路由依赖 replacements 做命中判断，必须有规则。
    if (!form.replacements.some((r) => r.from.trim())) return
  }
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
  // channel_ids 规范化：含 `*` 时独占（通用全匹配），其余去重。
  const channel_ids = isAllChannels.value ? [ALL_CHANNELS] : [...new Set(form.channel_ids)]
  emit('save', {
    models: [...form.models],
    channel_ids,
    capability: form.capability,
    route: form.route,
    via_options: viaOptions,
    replacements,
  })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>{{ route ? '编辑能力路由' : '添加能力路由' }}</DialogTitle>
        <DialogDescription>
          给目标模型附加能力：视觉（图片识别替换）或敏感词过滤（请求体整体替换）。
        </DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="space-y-2">
          <Label>目标渠道</Label>
          <Popover v-model:open="channelOpen">
            <PopoverTrigger as-child>
              <Button type="button" variant="outline" class="w-full justify-between font-normal">
                <span class="truncate text-muted-foreground">{{ channelTriggerLabel }}</span>
                <RiSearchLine class="size-4 shrink-0 opacity-50" />
              </Button>
            </PopoverTrigger>
            <PopoverContent class="w-[var(--reka-popper-anchor-width)] p-2" align="start">
              <div class="space-y-2">
                <div
                  class="flex max-h-56 flex-wrap gap-1.5 overflow-y-auto rounded-md border border-border p-2"
                >
                  <Button
                    type="button"
                    size="sm"
                    :variant="isAllChannels ? 'default' : 'outline'"
                    @click="toggleChannel(ALL_CHANNELS)"
                    >通用（全匹配）</Button
                  >
                  <Button
                    v-for="channel in channels"
                    :key="channel.id"
                    type="button"
                    size="sm"
                    :variant="
                      !isAllChannels && form.channel_ids.includes(channel.id)
                        ? 'default'
                        : 'outline'
                    "
                    @click="toggleChannel(channel.id)"
                    >{{ channel.name }}</Button
                  >
                </div>
                <p class="text-xs text-muted-foreground">
                  通用（全匹配）=
                  路由对所有渠道生效；也可多选/单选具体渠道，仅这些渠道上的目标模型命中。
                </p>
              </div>
            </PopoverContent>
          </Popover>
          <div v-if="form.channel_ids.length" class="flex flex-wrap gap-1.5">
            <Badge
              v-for="id in form.channel_ids"
              :key="id"
              variant="secondary"
              class="gap-1 py-0 pr-1"
            >
              {{ id === ALL_CHANNELS ? '通用（全匹配）' : channelName(id) }}
              <button
                type="button"
                class="rounded-full p-0.5 hover:bg-muted hover:text-destructive"
                aria-label="移除"
                @click="toggleChannel(id)"
              >
                <RiCloseLine size="12" />
              </button>
            </Badge>
          </div>
        </div>
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
            支持 <code class="font-mono">*</code> 通配与前缀匹配；候选随目标渠道过滤（通用/空 =
            全部渠道模型）。 未命中默认原生透传。
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
        v-if="form.capability === CAP_SENSITIVE && form.route !== 'native'"
        class="space-y-2"
      >
        <Label>敏感词过滤列表（按从上到下顺序替换 / 命中判断）</Label>
        <SensitiveWordList v-model="form.replacements" add-label="添加规则" />
      </div>
      <div v-else-if="form.route === 'proxy'" class="space-y-2">
        <Label>视觉候选（从上到下依次请求，失败换下一个）</Label>
        <ModelChannelList v-model="form.viaOptions" :channels="channels" add-label="添加候选" />
      </div>
      <DialogFooter class="sm:justify-between">
        <p class="text-xs text-muted-foreground">{{ routeHint }}</p>
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
