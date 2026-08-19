<script setup lang="ts">
import { ref } from 'vue'
import { RiAddLine, RiRefreshLine } from '@remixicon/vue'
import { useChannels, groupChannelsByBaseURL } from '@/composables/useChannels'
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
const { pending, run } = useAsyncTask()
const { confirmDialog } = useConfirm()
const editing = ref<Channel>()
const editorOpen = ref(false)
/** 非空 = "添加 Key" 模式，base_url 锁定为该组地址 */
const lockBaseUrl = ref('')

function openAdd() {
  editing.value = undefined
  lockBaseUrl.value = ''
  editorOpen.value = true
}
function openAddKey(baseUrl: string) {
  editing.value = undefined
  lockBaseUrl.value = baseUrl
  editorOpen.value = true
}
function openEdit(channel: Channel) {
  editing.value = channel
  lockBaseUrl.value = ''
  editorOpen.value = true
}
async function save(input: ChannelInput) {
  await run(async () => {
    const saved = await service.save(input, editing.value?.id)
    // 模型清单：合并 编辑器候选 ∪ 后端探测结果 后全量替换（选中 = 启用，未选 = 禁用）。
    // 探测新发现的模型（编辑器打开时没有的）默认启用，不能丢；
    // 用户见过的候选按用户选择（禁用的保持禁用）。
    const id = editing.value?.id || saved?.id
    const oldCandidates = new Set(input.model_candidates || [])
    const union = new Map<string, boolean>()
    for (const m of saved?.models || []) {
      if (!oldCandidates.has(m)) union.set(m, true) // 新探测模型：默认启用
    }
    for (const m of oldCandidates) {
      union.set(m, (input.models || []).includes(m)) // 见过的候选：按用户选择
    }
    if (id && union.size) {
      await service.replaceModels(
        id,
        [...union.entries()].map(([model, enabled]) => ({ model, enabled })),
      )
    }
    editing.value = undefined
    lockBaseUrl.value = ''
    editorOpen.value = false
    await refresh()
  }, '渠道已保存')
}
function groupKeys(baseUrl: string): Channel[] {
  return (data.value || []).filter((ch) => ch.base_url === baseUrl)
}
async function toggleKey(channel: Channel) {
  const enabled = channel.manual_enabled ?? channel.enabled ?? true
  await run(async () => {
    await service.setEnabled(channel.id, !enabled)
    await refresh()
  })
}
async function refreshKey(channel: Channel) {
  await run(async () => {
    await service.refreshModels(channel.id)
    await refresh()
  }, '模型列表已刷新')
}
async function refreshGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  await run(async () => {
    for (const key of keys) await service.refreshModels(key.id)
    await refresh()
  }, '模型列表已刷新')
}
async function removeKey(channel: Channel) {
  if (!(await confirmDialog(`删除 Key「${channel.name}」？`))) return
  await run(async () => {
    await service.remove(channel.id)
    await refresh()
  }, 'Key 已删除')
}
async function removeGroup(baseUrl: string) {
  const keys = groupKeys(baseUrl)
  if (!(await confirmDialog(`删除渠道「${baseUrl}」及其全部 ${keys.length} 个 Key？`))) return
  await run(async () => {
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
  await run(async () => {
    await service.reorder(ids)
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
    ><ChannelEditor
      v-model:open="editorOpen"
      :channel="editing"
      :lock-base-url="lockBaseUrl"
      :pending="pending"
      @save="save"
      @cancel="editorOpen = false"
    /><LoadingBlock v-if="loading" /><ChannelTable
      v-else
      :channels="data || []"
      :pending="pending"
      @add-key="openAddKey"
      @toggle-key="toggleKey"
      @refresh-key="refreshKey"
      @edit-key="openEdit"
      @remove-key="removeKey"
      @refresh-group="refreshGroup"
      @move-group="moveGroup"
      @remove-group="removeGroup"
    />
  </div>
</template>
