<script setup lang="ts">
import { RiRefreshLine, RiRestartLine, RiStethoscopeLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useModelStatus } from '@/composables/useModelStatus'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import ModelStatusList from '@/components/model-status/ModelStatusList.vue'

const service = useModelStatus()
const { data, loading } = useListLoader(service.list)
const { pending, run } = useAsyncTask()
const { confirmDialog } = useConfirm()

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
  if (!fresh || !data.value) return
  const next = fresh.find((c) => c.channel.id === id)
  if (!next) return
  const idx = data.value.findIndex((c) => c.channel.id === id)
  if (idx !== -1) data.value[idx] = next
}

// 静默全量刷新：不置 loading，避免整表闪烁（用于健康检查、手动刷新）
async function silentRefresh() {
  const fresh = await fetchFresh()
  if (fresh) data.value = fresh
}

async function channelToggle(item: ChannelStatus, enabled: boolean) {
  await run(async () => {
    await service.setChannel(item.channel.id, enabled)
    await patchChannel(item.channel.id)
  }, '渠道开关已更新')
}
async function modelToggle(item: ChannelStatus, model: ModelStatus, enabled: boolean) {
  await run(async () => {
    await service.setModel(item.channel.id, model.model, enabled)
    await patchChannel(item.channel.id)
  }, '模型开关已更新')
}
async function recoverChannel(item: ChannelStatus) {
  await run(async () => {
    await service.recoverChannel(item.channel.id)
    await patchChannel(item.channel.id)
  }, '渠道已恢复')
}
async function recoverModel(item: ChannelStatus, model: ModelStatus) {
  await run(async () => {
    await service.recoverModel(item.channel.id, model.model)
    await patchChannel(item.channel.id)
  }, '模型已恢复')
}
async function check() {
  await run(async () => {
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
  await run(async () => {
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
  await run(async () => {
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
  await run(async () => {
    await service.recoverAll()
    await silentRefresh()
  }, '已恢复全平台全部异常模型')
}
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="模型状态"
      description="手动开关与自动健康状态分别管理；自动状态不会重新打开手动关闭的对象。"
      ><template #actions
        ><Button variant="outline" :disabled="pending" @click="silentRefresh"
          ><RiRefreshLine size="16" />刷新</Button
        ><Button :disabled="pending" @click="check"
          ><RiStethoscopeLine size="16" />健康检查</Button
        ><Button variant="outline" :disabled="pending" @click="recoverAllChannelsGlobal"
          ><RiRefreshLine size="16" />全平台恢复渠道</Button
        ><Button variant="outline" :disabled="pending" @click="recoverAllModelsGlobal"
          ><RiRestartLine size="16" />全平台恢复全部异常</Button
        ></template
      ></PageHeader
    ><LoadingBlock v-if="loading"     /><ModelStatusList
      v-else
      :items="data || []"
      :pending="pending"
      @channel-toggle="channelToggle"
      @model-toggle="modelToggle"
      @recover-channel="recoverChannel"
      @recover-model="recoverModel"
      @recover-all-models="recoverAllModels"
    />
  </div>
</template>
