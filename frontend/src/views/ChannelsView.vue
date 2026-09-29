<script setup lang="ts">
// 「渠道与模型」页：两层结构。
//
// 状态一（渠道总览）：一个平台一张 tag，点 tag 进入状态二。
// 状态二（平台 · Key 详情）：左栏平台下拉 + Key 列表，右栏是选中 Key 的详情与操作。
//
// 分组与汇总口径统一走 lib/channels 的纯函数（summarizeChannelPlatforms），
// 页面只负责取数据、切状态、把事件转发给 service。
import { computed, ref, watch } from 'vue'
import { RiAddLine, RiArrowLeftSLine, RiRefreshLine } from '@remixicon/vue'
import { useChannels, normalizeBaseURL, type ChannelInput } from '@/composables/useChannels'
import { summarizeChannelPlatforms, channelModelCount } from '@/lib/channels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { Channel } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import ChannelEditor from '@/components/channels/ChannelEditor.vue'
import ChannelModelSyncDialog from '@/components/channels/ChannelModelSyncDialog.vue'
import ChannelPlatformGrid from '@/components/channels/ChannelPlatformGrid.vue'
import ChannelKeyList from '@/components/channels/ChannelKeyList.vue'
import ChannelKeyDetail from '@/components/channels/ChannelKeyDetail.vue'

