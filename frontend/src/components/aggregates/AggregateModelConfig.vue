<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { RiArrowDownSLine, RiSparklingLine } from '@remixicon/vue'
import type { AggregateCapability, AggregateConfig, CatalogModel } from '@/lib/types'
import { useAggregates } from '@/composables/useAggregates'

const model = defineModel<AggregateConfig>({ required: true })

// 能力多选框：取值与后端 capability 常量一一对应。
const CAPABILITIES: { value: AggregateCapability; label: string; hint: string }[] = [
  {
    value: 'vision',
    label: '视觉',
    hint: '允许客户端发图（输出 supports_vision 与 input_modalities）',
  },
  { value: 'reasoning', label: '推理', hint: '声明支持思考（输出 supports_reasoning）' },
  { value: 'tool_use', label: '工具调用', hint: '声明支持函数调用（输出 supports_tool_use）' },
]

const aggregateService = useAggregates()

// 数字输入框用字符串绑定：空串表示「不声明该字段」（与 0 区分不了，直接视为未填）。
const contextLength = ref('')
const maxOutputTokens = ref('')
const capabilities = reactive<AggregateCapability[]>([])

// 规范化：输入框 → 配置对象（只有正数才声明，能力按顺序保留）。
function normalize(context: string, output: string, caps: AggregateCapability[]): AggregateConfig {
  const next: AggregateConfig = {}
  const ctx = Number.parseInt(context.trim(), 10)
  if (Number.isFinite(ctx) && ctx > 0) next.context_length = ctx
  const out = Number.parseInt(output.trim(), 10)
  if (Number.isFinite(out) && out > 0) next.max_output_tokens = out
  if (caps.length) next.capabilities = [...caps]
  return next
}

// 内容相同即视为无变化。两个 watcher 互相回写时必须靠它断开环路
// （否则每次赋值都产生新对象，触发对方再赋值，来回死循环）。
function sameConfig(a: AggregateConfig | undefined, b: AggregateConfig): boolean {
  const ac = a?.context_length || 0
  const ao = a?.max_output_tokens || 0
  const bc = b.context_length || 0
  const bo = b.max_output_tokens || 0
  const acaps = [...(a?.capabilities || [])].sort().join(',')
  const bcaps = [...(b.capabilities || [])].sort().join(',')
  return ac === bc && ao === bo && acaps === bcaps
}

// 外部（编辑已有 / 复制 / 加载配置）改写 model 时同步进本地输入框。
watch(
  () => model.value,
  (value) => {
    if (sameConfig(value, normalize(contextLength.value, maxOutputTokens.value, capabilities)))
      return
    contextLength.value = value?.context_length ? String(value.context_length) : ''
    maxOutputTokens.value = value?.max_output_tokens ? String(value.max_output_tokens) : ''
    capabilities.splice(0, capabilities.length, ...(value?.capabilities || []))
  },
  { immediate: true, deep: true },
)

watch([contextLength, maxOutputTokens, () => [...capabilities]], () => {
  const next = normalize(contextLength.value, maxOutputTokens.value, capabilities)
  if (sameConfig(model.value, next)) return
  model.value = next
})

function toggleCapability(value: AggregateCapability) {
  const index = capabilities.indexOf(value)
  if (index >= 0) capabilities.splice(index, 1)
  else capabilities.push(value)
}

// ===== 加载配置：从 OpenRouter 元数据缓存里挑一个模型，回填上面的输入框 =====

const open = ref(false)
const search = ref('')
const loading = ref(false)
const loadError = ref('')
const catalog = ref<CatalogModel[]>([])

// 首次展开时才拉取模型清单（懒加载）。Popover 触发器自带开关逻辑，
// 不要再手动 toggle——两次取反互相抵消，弹层开了又立刻关掉，看起来就是"没反应"。
watch(open, (value) => {
  if (value) void ensureCatalog()
})

const filtered = computed(() => {
  const keyword = search.value.trim().toLowerCase()
  if (!keyword) return catalog.value
  return catalog.value.filter(
    (m) => m.id.toLowerCase().includes(keyword) || m.name.toLowerCase().includes(keyword),
  )
})

