<script setup lang="ts">
// 渠道分组选择器（从 ModelChannelList 抽取的公共组件）。
// 一个渠道组 = 一个 base_url；组内每行 = 一个 Key（账号）。
// 支持两种布局：
//   vertical   = 组标题在上、keys 换行在下（ModelChannelList 原样式）
//   horizontal = 每个渠道一行：左渠道名、右 keys 横向排开
// 选择态统一归一为 { channel_id, channel_ids, channel_base_url }，与 ModelChannelItem 字段兼容。
// channelsOnly：只允许选 Key（禁选渠道级 + 隐藏自动路由）。模型测试页用，
// 让「预设」下拉与编辑聚合模型 dialog 视觉一致，同时禁用点组标题选整组的语义。
import { computed } from 'vue'
import { RiCheckLine, RiKey2Line, RiStackLine } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, normalizeBaseURL } from '@/composables/useChannels'

export interface ChannelSelection {
  channel_id?: string
  channel_ids?: string[]
  channel_base_url?: string
  /** 多渠道级（singleChannelGroup=false 时用；点过渠道名的组，显示无括号） */
  channel_base_urls?: string[]
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
    // 仅 Key 选择：true = 禁用渠道级（点组标题）与自动路由，组标题仅作分组标签展示。
    //   用于模型测试「预设」下拉，确保只选到具体 Key，不点组标题选整组。
    channelsOnly?: boolean
    autoLabel?: string
    emptyLabel?: string
  }>(),
  {
    layout: 'vertical',
    allowAuto: true,
    multiSelect: true,
    singleChannelGroup: true,
    channelsOnly: false,
    autoLabel: '自动路由（按模型找渠道）',
    emptyLabel: '暂无可用渠道',
  },
)

const model = defineModel<ChannelSelection>({ required: true })

// 实际生效的 allowAuto：channelsOnly 模式下强制关闭。
const effectiveAllowAuto = computed(() => props.allowAuto && !props.channelsOnly)

// 渠道按 base_url 分组（渠道组 = 一个渠道；组内 = 各 Key）。
const channelGroups = computed(() => groupChannelsByBaseURL(props.channels))

// 组标题：渠道名。
function groupLabel(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const first = group.keys[0]
  return first?.channel_name || first?.name || group.baseUrl
}

// 当前选择态：{ auto, ids }。ids = 已勾选 Key 的 id 集合（渠道级展开为组内全部 Key）。
const selection = computed(() => {
  const m = model.value
  if (!m) return { auto: true, ids: new Set<string>() }
  if (props.singleChannelGroup && m.channel_base_url && !props.channelsOnly) {
    const group = channelGroups.value.find(
      (g) => normalizeBaseURL(g.baseUrl) === normalizeBaseURL(m.channel_base_url!),
    )
    return { auto: false, ids: new Set((group?.keys || []).map((k) => k.id)) }
  }
  const ids = new Set<string>()
  for (const id of m.channel_ids || []) ids.add(id)
  if (!props.singleChannelGroup && !props.channelsOnly) {
    // 多渠道级展开：渠道级组内的 Key 也算已选。
    for (const bu of m.channel_base_urls || []) {
      const group = channelGroups.value.find(
        (g) => normalizeBaseURL(g.baseUrl) === normalizeBaseURL(bu),
      )
      group?.keys.forEach((k) => ids.add(k.id))
    }
  }
  return { auto: ids.size === 0, ids }
})

// 是否渠道级选中指定组（只有 channel_base_url === group.baseUrl 才算渠道级）。
function isChannelLevelSelected(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const m = model.value
  if (!m?.channel_base_url) return false
  return normalizeBaseURL(m.channel_base_url) === normalizeBaseURL(group.baseUrl)
}

// 多渠道模式下该组是否在渠道级列表里（点过渠道名才算）。
function isMultiChannelLevelSelected(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  const m = model.value
  if (!m?.channel_base_urls?.length) return false
  return m.channel_base_urls.some((bu) => normalizeBaseURL(bu) === normalizeBaseURL(group.baseUrl))
}