const service = useChannels()
const { data, loading, refreshing, refresh } = useListLoader(service.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()

/** 展示层状态：渠道总览 / 平台内 Key 详情。 */
const state = ref<'platforms' | 'keys'>('platforms')
const selectedBaseUrl = ref('')
const selectedKeyId = ref('')

const editing = ref<Channel>()
const editorOpen = ref(false)
/** 模型同步弹窗：记录触发它的那个渠道组（base_url），弹窗只在组内选源和目标 */
const syncBaseUrl = ref('')
const syncOpen = ref(false)
/** 非空 = "添加 Key" 模式，base_url 锁定为该组地址 */
const lockBaseUrl = ref('')
/** 添加 Key 时展示的所属渠道组名称（同组首个 Key 的 channel_name 兜底 name） */
const groupName = ref('')

const platforms = computed(() => summarizeChannelPlatforms(data.value || []))

/** 当前平台：优先按选中 base_url 找，找不到回落到第一个。 */
const activePlatform = computed(
  () => platforms.value.find((p) => p.baseUrl === selectedBaseUrl.value) || platforms.value[0],
)
/** 当前平台在列表中的下标（整组上移/下移的禁用判断）。 */
const activePlatformIndex = computed(() =>
  platforms.value.findIndex((p) => p.baseUrl === activePlatform.value?.baseUrl),
)
/** 当前 Key：优先按选中 id 找，找不到回落到该平台第一个 Key。 */
const activeKey = computed(() => {
  const keys = activePlatform.value?.keys || []
  return keys.find((k) => k.id === selectedKeyId.value) || keys[0]
})
/** 当前 Key 在组内下标。 */
const activeKeyIndex = computed(() => {
  const keys = activePlatform.value?.keys || []
  return keys.findIndex((k) => k.id === activeKey.value?.id)
})

// 平台筛选后原来选中的平台/Key 可能消失：这里把选中值同步回落，避免右栏空转。
watch(
  activePlatform,
  (p) => {
    if (p && p.baseUrl !== selectedBaseUrl.value) {
      selectedBaseUrl.value = p.baseUrl
      selectedKeyId.value = p.keys[0]?.id || ''
    }
  },
  { immediate: true },
)
watch(activeKey, (k) => {
  if (k && k.id !== selectedKeyId.value) selectedKeyId.value = k.id
})

// 操作 key：组操作锁组，key 操作锁 key，编辑器保存用全局 key。
// 子组件内按钮 :disabled 与这里 run() 必须使用同一套 key。
function groupKey(baseUrl: string, action: string) {
  return `group:${normalizeBaseURL(baseUrl)}:${action}`
}
function keyKey(channel: Channel, action: string) {
  return `key:${channel.id}:${action}`
}

function openPlatform(baseUrl: string) {
  selectedBaseUrl.value = normalizeBaseURL(baseUrl)
  selectedKeyId.value = ''
  state.value = 'keys'
}
function selectPlatform(baseUrl: string) {
  selectedBaseUrl.value = normalizeBaseURL(baseUrl)
  selectedKeyId.value = ''
}
function selectKey(key: Channel) {
  selectedKeyId.value = key.id
}
function backToPlatforms() {
  state.value = 'platforms'
}

function openAdd() {
  editing.value = undefined
  lockBaseUrl.value = ''
  groupName.value = ''
  editorOpen.value = true
}
function openAddKey(baseUrl: string) {
  editing.value = undefined
  lockBaseUrl.value = baseUrl
  const keys = groupKeys(baseUrl)
  groupName.value = keys[0]?.channel_name || keys[0]?.name || ''
  editorOpen.value = true
}
function openEdit(channel: Channel) {
  editing.value = channel
  lockBaseUrl.value = ''
  groupName.value = ''
  editorOpen.value = true
}
function groupKeys(baseUrl: string): Channel[] {
  // baseUrl 来自已 normalize 的组标识；channel.base_url 原样存储，
  // 按归一化后的字符串比较，兼容尾斜杠差异。
  const target = normalizeBaseURL(baseUrl)
  const summary = platforms.value.find((p) => p.baseUrl === target)
  if (summary) return summary.keys
  return (data.value || []).filter((ch) => normalizeBaseURL(ch.base_url) === target)
}

async function save(input: ChannelInput) {
  await run(
    'save',
    async () => {
      const saved = await service.save(input, editing.value?.id)
      // 模型清单：完全以用户在编辑器里看到的候选/选择为准，不把后端探测结果
      // 当新增候选自动并入——后端编辑时不再无条件探测，由用户通过「获取模型」
      // 显式控制探测；只有首次创建（candidates 为空）才用后端探测结果兜底。
      const id = editing.value?.id || saved?.id
      const candidates = input.model_candidates || []
      if (id && candidates.length) {
        const enabled = new Set(input.models || [])
        await service.replaceModels(
          id,
          candidates.map((model) => ({ model, enabled: enabled.has(model) })),
        )
      } else if (id) {
        const list = (saved?.models || []).map((model) => ({ model, enabled: true }))
        if (list.length) {
          await service.replaceModels(id, list)
        }
      }
      editing.value = undefined
      lockBaseUrl.value = ''
      editorOpen.value = false
      await refresh()
    },
    '渠道已保存',
  )
}

async function toggleKey(channel: Channel) {
  const enabled = channel.manual_enabled ?? channel.enabled ?? true
  await run(keyKey(channel, 'toggle'), async () => {
    await service.setEnabled(channel.id, !enabled)
    await refresh()
  })
}
async function refreshKey(channel: Channel) {
  await run(
    keyKey(channel, 'refresh'),
    async () => {
      await service.refreshModels(channel.id)
      await refresh()
    },
    '模型列表已刷新',
  )
}
function openSync(baseUrl: string) {
  syncBaseUrl.value = normalizeBaseURL(baseUrl)
  syncOpen.value = true
}
// 同步模型：把弹窗里编辑好的模型清单全量写进选中的 Key。
// 服务端是 PUT /api/channels/{id}/models（全量替换），并发下发后统一刷新列表；
// 失败由 useAsyncTask 统一提示（已成功的 Key 已生效）。
async function syncModels(payload: { channelIds: string[]; models: string[] }) {
  await run(
    'sync-models',
    async () => {
      await Promise.all(
        payload.channelIds.map((id) =>
          service.replaceModels(
            id,
            payload.models.map((model) => ({ model, enabled: true })),
          ),
        ),
      )
      syncOpen.value = false
      await refresh()
    },
    `模型已同步到 ${payload.channelIds.length} 个 Key`,
  )
}
async function refreshGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  await run(
    groupKey(baseUrl, 'refresh'),
    async () => {
      for (const key of keys) await service.refreshModels(key.id)
      await refresh()
    },
    '模型列表已刷新',
  )
}
async function removeKey(channel: Channel) {
  if (!(await confirmDialog(`删除 Key「${channel.name}」？`))) return
  await run(
    keyKey(channel, 'remove'),
    async () => {
      await service.remove(channel.id)
      await refresh()
    },
    'Key 已删除',
  )
}
async function removeGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  if (!(await confirmDialog(`删除渠道「${baseUrl}」及其全部 ${keys.length} 个 Key？`))) return
  await run(
    groupKey(baseUrl, 'remove'),
    async () => {
      for (const key of keys) await service.remove(key.id)
      await refresh()
    },
    '渠道已删除',
  )
}
async function moveGroup(baseUrl: string, direction: 'up' | 'down') {
  const groups = platforms.value.map((p) => ({ baseUrl: p.baseUrl, keys: p.keys }))
  const index = groups.findIndex((group) => group.baseUrl === normalizeBaseURL(baseUrl))
  const target = direction === 'up' ? index - 1 : index + 1
  if (index < 0 || target < 0 || target >= groups.length) return
  ;[groups[index], groups[target]] = [groups[target], groups[index]]
  const ids = groups.flatMap((group) => group.keys.map((key) => key.id))
  await run(groupKey(baseUrl, `move-${direction}`), async () => {
    await service.reorder(ids)
    await refresh()
  })
}
// 组内 Key 上下移动：调整单 key 在组内的 position（影响该渠道下多 key 的 failover 顺序）。
async function moveKey(channel: Channel, direction: 'up' | 'down') {
  const channels = data.value || []
  const keys = groupKeys(channel.base_url)
  const idxInGroup = keys.findIndex((k) => k.id === channel.id)
  const target = direction === 'up' ? idxInGroup - 1 : idxInGroup + 1
  if (idxInGroup < 0 || target < 0 || target >= keys.length) return
  const targetKey = keys[target]
  const i = channels.findIndex((c) => c.id === channel.id)
  const j = channels.findIndex((c) => c.id === targetKey.id)
  if (i < 0 || j < 0) return
  const next = [...channels]
  ;[next[i], next[j]] = [next[j], next[i]]
  await run(keyKey(channel, `move-${direction}`), async () => {
    await service.reorder(next.map((c) => c.id))
    await refresh()
  })
}

