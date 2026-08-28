<script setup lang="ts">
// 单 Key 模型聚合卡片（关注/其他分组，可拖拽排序）。
// 接收 channelId（API Key 的 channel_id），组件内部从 /api/volc-quota/aggregate
// 取该 Key 所属账号的聚合数据，并按该渠道勾选的模型做关注/其他分组渲染。
import { computed, onMounted, ref, watch } from 'vue'
import { RiArrowRightSLine } from '@remixicon/vue'
import { VueDraggable } from 'vue-draggable-plus'
import type { Channel, VolcQuotaAggregate } from '@/lib/types'
import { useChannels } from '@/composables/useChannels'
import { useVolcQuota } from '@/composables/useVolcQuota'
import { matchQuotaModel, mergeAggregates } from '@/composables/useVolcQuotaFocus'
import EmptyState from '@/components/EmptyState.vue'

const props = defineProps<{ channelId: string }>()

const quota = useVolcQuota()
const channelsApi = useChannels()

// ===== 聚合数据：后台按 model 聚合接口（/api/volc-quota/aggregate，v19） =====
const aggregates = ref<VolcQuotaAggregate[]>([])
const aggregatesLoaded = ref(false)
async function loadAggregates() {
  try {
    const data = await quota.aggregate()
    // 后端 /aggregate 返回每个渠道一行、携带该账号聚合；按当前 channelId 取对应行。
    // 同账号多渠道共享同一份快照。
    // 只取当前 channelId 对应的聚合；找不到或为空时直接返回空，
    // 不回退到别的账号（避免错显别的 Key 的额度）。
    const mine = data.configs.find(
      (c) => c.config.channel_id === props.channelId,
    )?.aggregates
    aggregates.value = mine || []
  } catch {
    aggregates.value = []
  } finally {
    aggregatesLoaded.value = true
  }
}

// ===== 关注模型分组（v20）：该渠道勾选过的模型 →「关注」区，其余 →「其他」区 =====
// 关注集合：当前 channelId 对应的渠道勾选模型（去重）。渠道没勾选任何模型时退化为平铺视图。
const arkChannels = ref<Channel[]>([])
async function loadArkChannels() {
  try {
    const all = await channelsApi.list()
    arkChannels.value = all.filter(
      (c) => c.base_url && c.base_url.includes('ark.cn-beijing.volces.com'),
    )
  } catch {
    arkChannels.value = []
  }
}
const focusModels = computed(() => {
  const set = new Set<string>()
  for (const ch of arkChannels.value) {
    if (ch.id !== props.channelId) continue
    for (const m of ch.models || []) set.add(m)
  }
  return [...set]
})

// 关注区拖拽顺序：localStorage 持久化（仅 UI 观察用途，不回写渠道配置）。
// 每个 Key 各存各的拖拽顺序（key 带 channelId 隔离），避免多 Key 互相穿扰。
const focusOrderKey = `volc-quota-focus-order-${props.channelId}`
const focusOrder = ref<string[]>(loadFocusOrder())
function loadFocusOrder(): string[] {
  try {
    const raw = localStorage.getItem(focusOrderKey)
    if (!raw) return []
    const arr = JSON.parse(raw)
    return Array.isArray(arr) ? arr.filter((x) => typeof x === 'string') : []
  } catch {
    return []
  }
}
function saveFocusOrder() {
  localStorage.setItem(focusOrderKey, JSON.stringify(focusOrder.value))
}

/** 关注区卡片项：来自用户勾选的全集。无聚合数据时填充占位字段 + __noAggregate 标记。 */
type FocusCardItem = VolcQuotaAggregate & { __noAggregate?: boolean }

function makePlaceholder(model: string): FocusCardItem {
  return {
    model,
    name: model,
    unit: 'token',
    initial_total: 0,
    local_remaining: 0,
    used_amount: 0,
    total_amount: 0,
    percentage: 0,
    exhausted: false,
    __noAggregate: true,
  }
}

/** 关注区卡片：来自渠道勾选的全集（用户视角的「已选」），按 localStorage 顺序排。
 * 同一聚合短名可能被多个渠道模型命中（如 doubao-seed-2-0-lite-260215 与
 * doubao-seed-2-0-lite-260428 都映射到 doubao-seed-2-0-lite），按命中到的 quota
 * 短名去重，只保留一张卡片；未命中任何聚合的占位卡（__noAggregate）各自保留。 */
const focusAggregates = computed<FocusCardItem[]>(() => {
  const orderMap = new Map(focusOrder.value.map((m, i) => [m, i]))
  const items: FocusCardItem[] = []
  const seenQuota = new Set<string>()
  for (const fm of focusModels.value) {
    const matched = aggregates.value.filter((a) => matchQuotaModel(a.model, fm))
    const cardKey = matched[0]?.model ?? fm
    if (seenQuota.has(cardKey)) continue
    seenQuota.add(cardKey)
    items.push(mergeAggregates(matched) ?? makePlaceholder(fm))
  }
  return items.sort((a, b) => {
    const ia = orderMap.has(a.model) ? (orderMap.get(a.model) as number) : Number.MAX_SAFE_INTEGER
    const ib = orderMap.has(b.model) ? (orderMap.get(b.model) as number) : Number.MAX_SAFE_INTEGER
    return ia - ib
  })
})
const otherAggregates = computed(() =>
  aggregates.value.filter((a) => !focusModels.value.some((fm) => matchQuotaModel(a.model, fm))),
)

// VueDraggable 拖拽目标列表：focusAggregates 是 computed 只读，拖拽需要可写数组。
const focusCards = ref<FocusCardItem[]>([])
watch(
  focusAggregates,
  (v) => {
    focusCards.value = v
  },
  { immediate: true },
)

