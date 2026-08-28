<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import { RiLoader4Line, RiRefreshLine, RiStopCircleLine, RiTranslate2 } from '@remixicon/vue'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import TranslateText from '@/components/TranslateText.vue'
import TargetModelPicker from '@/components/TargetModelPicker.vue'
import { useChannels } from '@/composables/useChannels'
import { useAggregates } from '@/composables/useAggregates'
import { getTranslateSources } from '@/lib/api'
import type { TranslateSourceItem } from '@/lib/types'
import { RiArrowRightSLine } from '@remixicon/vue'
import { useTranslateStore, type TranslateDisplayMode } from '@/stores/translate'

const store = useTranslateStore()
const channels = useChannels()
const aggregates = useAggregates()

// ---- 模型列表（渠道模型 + 聚合虚拟模型合并）----
const modelOptions = ref<string[]>([])
const modelsLoading = ref(false)
async function loadModels() {
  modelsLoading.value = true
  try {
    const [chs, aggs] = await Promise.all([channels.list(), aggregates.list()])
    const set = new Set<string>()
    for (const ch of chs) {
      for (const m of ch.models || []) set.add(m)
    }
    for (const a of aggs) set.add(a.name)
    modelOptions.value = [...set].sort()
    // 默认选中：优先 hy3（用户指定），否则第一个
    if (!store.model && modelOptions.value.length) {
      const prefer = modelOptions.value.find((m) => m.toLowerCase().includes('hy3'))
      store.model = prefer || modelOptions.value[0]
    }
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载模型列表失败')
  } finally {
    modelsLoading.value = false
  }
}

// ---- 来源清单 ----
const sources = ref<TranslateSourceItem[]>([])
const sourcesLoading = ref(false)
// 用 ref<string[]> 而非 Set：数组的 push/splice 会触发 Vue 响应式更新
const selected = ref<string[]>([])
const expandedParams = ref<string[]>([]) // 展开参数的工具 key
async function loadSources() {
  sourcesLoading.value = true
  try {
    const res = await getTranslateSources()
    sources.value = res.items || []
    // 一次批量 lookup 灌入数据库已有译文，避免每个 TranslateText 单独请求
    const texts: { text: string; textKey: string }[] = []
    for (const s of sources.value) {
      texts.push({ text: s.description, textKey: keyOf(s) })
      for (const p of s.params || []) {
        if (p.description)
          texts.push({ text: p.description, textKey: `${keyOf(s)}/param/${p.name}/description` })
        if (p.title) texts.push({ text: p.title, textKey: `${keyOf(s)}/param/${p.name}/title` })
      }
    }
    if (texts.length) void store.lookupBatch(texts)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '加载来源清单失败')
  } finally {
    sourcesLoading.value = false
  }
}

const keyOf = (s: { source_type: string; source_id: string }) => s.source_type + ':' + s.source_id
const isSelected = (key: string) => selected.value.includes(key)
function toggleItem(key: string) {
  const i = selected.value.indexOf(key)
  if (i >= 0) selected.value.splice(i, 1)
  else selected.value.push(key)
}

// 分组：按类型（mcp / skill）
const typeGroups = ['mcp', 'skill'] as const

const groupedSources = computed(() =>
  typeGroups
    .map((t) => ({ type: t, items: sources.value.filter((s) => s.source_type === t) }))
    .filter((g) => g.items.length),
)
function typeKeys(type: string) {
  return sources.value.filter((s) => s.source_type === type).map(keyOf)
}
function typeAllSelected(type: string) {
  const keys = typeKeys(type)
  return keys.length > 0 && keys.every((k) => selected.value.includes(k))
}
function toggleTypeAll(type: string) {
  const keys = typeKeys(type)
  const allOn = keys.every((k) => selected.value.includes(k))
  if (allOn) keys.forEach((k) => removeKey(k))
  else keys.forEach((k) => addKey(k))
}

function invertType(type: string) {
  const keys = typeKeys(type)
  const currently = new Set(selected.value)
  keys.forEach((k) => {
    if (currently.has(k)) removeKey(k)
    else addKey(k)
  })
}
function clearType(type: string) {
  const keys = typeKeys(type)
  keys.forEach((k) => removeKey(k))
}

// addKey/removeKey 是数组版 toggle 的辅助
function addKey(k: string) {
  if (!selected.value.includes(k)) selected.value.push(k)
}
function removeKey(k: string) {
  const i = selected.value.indexOf(k)
  if (i >= 0) selected.value.splice(i, 1)
}

// ---- 已翻译状态：后端缓存命中或 store 里已有译文 ----
function isTranslated(s: TranslateSourceItem): boolean {
  return !!s.translated || !!store.getTranslated(keyOf(s))
}