// 把勾选态写回 model（单渠道模式，后端字段归一化）。
// 重要：渠道级（channel_base_url）与 Key 多选（channel_ids）是两个独立概念，互不转换。
//   - 渠道级：只能由点击渠道组标题触发。
//   - Key 多选：即使全部 Key 恰好被勾满，也仍然是 channel_ids，不会自动折叠为渠道级。
//   - 空：自动路由。
// channelsOnly 模式：禁用渠道级 → 直接清空 channel_base_url / channel_base_urls。
function applySelection(ids: Set<string>, auto: boolean) {
  const m = model.value
  if (!m) return
  if (props.channelsOnly) {
    // channelsOnly 模式：自动路由不开放，无勾选 = 清空 channel_ids。
    const ordered = channelGroups.value
      .flatMap((g) => g.keys.map((k) => k.id))
      .filter((id) => ids.has(id))
    if (!props.multiSelect && ordered.length > 1) {
      m.channel_id = ''
      m.channel_ids = [ordered[ordered.length - 1]]
    } else {
      m.channel_id = ''
      m.channel_ids = ordered
    }
    m.channel_base_url = ''
    m.channel_base_urls = []
    return
  }
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

// 设为渠道级（点击组标题触发，单渠道模式）。再次点击同组 = 取消渠道级。
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

// ===== 多渠道模式（singleChannelGroup=false，能力路由）=====
// 渠道级与 Key 选择分开存：channel_base_urls（点过渠道名，多选）+ channel_ids（勾过 Key）。
// 保存时由外部把渠道级展开为 Key ids；此处只维护 UI 语义。
// channelsOnly 模式：禁止渠道级 → 不实现多渠道级多选逻辑，只走 channel_ids。

// 多渠道模式：toggle 单个 Key。
//   - 点在渠道级组内 → 该组退出渠道级，改为只选这个 Key。
//   - 普通 → 进/出 channel_ids（跨组自由累加）。
function multiToggleKey(id: string) {
  if (props.channelsOnly) {
    toggleKey(id)
    return
  }
  const m = model.value
  if (!m) return
  const keyGroup = channelGroups.value.find((g) => g.keys.some((k) => k.id === id))
  if (keyGroup && isMultiChannelLevelSelected(keyGroup)) {
    m.channel_base_urls = (m.channel_base_urls || []).filter(
      (bu) => normalizeBaseURL(bu) !== normalizeBaseURL(keyGroup.baseUrl),
    )
    m.channel_ids = [id]
    return
  }
  const next = new Set(m.channel_ids || [])
  if (next.has(id)) next.delete(id)
  else next.add(id)
  m.channel_ids = [...next]
}

// 多渠道模式：toggle 整个渠道（渠道级多选）。
//   - 设为渠道级：加入 channel_base_urls，并移除该组单独勾选的 Key（避免重复）。
//   - 取消渠道级：从 channel_base_urls 移除。
function multiToggleGroup(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (props.channelsOnly) return
  const m = model.value
  if (!m) return
  const base = normalizeBaseURL(group.baseUrl)
  const groupIds = new Set(group.keys.map((k) => k.id))
  if (isMultiChannelLevelSelected(group)) {
    m.channel_base_urls = (m.channel_base_urls || []).filter((bu) => normalizeBaseURL(bu) !== base)
  } else {
    m.channel_base_urls = [...(m.channel_base_urls || []), group.baseUrl]
    m.channel_ids = (m.channel_ids || []).filter((id) => !groupIds.has(id))
  }
}

function toggleKey(id: string) {
  if (!props.singleChannelGroup) {
    multiToggleKey(id)
    return
  }
  const sel = selection.value
  const keyGroup = channelGroups.value.find((g) => g.keys.some((k) => k.id === id))
  const active = activeGroupOf()
  // 单渠道模式：跨组勾选自动切组（清空旧组，只保留新勾选的 Key）。
  if (keyGroup && active && keyGroup.baseUrl !== active.baseUrl) {
    applySelection(new Set([id]), false)
    return
  }
  // 当前是渠道级 → 切到 Key 多选模式（保留组内所有 Key 的勾选视觉，仅切换当前 Key）。
  if (model.value?.channel_base_url) {
    const own = (active?.keys || []).map((k) => k.id)
    const next = new Set(own)
    if (next.has(id)) next.delete(id)
    else next.add(id)
    applySelection(next, false)
    return
  }
  if (sel.ids.has(id)) sel.ids.delete(id)
  else sel.ids.add(id)
  applySelection(sel.ids, false)
}
// 组标题是否呈选中态：
//   - 单渠道模式（ModelChannelList）：渠道级选中（channel_base_url 命中该组）。
//   - 多渠道模式（能力路由）：该组在渠道级列表里（点过渠道名才算，勾满 Key 不算）。
function isGroupSelected(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (props.channelsOnly) return false
  if (props.singleChannelGroup) return isChannelLevelSelected(group)
  return isMultiChannelLevelSelected(group)
}

// 点击组标题 = 选中/取消整个渠道：
//   - 单渠道模式：设为渠道级（channel_base_url，独占；再次点击同组取消）。
//   - 多渠道模式：渠道级多选（channel_base_urls），可多组并存。
//   - channelsOnly：组标题不响应点击，仅作分组标签。
function toggleGroup(group: ReturnType<typeof groupChannelsByBaseURL>[number]) {
  if (props.channelsOnly) return
  if (!group.keys.length) return
  if (props.singleChannelGroup) {
    if (isChannelLevelSelected(group)) {
      applySelection(new Set(), true)
      return
    }
    setChannelLevel(group)
    return
  }
  multiToggleGroup(group)
}
function toggleAuto() {
  applySelection(new Set(), true)
}
</script>

<template>
  <div>
    <div v-if="effectiveAllowAuto" class="mb-3">
      <button
        type="button"
        class="w-full rounded-md border px-2 py-1.5 text-xs font-medium transition-colors"
        :class="
          !model.channel_id && !model.channel_ids?.length && !model.channel_base_url
            ? 'border-primary bg-primary text-primary-foreground'
            : 'border-border bg-background hover:bg-muted'
        "
        @click="toggleAuto"
      >
        {{ autoLabel }}
      </button>
    </div>
    <div v-if="layout === 'vertical'" class="space-y-1">
      <template v-for="group in channelGroups" :key="group.baseUrl">
        <div
          class="rounded-md border p-1 transition-colors"
          :class="isGroupSelected(group) ? 'border-primary/40 bg-primary/5' : 'border-transparent'"
        >
          <!-- channelsOnly 模式：组标题不响应点击，仅作分组标签 -->
          <div
            v-if="channelsOnly"
            class="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium text-muted-foreground"
          >
            <RiStackLine class="size-3.5 shrink-0 opacity-50" />
            <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
          </div>
          <button
            v-else
            type="button"
            class="flex w-full items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium transition-colors"
            :class="
              isGroupSelected(group) ? 'text-primary' : 'text-muted-foreground hover:bg-muted/50'
            "
            :aria-label="`选择整组：${groupLabel(group)}`"
            @click="toggleGroup(group)"
          >
            <RiStackLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
            <RiKey2Line v-else class="size-3.5 shrink-0 opacity-50" />
            <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
            <RiCheckLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
          </button>
          <div class="mt-0.5 flex flex-wrap gap-1">
            <button
              v-for="key in group.keys"
              :key="key.id"
              type="button"
              class="rounded border px-1.5 py-0.5 text-xs font-medium transition-colors"
              :class="
                selection.ids.has(key.id)
                  ? 'border-primary bg-primary text-primary-foreground'
                  : 'border-border bg-background hover:bg-muted'
              "
              @click="toggleKey(key.id)"
            >
              {{ key.name }}
            </button>
          </div>
        </div>
      </template>
    </div>
    <div v-else class="space-y-0">
      <div
        v-for="group in channelGroups"
        :key="group.baseUrl"
        class="flex items-center gap-2 rounded-md border p-1.5 transition-colors"
        :class="isGroupSelected(group) ? 'border-primary/40 bg-primary/5' : 'border-transparent'"
      >
        <!-- channelsOnly 模式：组标题不响应点击，仅作分组标签 -->
        <div
          v-if="channelsOnly"
          class="flex w-36 shrink-0 items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium text-muted-foreground"
          :title="groupLabel(group)"
        >
          <RiStackLine class="size-3.5 shrink-0 opacity-50" />
          <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
        </div>
        <button
          v-else
          type="button"
          class="flex w-36 shrink-0 items-center gap-1 rounded px-1 py-0.5 text-left text-xs font-medium transition-colors"
          :class="
            isGroupSelected(group) ? 'text-primary' : 'text-muted-foreground hover:bg-muted/50'
          "
          :title="groupLabel(group)"
          @click="toggleGroup(group)"
        >
          <RiStackLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
          <RiKey2Line v-else class="size-3.5 shrink-0 opacity-50" />
          <span class="flex-1 truncate">{{ groupLabel(group) }}</span>
          <RiCheckLine v-if="isGroupSelected(group)" class="size-3.5 shrink-0" />
        </button>
        <div class="flex min-w-0 flex-1 flex-wrap gap-1">
          <button
            v-for="key in group.keys"
            :key="key.id"
            type="button"
            class="rounded border px-1.5 py-0.5 text-xs font-medium transition-colors"
            :class="
              selection.ids.has(key.id)
                ? 'border-primary bg-primary text-primary-foreground'
                : 'border-border bg-background hover:bg-muted'
            "
            @click="toggleKey(key.id)"
          >
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