// 拖拽重排（vue-draggable-plus）：拖拽结束回传新顺序，写回 focusOrder 持久化。
function onFocusReorder(list: FocusCardItem[]) {
  focusOrder.value = list.map((a) => a.model)
  saveFocusOrder()
}

// 卡片分组渲染：关注区（可拖拽）+ 其他区（不可拖拽）；关注为空时只显示「模型」一组。
const cardGroups = computed(() => {
  const focus = focusAggregates.value
  const other = otherAggregates.value
  const groups: {
    key: string
    label: string
    hint?: string
    items: FocusCardItem[]
    draggable: boolean
  }[] = []
  if (focus.length) {
    groups.push({
      key: 'focus',
      label: `关注模型 · ${focus.length}`,
      hint: '拖拽卡片可调整顺序',
      items: focus,
      draggable: true,
    })
  }
  if (other.length) {
    groups.push({
      key: 'other',
      label: focus.length ? `其他模型 · ${other.length}` : `模型 · ${other.length}`,
      items: other as FocusCardItem[],
      draggable: false,
    })
  }
  return groups
})

// 分组折叠：关注默认展开、其他默认折叠
const expandedGroups = ref<string[]>([])
watch(
  cardGroups,
  (groups) => {
    for (const g of groups) {
      if (g.key !== 'focus') continue
      if (!expandedGroups.value.includes(g.key)) expandedGroups.value.push(g.key)
    }
  },
  { immediate: true },
)

onMounted(() => {
  loadAggregates()
  loadArkChannels()
})

// 供父组件在刷新后调用：重载聚合数据与渠道勾选模型。
// 切换 tab 时组件常驻（v-show 控制显隐），不会重复触发加载，
// 只有显式刷新才重新请求 /aggregate。
async function refresh() {
  await Promise.all([loadAggregates(), loadArkChannels()])
}
defineExpose({ refresh })
</script>

<template>
  <Accordion v-if="aggregates.length" v-model="expandedGroups" type="multiple" class="w-full">
    <AccordionItem v-for="group in cardGroups" :key="group.key" :value="group.key">
      <AccordionTrigger>
        <span class="inline-flex items-center">
          <RiArrowRightSLine
            class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90" />
          <span class="text-xs font-medium text-muted-foreground">{{ group.label }}</span>
          <span v-if="group.hint" class="ml-2 text-[10px] font-normal text-muted-foreground/70">{{
            group.hint
            }}</span>
        </span>
        <template #icon><span class="hidden" /></template>
      </AccordionTrigger>
      <AccordionContent>
        <VueDraggable :model-value="group.draggable ? focusCards : group.items" :disabled="!group.draggable"
          :animation="150" ghost-class="opacity-0" drag-class="volc-dragging"
          class="grid grid-cols-1 gap-2 md:grid-cols-2! lg:grid-cols-3! xl:grid-cols-4!" @update:model-value="onFocusReorder">
          <div v-for="a in group.draggable ? focusCards : group.items" :key="a.model"
            class="flex flex-col gap-1.5 rounded-md border p-3" :class="[
              a.__noAggregate ? 'border-dashed border-muted-foreground/40 bg-muted/30 text-muted-foreground' : '',
              !a.__noAggregate && a.exhausted ? 'border-destructive/40 bg-destructive/5' : '',
              group.draggable ? 'cursor-grab active:cursor-grabbing' : '',
            ]">
            <!-- 顶部：左侧模型名，右侧 token 积分（当前剩余/总）；占位无数据 → 显示「暂无额度数据」标记 -->
            <div class="flex items-start justify-between gap-2">
              <div class="flex items-center min-w-0 gap-2">
                <div class="truncate text-sm font-mono font-bold select-all" :title="a.name || a.model">
                  {{ a.name || a.model }}
                </div>
                <Badge v-if="a.__noAggregate" variant="outline" class="shrink-0 text-[10px] font-normal">
                  暂无额度数据
                </Badge>
              </div>
              <div v-if="!a.__noAggregate" class="shrink-0 text-right flex gap-2 items-center">
                <div class="text-sm font-semibold tabular-nums" :class="a.exhausted ? 'text-destructive' : ''">
                  {{ a.local_remaining.toLocaleString() }}
                </div>
                <div class="text-[10px] text-muted-foreground tabular-nums">
                  / {{ a.initial_total.toLocaleString() }}
                </div>
              </div>
              <div v-else class="text-xs text-muted-foreground">—</div>
            </div>
            <!-- 进度条 + 右侧百分比 -->
            <div v-if="!a.__noAggregate" class="flex items-center gap-2">
              <Progress :model-value="a.percentage" class="h-1.5 flex-1" :class="a.exhausted
                  ? 'bg-destructive/20'
                  : a.percentage < 20
                    ? 'bg-amber-500/20'
                    : ''
                " />
              <span class="w-9 shrink-0 text-right text-xs tabular-nums text-muted-foreground"
                :class="a.exhausted ? 'text-destructive' : ''">
                {{ a.percentage }}%
              </span>
            </div>
            <div v-else class="text-[10px] text-muted-foreground/70">
              该模型不在火山引擎免费额度范围 / 未拉到账单明细。
            </div>
          </div>
        </VueDraggable>
      </AccordionContent>
    </AccordionItem>
  </Accordion>
  <EmptyState v-else :title="aggregatesLoaded ? '暂无额度数据' : '加载中…'"
    description="点击右侧「刷新远程」获取最新额度（账单有延迟，后台每 15 分钟自动刷新）。" />
</template>
