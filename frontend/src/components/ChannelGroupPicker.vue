<script setup lang="ts">
// 渠道分组选择器（从 ModelChannelList 抽取的公共组件）。
// 一个渠道组 = 一个 base_url；组内每行 = 一个 Key（账号）。
// 支持两种布局：
//   vertical   = 组标题在上、keys 换行在下（ModelChannelList 原样式）
//   horizontal = 每个渠道一行：左渠道名、右 keys 横向排开
// 选择态统一归一为 { channel_id, channel_ids, channel_base_url }，与 ModelChannelItem 字段兼容。
import { computed } from 'vue'
import { RiCheckLine, RiKey2Line, RiStackLine } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from '@/composables/useChannels'

export interface ChannelSelection {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
}

const props = withDefaults(
  defineProps<{
    channels: Channel[]
    layout?: 'vertical' | 'horizontal'
    // 是否显示「自动路由」（选择态为空时由外部处理；仅 ModelChannelList 用）。
    allowAuto?: boolean
    multiSelect?: boolean
    // 单渠道约束：true（默认，ModelChannelList 场景）= 选中 Key 自动切组（跨组清空旧组）；
    //   false（能力路由场景）= 允许跨 base_url 组自由累加 Key。
    singleChannelGroup?: boolean
    autoLabel?: string
    emptyLabel?: string
  }>(),
  {
    layout: 'vertical',
    allowAuto: true,
    multiSelect: true,
    singleChannelGroup: true,
    autoLabel: '自动路由（按模型找渠道）',
    emptyLabel: '暂无可用渠道',
  },
)

const model = defineModel<ChannelSelection>({ required: true })

// 渠道按 base_url 分组（渠道组 = 一个渠道；组内 = 各 Key）。
const channelGroups = computed(() => groupChannelsByBaseURL(props.channels))

// 组标题：渠道名。
function groupLabel(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const first = group.keys[0]
  return first?.channel_name || first?.name || group.baseUrl
}

// 当前选择态：{ auto, ids }。ids = 已勾选 Key 的 id 集合。
const selection = computed(() => {
  const m = model.value
  if (!m) return { auto: true, ids: new Set<string>() }
  if (m.channel_base_url) {
    const group = channelGroups.value.find(
      (g) => normalizeBaseURL(g.baseUrl) === normalizeBaseURL(m.channel_base_url!),
    )
    return { auto: false, ids: new Set((group?.keys || []).map((k) => k.id)) }
  }
  const ids = m.channel_ids?.length ? m.channel_ids : m.channel_id ? [m.channel_id] : []
  return { auto: ids.length === 0, ids: new Set(ids) }
})

// 是否渠道级选中指定组（只有 channel_base_url === group.baseUrl 才算渠道级）。
function isChannelLevelSelected(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const m = model.value
  if (!m?.channel_base_url) return false
  return normalizeBaseURL(m.channel_base_url) === normalizeBaseURL(group.baseUrl)
}

