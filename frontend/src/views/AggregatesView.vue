<script setup lang="ts">
import { ref } from 'vue'
import { RiAddLine, RiRefreshLine } from '@remixicon/vue'
import { useAggregates } from '@/composables/useAggregates'
import { useChannels } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { Aggregate } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import AggregateEditor from '@/components/aggregates/AggregateEditor.vue'
import AggregateTable from '@/components/aggregates/AggregateTable.vue'

const aggregateService = useAggregates()
const channelService = useChannels()
const {
  data: aggregates,
  loading: aggregateLoading,
  refreshing: aggregatesRefreshing,
  refresh: refreshAggregates,
} = useListLoader(aggregateService.list)
const {
  data: channels,
  loading: channelLoading,
  refreshing: channelsRefreshing,
  refresh: refreshChannels,
} = useListLoader(channelService.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()
const editing = ref<Aggregate>()
const editorOpen = ref(false)
const editorDuplicate = ref(false)
const loading = () => aggregateLoading.value || channelLoading.value
const refreshing = () => aggregatesRefreshing.value || channelsRefreshing.value
async function refresh() {
  await Promise.all([refreshAggregates(), refreshChannels()])
}
function openAdd() {
  editing.value = undefined
  editorDuplicate.value = false
  editorOpen.value = true
}
function openEdit(value: Aggregate) {
  editing.value = value
  editorDuplicate.value = false
  editorOpen.value = true
}
function duplicateName(source: string, taken: Set<string>): string {
  const base = `${source}-copy`
  if (!taken.has(base)) return base
  let i = 2
  while (taken.has(`${source}-copy-${i}`)) i++
  return `${source}-copy-${i}`
}
function openDuplicate(value: Aggregate) {
  // 克隆 targets 并自动追加后缀；按「添加」流程打开，名字可改，保存即真实新增。
  const taken = new Set((aggregates.value || []).map((item) => item.name))
  editing.value = {
    name: duplicateName(value.name, taken),
    enabled: value.enabled ?? true,
    targets: value.targets.map((t) => ({ ...t })),
  }
  editorDuplicate.value = true
  editorOpen.value = true
}
async function save(value: Aggregate) {
  await run(
    'save',
    async () => {
      const next = [
        ...(aggregates.value || []).filter(
          (item) => item.name !== (editing.value?.name || value.name),
        ),
        value,
      ]
      await aggregateService.replaceAll(next)
      editing.value = undefined
      editorOpen.value = false
      await refreshAggregates()
    },
    '聚合模型已保存',
  )
}
async function remove(value: Aggregate) {
  if (!(await confirmDialog(`删除聚合模型「${value.name}」？`))) return
  await run(
    `aggregate:${value.name}:remove`,
    async () => {
      await aggregateService.replaceAll(
        (aggregates.value || []).filter((item) => item.name !== value.name),
      )
      await refreshAggregates()
    },
    '聚合模型已删除',
  )
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="聚合模型"
      description="为调用方提供一个虚拟模型名，按你设定的模型和渠道顺序自动切换。"
      ><template #actions
        ><Button variant="outline" :disabled="loading() || refreshing()" @click="refresh"
          ><RiRefreshLine :class="{ 'animate-spin': refreshing() }" size="16" />刷新 </Button
        ><Button @click="openAdd"> <RiAddLine size="16" />添加聚合模型 </Button></template
      >
    </PageHeader>
    <AggregateEditor
      v-model:open="editorOpen"
      :aggregate="editing"
      :channels="channels || []"
      :pending="isPending('save')"
      :duplicate="editorDuplicate"
      @save="save"
      @cancel="editorOpen = false"
    />
    <LoadingBlock v-if="loading()" />
    <AggregateTable
      v-else
      :aggregates="aggregates || []"
      :channels="channels || []"
      :is-pending="isPending"
      @edit="openEdit"
      @duplicate="openDuplicate"
      @remove="remove"
    />
  </div>
</template>