/** 总览页脚：模型总数与探测失败的 Key 数（让人一眼看出有没有漏配）。 */
const totals = computed(() => {
  const list = platforms.value
  const failed = (data.value || []).filter((ch) => channelModelCount(ch) < 0).length
  return {
    platforms: list.length,
    models: new Set(list.flatMap((p) => p.models)).size,
    failedKeys: failed,
  }
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="渠道与模型"
      description="同一 Base URL 的多个 Key 归为一个渠道组；配置上游服务、刷新模型目录，并控制普通模型的候选顺序。"
    >
      <template #actions>
        <Button variant="outline" :disabled="loading || refreshing" @click="refresh">
          <RiRefreshLine :class="{ 'animate-spin': refreshing }" size="16" />刷新
        </Button>
        <Button @click="openAdd"><RiAddLine size="16" />添加渠道</Button>
      </template>
    </PageHeader>

    <ChannelEditor
      v-model:open="editorOpen"
      :channel="editing"
      :lock-base-url="lockBaseUrl"
      :group-name="groupName"
      :pending="isPending('save')"
      @save="save"
      @cancel="editorOpen = false"
    />
    <ChannelModelSyncDialog
      v-model:open="syncOpen"
      :channels="data || []"
      :base-url="syncBaseUrl"
      :pending="isPending('sync-models')"
      @sync="syncModels"
    />

    <LoadingBlock v-if="loading" />

    <template v-else>
      <!-- ============ 状态一：渠道总览 ============ -->
      <template v-if="state === 'platforms'">
        <div class="flex items-center justify-between gap-3">
          <p class="text-xs tabular-nums text-muted-foreground">
            {{ totals.platforms }} 个平台 · {{ totals.models }} 个模型 ·
            <span :class="totals.failedKeys ? 'text-amber-600 dark:text-amber-400' : ''">
              {{ totals.failedKeys }} 个 Key 探测失败
            </span>
          </p>
        </div>
        <ChannelPlatformGrid :platforms="platforms" @select="openPlatform" />
        <EmptyState
          v-if="!platforms.length"
          title="还没有配置渠道"
          description="点右上角「添加渠道」配置第一个上游服务。"
        />
      </template>

      <!-- ============ 状态二：平台 · Key 详情 ============ -->
      <div v-else class="space-y-3">
        <div class="flex items-center gap-2 text-sm text-muted-foreground">
          <Button
            variant="ghost"
            size="sm"
            class="gap-0.5 pl-1 text-muted-foreground"
            @click="backToPlatforms"
          >
            <RiArrowLeftSLine size="16" />渠道总览
          </Button>
          <span class="text-border">/</span>
          <span class="font-medium text-foreground">{{ activePlatform?.name }}</span>
        </div>

        <div
          class="flex min-h-[32rem] flex-col overflow-hidden rounded-lg border border-border md:flex-row!"
        >
          <ChannelKeyList
            :platforms="platforms"
            :active-base-url="activePlatform?.baseUrl || ''"
            :active-key-id="activeKey?.id"
            @select-platform="selectPlatform"
            @select-key="selectKey"
          />
          <ChannelKeyDetail
            v-if="activePlatform"
            :platform="activePlatform"
            :channel="activeKey"
            :key-index="activeKeyIndex"
            :group-index="activePlatformIndex"
            :group-count="platforms.length"
            :is-pending="isPending"
            @add-key="openAddKey"
            @toggle-key="toggleKey"
            @refresh-key="refreshKey"
            @edit-key="openEdit"
            @move-key="moveKey"
            @remove-key="removeKey"
            @sync-models="openSync"
            @refresh-group="refreshGroup"
            @move-group="moveGroup"
            @remove-group="removeGroup"
          />
        </div>
      </div>
    </template>
  </div>
</template>