// ---- 单个条目翻译：描述 ----
const translatingKeys = ref<string[]>([])
async function translateItem(s: TranslateSourceItem) {
  const key = keyOf(s)
  if (translatingKeys.value.includes(key)) return
  translatingKeys.value.push(key)
  try {
    await store.translateOne(s.description, {
      textKey: key,
      source_type: s.source_type,
      source_id: s.source_id,
    })
    s.translated = true
    toast.success(`「${s.name}」翻译完成`)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : `「${s.name}」翻译失败`)
  } finally {
    const i = translatingKeys.value.indexOf(key)
    if (i >= 0) translatingKeys.value.splice(i, 1)
  }
}

// ---- 单个 MCP 工具：翻译全部参数 ----
const paramsKeys = ref<string[]>([])
async function translateParams(s: TranslateSourceItem) {
  if (!s.params || !s.params.length) return
  const key = keyOf(s)
  if (paramsKeys.value.includes(key)) return
  paramsKeys.value.push(key)
  try {
    for (const p of s.params) {
      if (p.description) {
        await store.translateOne(p.description, {
          textKey: `${key}/param/${p.name}/description`,
          source_type: s.source_type,
          source_id: s.source_id,
        })
      }
      if (p.title) {
        await store.translateOne(p.title, {
          textKey: `${key}/param/${p.name}/title`,
          source_type: s.source_type,
          source_id: s.source_id,
        })
      }
    }
    toast.success(`「${s.name}」参数翻译完成`)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : `「${s.name}」参数翻译失败`)
  } finally {
    const i = paramsKeys.value.indexOf(key)
    if (i >= 0) paramsKeys.value.splice(i, 1)
  }
}

// 参数展开/收起
function toggleParams(key: string) {
  const i = expandedParams.value.indexOf(key)
  if (i >= 0) expandedParams.value.splice(i, 1)
  else expandedParams.value.push(key)
}
const isParamsExpanded = (key: string) => expandedParams.value.includes(key)

// ---- 批量翻译 ----
// 按钮"翻译中"状态直接跟随 store.batchRunning：启动/恢复为 true，停止/完成为 false。
const translating = computed(() => store.batchRunning)
// 翻译中按钮 hover 时切换为"停止翻译"：true 表示鼠标悬停，可点击终止
const hoverCancel = ref(false)
async function stopBatch() {
  await store.cancelBatch()
  hoverCancel.value = false
  toast.info('已停止翻译')
}
async function runBatch() {
  const items: { source_type: string; source_id: string; description: string; textKey?: string }[] =
    []
  for (const s of sources.value) {
    if (!isSelected(keyOf(s))) continue
    const key = keyOf(s)
    // 描述本身
    if (s.description)
      items.push({
        source_type: s.source_type,
        source_id: s.source_id,
        description: s.description,
        textKey: key,
      })
    // 该工具的每个参数（描述 + 标题）一并放入同一批量请求，避免逐条单独调用
    for (const p of s.params || []) {
      if (p.description)
        items.push({
          source_type: s.source_type,
          source_id: s.source_id,
          description: p.description,
          textKey: `${key}/param/${p.name}/description`,
        })
      if (p.title)
        items.push({
          source_type: s.source_type,
          source_id: s.source_id,
          description: p.title,
          textKey: `${key}/param/${p.name}/title`,
        })
    }
  }
  if (!items.length) {
    toast.error('请先勾选要翻译的条目')
    return
  }
  hoverCancel.value = false
  try {
    await store.translateBatch(items, {
      model: store.model,
      prompt: store.prompt,
      target_lang: store.targetLang,
    })
    // 批量完成后把后端缓存命中情况写回，徽标即时更新
    for (const s of sources.value) {
      if (store.getTranslated(keyOf(s))) s.translated = true
    }
    if (store.batchFinished) {
      if (store.batchError) {
        toast.error(`部分条目翻译失败：${store.batchError}`)
      } else {
        toast.success('批量翻译完成')
      }
    }
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '批量翻译失败')
  }
}

const displayModes: { value: TranslateDisplayMode; label: string }[] = [
  { value: 'both', label: '原文+译文' },
  { value: 'translated', label: '仅译文' },
  { value: 'original', label: '仅原文' },
]