// 把勾选态写回 model（后端字段归一化）。
// 重要：渠道级（channel_base_url）与 Key 多选（channel_ids）是两个独立概念，互不转换。
//   - 渠道级：只能由点击渠道组标题触发。
//   - Key 多选：即使全部 Key 恰好被勾满，也仍然是 channel_ids，不会自动折叠为渠道级。
//   - 空：自动路由。
function applySelection(ids: Set<string>, auto: boolean) {
  const m = model.value
  if (!m) return
  if (auto || ids.size === 0) {
    m.channel_id = ''
    m.channel_ids = []
    m.channel_base_url = ''
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
  m.channel_id = ''
  m.channel_ids = ordered
  m.channel_base_url = ''
}

// 设为渠道级（点击组标题触发）。再次点击同组 = 取消渠道级。
function setChannelLevel(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const m = model.value
  if (!m) return
  m.channel_id = ''
  m.channel_ids = []
  m.channel_base_url = group.baseUrl
}

// 当前勾选所属的渠道组（单渠道约束：一个选择只允许一个渠道组，组内可多选 Key）。
function activeGroupOf(): ReturnType<typeof groupChannelsByBaseURL>[number] | undefined {
  const { ids } = selection.value
  for (const g of channelGroups.value) {
    if (g.keys.some((k) => ids.has(k.id))) return g
  }
  return undefined
}

function toggleKey(id: string) {
  const sel = selection.value
  const keyGroup = channelGroups.value.find((g) => g.keys.some((k) => k.id === id))
  const active = activeGroupOf()
  // 单渠道模式：跨组勾选自动切组（清空旧组，只保留新勾选的 Key）。
  if (props.singleChannelGroup && keyGroup && active && keyGroup.baseUrl !== active.baseUrl) {
    applySelection(new Set([id]), false)
    return
  }
  // 当前是渠道级 → 切到 Key 多选模式：
  //   - 单渠道模式：保留组内所有 Key 的勾选视觉，仅切换当前 Key。
  //   - 多渠道模式：保留组内所有 Key（用作起点），合并点击的 Key（可跨组累加）。
  if (model.value?.channel_base_url) {
    const own = (active?.keys || []).map((k) => k.id)
    const next = new Set(own)
    if (props.singleChannelGroup) {
      if (next.has(id)) next.delete(id)
      else next.add(id)
    } else {
      // 多渠道模式：从渠道级切换到多 Key 时，已有 Key 一律带过去（之后可继续跨组扩展）。
      next.add(id)
    }
    applySelection(next, false)
    return
  }
  if (sel.ids.has(id)) sel.ids.delete(id)
  else sel.ids.add(id)
  applySelection(sel.ids, false)
}
// 组标题是否呈选中态：
//   - 单渠道模式（ModelChannelList）：渠道级选中（channel_base_url 命中该组）。
//   - 多渠道模式（能力路由）：组内全部 Key 都已被勾选（整组多选）。
function isGroupSelected(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (props.singleChannelGroup) return isChannelLevelSelected(group)
  const { ids } = selection.value
  return group.keys.length > 0 && group.keys.every((k) => ids.has(k.id))
}

// 点击组标题 = 选中/取消整个渠道：
//   - 单渠道模式：设为渠道级（channel_base_url，独占；再次点击同组取消）。
//   - 多渠道模式：整组 keys 一起进出 channel_ids，可跨组多选；不写 channel_base_url。
function toggleGroup(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (!group.keys.length) return
  if (props.singleChannelGroup) {
    if (isChannelLevelSelected(group)) {
      applySelection(new Set(), true)
      return
    }
    setChannelLevel(group)
    return
  }
  // 多渠道模式：toggle 整组（并集，保留其他组已选）。
  const sel = selection.value
  const groupIds = new Set(group.keys.map((k) => k.id))
  const allIn = group.keys.every((k) => sel.ids.has(k.id))
  if (allIn) {
    const next = new Set(sel.ids)
    group.keys.forEach((k) => next.delete(k.id))
    applySelection(next, false)
  } else {
    applySelection(new Set([...sel.ids, ...groupIds]), false)
  }
}
function toggleAuto() {
  applySelection(new Set(), true)
}
</script>

<template>
  <div>
    <div v-if="allowAuto" class="mb-3">
      <button type="button" class="w-full rounded-md border px-2 py-1.5 text-xs font-medium transition-colors" :class="!model.channel_id && !model.channel_ids?.length && !model.channel_base_url
          ? 'border-primary bg-primary text-primary-foreground'
          : 'border-border bg-background hover:bg-muted'
        " @click="toggleAuto">
        {{ autoLabel }}
      </button>
    </div>
    <div v-if="layout === 'vertical'" class="space-y-1">
      <template v-for="group in channelGroups" :key="group.baseUrl">
        <div class="rounded-md border p-1 transition-colors"
          :class="isGroupSelected(group) ? 'border-primary/40 bg-primary/5' : 'border-transparent'">
          <button type="button"
            class="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium transition-colors"
            :class="isGroupSelected(group)
                ? 'text-primary'
                : 'text-muted-foreground hover:bg-muted/50'
              " :aria-label="`选择整组：${groupLabel(group)}`" @click="toggleGroup(group)">
            <RiStackLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
            <RiKey2Line v-else class="size-3.5 shrink-0 opacity-50" />
            <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
            <RiCheckLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
          </button>
          <div class="mt-0.5 flex flex-wrap gap-1">
            <button v-for="key in group.keys" :key="key.id" type="button"
              class="rounded border px-1.5 py-0.5 text-xs font-medium transition-colors" :class="selection.ids.has(key.id)
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'border-border bg-background hover:bg-muted'
                " @click="toggleKey(key.id)">
              {{ key.name }}
            </button>
          </div>
        </div>
      </template>
    </div>
    <div v-else class="space-y-0">
      <div v-for="group in channelGroups" :key="group.baseUrl"
        class="flex items-center gap-2 rounded-md border p-1.5 transition-colors"
        :class="isGroupSelected(group) ? 'border-primary/40 bg-primary/5' : 'border-transparent'">
        <button type="button"
          class="flex w-36 shrink-0 items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium transition-colors"
          :class="isGroupSelected(group) ? 'text-primary' : 'text-muted-foreground hover:bg-muted/50'
            " :title="groupLabel(group)" @click="toggleGroup(group)">
          <RiStackLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
          <RiKey2Line v-else class="size-3.5 shrink-0 opacity-50" />
          <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
          <RiCheckLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
        </button>
        <div class="flex min-w-0 flex-1 flex-wrap gap-1">
          <button v-for="key in group.keys" :key="key.id" type="button"
            class="rounded border px-1.5 py-0.5 text-xs font-medium transition-colors" :class="selection.ids.has(key.id)
                ? 'border-primary bg-primary text-primary-foreground'
                : 'border-border bg-background hover:bg-muted'
              " @click="toggleKey(key.id)">
            {{ key.name }}
          </button>
        </div>
      </div>
    </div>
    <p v-if="!channelGroups.length" class="px-2 py-3 text-sm text-muted-foreground">
      {{ emptyLabel }}
    </p>
  </div>
</template>