async function ensureCatalog() {
  if (catalog.value.length || loading.value) return
  loading.value = true
  loadError.value = ''
  try {
    const res = await aggregateService.catalogModels()
    catalog.value = res.models || []
    if (!catalog.value.length) loadError.value = '没有可用的模型，请先到「UnifyAI」页更新元数据'
  } catch (err) {
    loadError.value = err instanceof Error ? err.message : '加载模型列表失败'
  } finally {
    loading.value = false
  }
}

// 选中一个模型：把它的四项属性填进输入框（能力用多选框体现）。
function applyModel(item: CatalogModel) {
  contextLength.value = item.context ? String(item.context) : ''
  maxOutputTokens.value = item.output ? String(item.output) : ''
  capabilities.splice(0, capabilities.length)
  if (item.vision) capabilities.push('vision')
  if (item.reasoning) capabilities.push('reasoning')
  open.value = false
  search.value = ''
}
</script>

<template>
  <div class="space-y-2">
    <div class="flex items-center justify-between gap-2">
      <Label>模型配置</Label>
      <Popover v-model:open="open">
        <PopoverTrigger as-child>
          <Button type="button" variant="outline" size="sm">
            <RiSparklingLine size="14" />加载配置
            <RiArrowDownSLine class="size-4 opacity-50" />
          </Button>
        </PopoverTrigger>
        <PopoverContent class="w-96 p-0" align="end">
          <Command>
            <CommandInput v-model="search" placeholder="搜索模型（来自 OpenRouter）…" />
            <CommandList>
              <div v-if="loading" class="px-3 py-6 text-center text-xs text-muted-foreground">
                正在加载…
              </div>
              <div
                v-else-if="loadError"
                class="px-3 py-6 text-center text-xs text-muted-foreground"
              >
                {{ loadError }}
              </div>
              <CommandEmpty v-else>未找到「{{ search }}」</CommandEmpty>
              <CommandGroup>
                <CommandItem
                  v-for="m in filtered"
                  :key="m.id"
                  :value="m.id"
                  @select="applyModel(m)"
                >
                  <div class="min-w-0 flex-1">
                    <div class="truncate font-mono text-xs">{{ m.id }}</div>
                    <div class="truncate text-[11px] text-muted-foreground">{{ m.name }}</div>
                  </div>
                  <span class="shrink-0 text-[11px] tabular-nums text-muted-foreground">{{
                    m.context ? m.context.toLocaleString() : '—'
                  }}</span>
                </CommandItem>
              </CommandGroup>
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
    <p class="text-xs text-muted-foreground">
      配置这个虚拟模型对外声明的属性（/v1/models 返回）。留空表示不声明，客户端按自己的默认值处理。
    </p>
    <div class="grid gap-3 sm:grid-cols-2">
      <div class="space-y-2">
        <Label for="aggregate-context">上下文长度</Label>
        <Input
          id="aggregate-context"
          v-model="contextLength"
          inputmode="numeric"
          placeholder="如 1048576"
        />
      </div>
      <div class="space-y-2">
        <Label for="aggregate-max-output">最大输出</Label>
        <Input
          id="aggregate-max-output"
          v-model="maxOutputTokens"
          inputmode="numeric"
          placeholder="如 384000"
        />
      </div>
    </div>
    <div class="space-y-2">
      <Label>能力</Label>
      <div class="flex flex-wrap gap-4">
        <Tooltip v-for="cap in CAPABILITIES" :key="cap.value">
          <TooltipTrigger as-child>
            <label class="flex cursor-pointer items-center gap-2 text-sm">
              <Checkbox
                :model-value="capabilities.includes(cap.value)"
                @update:model-value="toggleCapability(cap.value)"
              />
              {{ cap.label }}
            </label>
          </TooltipTrigger>
          <TooltipContent>{{ cap.hint }}</TooltipContent>
        </Tooltip>
      </div>
    </div>
  </div>
</template>