onMounted(() => {
  void loadModels()
  void loadSources()
  // 刷新后若存在未完成的后台批量翻译任务，恢复进度条继续显示（由 store.batchRunning 驱动）
  void store.resumeBatch()
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="翻译" description="配置目标语言/模型/提示词，批量翻译 MCP 工具与技能描述。">
      <template #actions>
        <Button variant="outline" :disabled="sourcesLoading" @click="loadSources">
          <RiLoader4Line v-if="sourcesLoading" class="size-4 animate-spin" />
          <RiRefreshLine v-else class="size-4" />刷新来源
        </Button>
      </template>
    </PageHeader>

    <!-- ===== 顶部配置条（横排一行） ===== -->
    <Card class="rounded-md pb-2">
      <CardContent>
        <div class="flex items-bottom gap-3">
          <div class="space-y-1">
            <Label class="text-xs">目标语言</Label>
            <Input v-model="store.targetLang" placeholder="zh-CN / zh / ja" />
          </div>
          <div class="space-y-1">
            <Label class="text-xs">目标模型</Label>
            <TargetModelPicker
              v-model="store.model"
              :models="modelOptions"
              :multiple="false"
              :allow-custom="false"
              :loading="modelsLoading"
            />
          </div>
          <div class="space-y-1">
            <Label class="text-xs">显示模式</Label>
            <Select
              :model-value="store.displayMode"
              @update:model-value="store.displayMode = $event"
            >
              <SelectTrigger id="translate-display" class="w-full">
                <SelectValue placeholder="选择显示模式" />
              </SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem v-for="dm in displayModes" :key="dm.value" :value="dm.value">
                    {{ dm.label }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1">
            <Label class="text-xs">并发数量</Label>
            <Input
              v-model.number="store.batchConcurrency"
              type="number"
              min="1"
              placeholder="默认 5"
              class="w-[96px]"
            />
          </div>
          <div class="flex flex-wrap items-end gap-2">
            <div class="group relative" @mouseleave="hoverCancel = false">
              <!-- 翻译中：hover 时切换为"停止翻译"按钮，点击终止任务 -->
              <Button
                v-if="translating"
                variant="destructive"
                @click="hoverCancel ? stopBatch() : (hoverCancel = true)"
                @mouseenter="hoverCancel = true"
              >
                <RiStopCircleLine v-if="hoverCancel" class="size-4" />
                <RiLoader4Line v-else class="size-4 animate-spin" />
                {{ hoverCancel ? '停止翻译' : `批量翻译 ${store.batchDone}/${store.batchTotal}` }}
              </Button>
              <!-- 未翻译：原"批量翻译 N"按钮 -->
              <Button v-else :disabled="!selected.length" @click="runBatch">
                <RiTranslate2 class="size-4" />
                批量翻译{{ selected.length ? ` ${selected.length}` : '' }}
              </Button>
            </div>
          </div>
        </div>
        <!-- 提示词：可折叠（Accordion）-->
        <Accordion type="single" collapsible>
          <AccordionItem value="prompt">
            <AccordionTrigger class="text-xs text-muted-foreground">
              <span class="inline-flex items-center">
                <RiArrowRightSLine
                  class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90"
                />
                翻译提示词（可自定义）
              </span>
              <template #icon><span class="hidden" /></template>
            </AccordionTrigger>
            <AccordionContent class="px-1">
              <Textarea
                v-model="store.prompt"
                :rows="3"
                class="mt-2 min-h-[72px]"
                placeholder="留空使用默认提示词。可用 {lang} 表示目标语言。"
              />
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </CardContent>
    </Card>

    <!-- ===== MCP / Skill 分组 ===== -->
    <div v-if="sourcesLoading" class="rounded-md border p-6">
      <LoadingBlock />
    </div>
    <EmptyState
      v-else-if="!sources.length"
      title="暂无可翻译来源"
      description="需要启用的 MCP 服务器或已安装技能，刷新后显示。"
    />
    <div v-else class="space-y-4">
      <Accordion type="multiple" :default-value="['mcp', 'skill']" class="space-y-4">
        <AccordionItem
          v-for="group in groupedSources"
          :key="group.type"
          :value="group.type"
          class="overflow-hidden rounded-md border"
        >
          <AccordionTrigger class="border-b bg-muted/40 px-4 py-3 hover:no-underline">
            <RiArrowRightSLine
              class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90"
            />
            <Badge :variant="group.type === 'mcp' ? 'default' : 'secondary'">
              {{ group.type === 'mcp' ? 'MCP' : 'Skill' }}
            </Badge>
            <span class="text-sm text-muted-foreground">{{ group.items.length }} 项</span>
            <div class="ml-auto flex items-center gap-1" @click.stop>
              <Button variant="outline" size="sm" @click.stop="toggleTypeAll(group.type)">
                {{ typeAllSelected(group.type) ? '取消全选' : '全选' }}
              </Button>
              <Button variant="outline" size="sm" @click.stop="invertType(group.type)">
                反选
              </Button>
              <Button variant="outline" size="sm" @click.stop="clearType(group.type)">
                清空
              </Button>
            </div>
            <template #icon><span class="hidden" /></template>
          </AccordionTrigger>
          <AccordionContent>
            <div
              v-for="s in group.items"
              :key="keyOf(s)"
              class="border-b px-4 py-3 last:border-b-0"
              :class="{ 'bg-primary/5': isSelected(keyOf(s)) }"
            >
              <!-- 条目头 -->
              <div class="flex items-center gap-2">
                <label class="flex cursor-pointer items-center" @click.stop>
                  <Checkbox
                    :model-value="isSelected(keyOf(s))"
                    @update:model-value="toggleItem(keyOf(s))"
                  />
                </label>
                <span class="truncate text-sm font-medium">{{ s.name }}</span>
                <Badge
                  v-if="isTranslated(s)"
                  variant="success"
                  class="ml-2 bg-emerald-100 text-emerald-700 hover:bg-emerald-100"
                >
                  已翻译
                </Badge>
                <Badge v-else variant="secondary" class="ml-2">未翻译</Badge>
                <div class="ml-auto flex items-center gap-2">
                  <Button
                    v-if="s.params && s.params.length"
                    variant="ghost"
                    size="sm"
                    class="text-xs"
                    @click.stop="toggleParams(keyOf(s))"
                  >
                    {{ isParamsExpanded(keyOf(s)) ? '收起参数' : '参数' }}
                    <span class="text-muted-foreground">({{ s.params.length }})</span>
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="text-xs text-blue-600 hover:underline hover:bg-transparent"
                    :disabled="translatingKeys.includes(keyOf(s))"
                    @click.stop="translateItem(s)"
                  >
                    <RiLoader4Line
                      v-if="translatingKeys.includes(keyOf(s))"
                      class="animate-spin"
                      size="14"
                    />
                    {{ isTranslated(s) ? '重新翻译' : '翻译' }}
                  </Button>
                </div>
              </div>

              <!-- 描述：原文 + 译文 -->
              <div class="ml-7 mt-2 text-sm">
                <TranslateText
                  :source="s.description"
                  :text-key="keyOf(s)"
                  :source-type="s.source_type"
                  :source-id="s.source_id"
                />
              </div>

              <!-- MCP 参数表格 -->
              <div
                v-if="isParamsExpanded(keyOf(s)) && s.params && s.params.length"
                class="ml-7 mt-3 overflow-hidden rounded-md border"
              >
                <div class="flex items-center gap-2 bg-muted/40 px-3 py-1.5">
                  <span class="text-xs font-medium text-muted-foreground">参数</span>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="ml-auto text-xs text-blue-600 hover:underline hover:bg-transparent"
                    :disabled="paramsKeys.includes(keyOf(s))"
                    @click.stop="translateParams(s)"
                  >
                    <RiLoader4Line
                      v-if="paramsKeys.includes(keyOf(s))"
                      class="animate-spin"
                      size="14"
                    />
                    全部翻译参数
                  </Button>
                </div>
                <table class="w-full text-sm">
                  <tbody>
                    <tr v-for="p in s.params" :key="p.name" class="border-t first:border-t-0">
                      <td class="w-44 px-3 py-2 align-top whitespace-nowrap">
                        <span class="relative inline-flex items-center">
                          <code class="rounded bg-muted px-1.5 py-0.5 text-xs font-semibold">
                            {{ p.name }}
                          </code>
                          <span
                            v-if="p.required"
                            class="absolute -right-1.5 -top-1.5 text-sm font-bold leading-none text-red-500"
                            title="必填"
                            >*</span
                          >
                        </span>
                        <Badge v-if="p.type" variant="outline" class="ml-1 text-[10px]">
                          {{ p.type }}
                        </Badge>
                      </td>
                      <td class="px-3 py-2">
                        <div v-if="p.title && p.title !== p.name" class="text-xs">
                          <TranslateText
                            :source="p.title"
                            :text-key="`${keyOf(s)}/param/${p.name}/title`"
                            :source-type="s.source_type"
                            :source-id="s.source_id"
                          />
                        </div>
                        <div v-if="p.description" class="text-xs">
                          <TranslateText
                            :source="p.description"
                            :text-key="`${keyOf(s)}/param/${p.name}/description`"
                            :source-type="s.source_type"
                            :source-id="s.source_id"
                          />
                        </div>
                      </td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
          </AccordionContent>
        </AccordionItem>
      </Accordion>
    </div>
  </div>
</template>
