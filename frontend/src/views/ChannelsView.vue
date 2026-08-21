<script setup lang="ts">
import { ref } from 'vue'
import { RiAddLine, RiRefreshLine } from '@remixicon/vue'
import { useChannels, groupChannelsByBaseURL, normalizeBaseURL } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { Channel } from '@/lib/types'
import type { ChannelInput } from '@/composables/useChannels'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ChannelEditor from '@/components/channels/ChannelEditor.vue'
import ChannelTable from '@/components/channels/ChannelTable.vue'

const service = useChannels()
const { data, loading, refreshing, refresh } = useListLoader(service.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()
const editing = ref<Channel>()
const editorOpen = ref(false)
/** 非空 = "添加 Key" 模式，base_url 锁定为该组地址 */
const lockBaseUrl = ref('')
/** 添加 Key 时展示的所属渠道组名称（同组首个 Key 的 channel_name 兜底 name） */
const groupName = ref('')

// 操作 key：组操作锁组，key 操作锁 key，编辑器保存用全局 key。
// ChannelTable 内按钮 :disabled 与 ChannelsView 内 run() 必须使用同一套 key。
function groupKey(baseUrl: string, action: string) {
  return `group:${normalizeBaseURL(baseUrl)}:${action}`
}
function keyKey(channel: Channel, action: string) {
  return `key:${channel.id}:${action}`
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
async function save(input: ChannelInput) {
  await run('save', async () => {
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
      // 首次创建/用户未提供任何模型：使用后端探测结果（handleChannelCreateDB
      // 仅在 POST 时探测，candidates 为空通常意味着走的就是这条路径）。
      const list = (saved?.models || []).map((model) => ({ model, enabled: true }))
      if (list.length) {
        await service.replaceModels(id, list)
      }
    }
    editing.value = undefined
    lockBaseUrl.value = ''
    editorOpen.value = false
    await refresh()
  }, '渠道已保存')
}
function groupKeys(baseUrl: string): Channel[] {
  // baseUrl 来自 ChannelTable 已 normalize 的组标识；channel.base_url 原样存储，
  // 按归一化后的字符串比较，兼容尾斜杠差异。
  const target = normalizeBaseURL(baseUrl)
  return (data.value || []).filter((ch) => normalizeBaseURL(ch.base_url) === target)
}
async function toggleKey(channel: Channel) {
  const enabled = channel.manual_enabled ?? channel.enabled ?? true
  await run(keyKey(channel, 'toggle'), async () => {
    await service.setEnabled(channel.id, !enabled)
    await refresh()
  })
}
async function refreshKey(channel: Channel) {
  await run(keyKey(channel, 'refresh'), async () => {
    await service.refreshModels(channel.id)
    await refresh()
  }, '模型列表已刷新')
}
async function refreshGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  await run(groupKey(baseUrl, 'refresh'), async () => {
    for (const key of keys) await service.refreshModels(key.id)
    await refresh()
  }, '模型列表已刷新')
}
async function removeKey(channel: Channel) {
  if (!(await confirmDialog(`删除 Key「${channel.name}」？`))) return
  await run(keyKey(channel, 'remove'), async () => {
    await service.remove(channel.id)
    await refresh()
  }, 'Key 已删除')
}
async function removeGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  if (!(await confirmDialog(`删除渠道「${baseUrl}」及其全部 ${keys.length} 个 Key？`))) return
  await run(groupKey(baseUrl, 'remove'), async () => {
    for (const key of keys) await service.remove(key.id)
    await refresh()
  }, '渠道已删除')
}
async function moveGroup(baseUrl: string, direction: 'up' | 'down') {
  const groups = groupChannelsByBaseURL(data.value || [])
  const index = groups.findIndex((group) => group.baseUrl === baseUrl)
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
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="渠道与模型"
      description="同一 Base URL 的多个 Key 归为一个渠道组；配置上游服务、刷新模型目录，并控制普通模型的候选顺序。"
      ><template #actions
        ><Button variant="outline" :disabled="loading || refreshing" @click="refresh"
          ><RiRefreshLine :class="{ 'animate-spin': refreshing }" size="16" />刷新</Button
        ><Button @click="openAdd"><RiAddLine size="16" />添加渠道</Button></template
      ></PageHeader
    >    <ChannelEditor
      v-model:open="editorOpen"
      :channel="editing"
      :lock-base-url="lockBaseUrl"
      :group-name="groupName"
      :pending="isPending('save')"
      @save="save"
      @cancel="editorOpen = false"
    /><LoadingBlock v-if="loading" /><ChannelTable
      v-else
      :channels="data || []"
      :is-pending="isPending"
      @add-key="openAddKey"
      @toggle-key="toggleKey"
      @refresh-key="refreshKey"
      @edit-key="openEdit"
      @move-key="moveKey"
      @remove-key="removeKey"
      @refresh-group="refreshGroup"
      @move-group="moveGroup"
      @remove-group="removeGroup"
    />
  </div>
</template>
