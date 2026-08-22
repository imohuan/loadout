<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  RiLoader4Line,
  RiRefreshLine,
  RiRestartLine,
  RiStethoscopeLine,
  RiListCheck,
  RiGridLine,
  RiExpandHeightLine,
  RiCollapseVerticalLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useModelStatus } from '@/composables/useModelStatus'
import type { ModelStatusFilters } from '@/composables/useModelStatus'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ModelStatusFiltersForm from '@/components/model-status/ModelStatusFilters.vue'
import ModelStatusList from '@/components/model-status/ModelStatusList.vue'

const service = useModelStatus()
const { data: rawData, loading } = useListLoader(service.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()

// 操作 key：模型状态页所有按钮级 loading 的唯一来源。
// 子组件（ModelStatusList → ChannelGroup → Channel）内部按钮用相同规则生成 key。
function msKey(channelId: string, action: string) {
  return `ms:${channelId}:${action}`
}

const filters = ref<ModelStatusFilters>({})
const mode = ref<'table' | 'tags'>('table')

// 全部展开/折叠联动控制：'expanded' 让所有一二级菜单强制展开，'collapsed' 反之。
// null 表示不联动（保留各菜单当前的本地手动态，便于刷新/筛选后不强行覆盖用户的偏好）。
const expandAll = ref<'expanded' | 'collapsed' | null>(null)

function toggleExpandAll() {
  // 按钮主动切换：当前为展开则折叠，否则展开。null 视为"未激活"，点击后立刻进入展开态。
  expandAll.value = expandAll.value === 'expanded' ? 'collapsed' : 'expanded'
}

/** 将 * 通配符转为正则 */
function wildcardToRegex(pattern: string): RegExp {
  const escaped = pattern.replace(/[.+^${}()|[\]\\]/g, '\\$&')
  const regexStr = escaped.replace(/\*/g, '.*')
  return new RegExp(`^${regexStr}$`, 'i')
}

/** 模型名是否匹配搜索词（支持 * 通配符 + 模糊包含） */
function modelMatches(modelName: string, query?: string): boolean {
  if (!query || !query.trim()) return true
  const q = query.trim()
  if (q.includes('*')) return wildcardToRegex(q).test(modelName)
  return modelName.toLowerCase().includes(q.toLowerCase())
}

const data = computed(() => {
  const items = rawData.value || []
  // 是否有任一筛选条件生效：没有任何筛选就直接返回原数据（零开销）
  const hasFilters =
    !!filters.value.model || filters.value.manual_enabled !== undefined || !!filters.value.status
  if (!hasFilters) return items
  return items
    .map((channel) => {
      const filteredModels = channel.models.filter((m) => {
        if (!modelMatches(m.model, filters.value.model)) return false
        if (filters.value.manual_enabled !== undefined && m.manual_enabled !== filters.value.manual_enabled)
          return false
        if (filters.value.status && m.health_status !== filters.value.status) return false
        return true
      })
      // 任意筛选后该 Key 下没有匹配模型，整行直接隐藏，避免「0 / 0 个模型」的空壳继续占视觉空间。
      // 上层 List 按 base_url 再聚合：若某 base_url 下所有 Key 都被隐藏，对应的 group 也会自动消失。
      if (filteredModels.length === 0) return null
      return { ...channel, models: filteredModels }
    })
    .filter(Boolean) as ChannelStatus[]
})

async function applyFilters(next: ModelStatusFilters) {
  await run('filter', async () => {
    filters.value = next
  })
}
function resetFilters() {
  filters.value = {}
}

async function fetchFresh(): Promise<ChannelStatus[] | null> {
  try {
    return await service.list()
  } catch (error) {
    toast.error(error instanceof Error ? error.message : '刷新失败')
    return null
  }
}

// 无损刷新：静默拉取最新数据后，仅替换目标渠道，不触发 loading、不影响其他渠道与展开状态
async function patchChannel(id: string) {
  const fresh = await fetchFresh()
  if (!fresh || !rawData.value) return
  const next = fresh.find((c) => c.channel.id === id)
  if (!next) return
  const idx = rawData.value.findIndex((c) => c.channel.id === id)
  if (idx !== -1) rawData.value[idx] = next
}

// 静默全量刷新：不置 loading，避免整表闪烁（用于健康检查、手动刷新）
async function silentRefresh() {
  await run('refresh', async () => {
    const fresh = await fetchFresh()
    if (fresh) rawData.value = fresh
  })
}

async function channelToggle(item: ChannelStatus, enabled: boolean) {
  await run(msKey(item.channel.id, 'toggle'), async () => {
    await service.setChannel(item.channel.id, enabled)
    await patchChannel(item.channel.id)
  }, '渠道开关已更新')
}
async function modelToggle(item: ChannelStatus, model: ModelStatus, enabled: boolean) {
  await run(msKey(item.channel.id, `model-toggle:${model.model}`), async () => {
    await service.setModel(item.channel.id, model.model, enabled)
    await patchChannel(item.channel.id)
  }, '模型开关已更新')
}
async function deleteModel(item: ChannelStatus, model: ModelStatus) {
  const confirmed = await confirmDialog({
    title: `删除模型「${model.model}」？`,
    description: '将同时清除该模型的健康状态记录（失败计数、手动开关等）。此操作不可恢复。',
    confirmText: '删除',
  })
  if (!confirmed) return
  await run(msKey(item.channel.id, `model-delete:${model.model}`), async () => {
    await service.deleteModel(item.channel.id, model.model)
    await patchChannel(item.channel.id)
  }, '模型已删除')
}
async function batchModelToggle(item: ChannelStatus, models: ModelStatus[], enabled: boolean) {
  const action = enabled ? '开启' : '关闭'
  const confirmed = await confirmDialog({
    title: `批量${action} ${models.length} 个模型？`,
    description: `将${action}「${item.channel.name}」下 ${models.length} 个模型的手动开关。`,
    confirmText: action,
  })
  if (!confirmed) return
  await run(msKey(item.channel.id, `batch-toggle:${enabled ? 'on' : 'off'}`), async () => {
    await service.setModels(item.channel.id, models.map((m) => m.model), enabled)
    await patchChannel(item.channel.id)
  }, `已${action} ${models.length} 个模型`)
}
async function batchRecoverModel(item: ChannelStatus, models: ModelStatus[]) {
  await run(msKey(item.channel.id, 'batch-recover'), async () => {
    await service.recoverModels(item.channel.id, models.map((m) => m.model))
    await patchChannel(item.channel.id)
  }, `已恢复 ${models.length} 个模型`)
}
async function batchDeleteModel(item: ChannelStatus, models: ModelStatus[]) {
  const manualOnly = models.filter((m) => m.source === 'manual')
  if (manualOnly.length === 0) {
    toast.info('所选模型中没有手动添加的，无法删除')
    return
  }
  const confirmed = await confirmDialog({
    title: `批量删除 ${manualOnly.length} 个手动模型？`,
    description: `将删除「${item.channel.name}」下 ${manualOnly.length} 个手动添加的模型及其健康记录。自动探测的模型会被跳过。`,
    confirmText: '删除',
  })
  if (!confirmed) return
  await run(msKey(item.channel.id, 'batch-delete'), async () => {
    await service.deleteModels(item.channel.id, manualOnly.map((m) => m.model))
    await patchChannel(item.channel.id)
  }, `已删除 ${manualOnly.length} 个手动模型`)
}
async function recoverChannel(item: ChannelStatus) {
  await run(msKey(item.channel.id, 'recover'), async () => {
    await service.recoverChannel(item.channel.id)
    await patchChannel(item.channel.id)
  }, '渠道已恢复')
}
async function recoverModel(item: ChannelStatus, model: ModelStatus) {
  await run(msKey(item.channel.id, `model-recover:${model.model}`), async () => {
    await service.recoverModel(item.channel.id, model.model)
    await patchChannel(item.channel.id)
  }, '模型已恢复')
}
async function check() {
  await run('check', async () => {
    await service.check()
    await silentRefresh()
  }, '健康检查已启动')
}
async function recoverAllModels(item: ChannelStatus) {
  // 计数当前渠道内"非正常"条目，给用户一个明确的预期
  const summary = (item.models || []).reduce(
    (acc, m) => {
      if (!m.effective_available) acc.disabled += 1
      return acc
    },
    { disabled: 0 },
  )
  if (summary.disabled === 0) {
    toast.info('当前渠道没有需要恢复的异常模型')
    return
  }
  const confirmed = await confirmDialog({
    title: `恢复「${item.channel.name}」全部异常模型？`,
    description: `将一键开启该渠道 ${summary.disabled} 个被自动熔断或手动关闭的模型，并清空该渠道所有自动失败计数。此操作会覆盖你主动关闭的开关。`,
    confirmText: '恢复全部',
  })
  if (!confirmed) return
  await run(msKey(item.channel.id, 'recover-all'), async () => {
    await service.recoverAllByChannel(item.channel.id)
    await patchChannel(item.channel.id)
  }, `已恢复「${item.channel.name}」全部异常模型`)
}

// 全平台操作：恢复所有渠道的自动熔断（只清渠道状态，不碰模型开关）
async function recoverAllChannelsGlobal() {
  const affected = (data.value || []).filter((ch) => !ch.effective_available).length
  if (affected === 0) {
    toast.info('没有需要恢复的异常渠道')
    return
  }
  const confirmed = await confirmDialog({
    title: '全平台恢复所有异常渠道？',
    description: `将清空 ${affected} 个异常渠道的自动熔断与失败计数，恢复其自动状态。此操作不会改动任何模型的手动开关。`,
    confirmText: '恢复全部渠道',
  })
  if (!confirmed) return
  await run('recover-all-channels', async () => {
    await service.recoverAllChannels()
    await silentRefresh()
  }, '已恢复全部异常渠道')
}

// 全平台操作：恢复所有渠道的全部异常模型（清熔断 + 强制打开手动开关）
async function recoverAllModelsGlobal() {
  const summary = (data.value || []).reduce(
    (acc, ch) => {
      ch.models.forEach((m) => {
        if (!m.effective_available) acc.disabled += 1
      })
      return acc
    },
    { disabled: 0 },
  )
  if (summary.disabled === 0) {
    toast.info('全平台没有需要恢复的异常模型')
    return
  }
  const confirmed = await confirmDialog({
    title: '全平台恢复全部异常模型？',
    description: `将一键开启全平台 ${summary.disabled} 个被自动熔断或手动关闭的模型，并清空所有自动失败计数。此操作会覆盖你主动关闭的开关。`,
    confirmText: '恢复全部',
  })
  if (!confirmed) return
  await run('recover-all-models', async () => {
    await service.recoverAll()
    await silentRefresh()
  }, '已恢复全平台全部异常模型')
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="模型状态" description="手动开关与自动健康状态分别管理；自动状态不会重新打开手动关闭的对象。"><template #actions><Button
          variant="outline" :disabled="isPending('refresh')" @click="silentRefresh">
          <RiLoader4Line v-if="isPending('refresh')" class="animate-spin" size="16" /><RiRefreshLine v-else size="16" />刷新
        </Button><Button :disabled="isPending('check')" @click="check">
          <RiLoader4Line v-if="isPending('check')" class="animate-spin" size="16" /><RiStethoscopeLine v-else size="16" />健康检查
        </Button><Button variant="outline" :disabled="isPending('recover-all-channels')" @click="recoverAllChannelsGlobal">
          <RiLoader4Line v-if="isPending('recover-all-channels')" class="animate-spin" size="16" /><RiRefreshLine v-else size="16" />全平台恢复渠道
        </Button><Button variant="outline" :disabled="isPending('recover-all-models')" @click="recoverAllModelsGlobal">
          <RiLoader4Line v-if="isPending('recover-all-models')" class="animate-spin" size="16" /><RiRestartLine v-else size="16" />全平台恢复全部异常
        </Button></template></PageHeader>
    <div class="flex items-center gap-3">
      <ModelStatusFiltersForm class="flex-1" :is-pending="isPending" @apply="applyFilters" @reset="resetFilters" />

      <TooltipProvider>
        <div class="flex shrink-0 min-w-32 items-center justify-end gap-1">
          <div class="space-y-1">
            <Label for="ms-model">显示状态</Label>

            <Tooltip>
              <TooltipTrigger as-child><Button size="sm" variant="ghost" :class="mode === 'table' ? 'bg-muted' : ''"
                  @click="mode = 'table'">
                  <RiListCheck size="16" />
                </Button></TooltipTrigger>
              <TooltipContent>表格模式</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger as-child><Button size="sm" variant="ghost" :class="mode === 'tags' ? 'bg-muted' : ''"
                  @click="mode = 'tags'">
                  <RiGridLine size="16" />
                </Button></TooltipTrigger>
              <TooltipContent>标签模式</TooltipContent>
            </Tooltip>
            <!--
              一键折叠/展开：覆盖所有一二级菜单（含嵌套的 Key 行）。
              图标随当前状态切换：已展开 → 折叠图标（提示即将折叠）；其他 → 展开图标。
            -->
            <Tooltip>
              <TooltipTrigger as-child>
                <Button size="sm" variant="ghost" @click="toggleExpandAll">
                  <RiCollapseVerticalLine v-if="expandAll === 'expanded'" size="16" />
                  <RiExpandHeightLine v-else size="16" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>{{ expandAll === 'expanded' ? '全部折叠' : '全部展开' }}</TooltipContent>
            </Tooltip>
          </div>
        </div>
      </TooltipProvider>
    </div>
    <LoadingBlock v-if="loading" />
    <ModelStatusList v-else :items="data || []" :mode="mode" :is-pending="isPending" :expand-all="expandAll"
      @channel-toggle="channelToggle"
      @model-toggle="modelToggle" @recover-channel="recoverChannel" @recover-model="recoverModel"
      @recover-all-models="recoverAllModels" @batch-model-toggle="batchModelToggle"
      @batch-recover-model="batchRecoverModel" @batch-delete-model="batchDeleteModel" @delete-model="deleteModel" />
  </div>
</template>
