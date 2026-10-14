<script setup lang="ts">
// 「模型状态」页：两层结构。
//
// 状态一（平台总览）：一个平台一张 tag，tag 上直接给出「几个 Key、几个模型可用、
//   哪些模型可用」，点 tag 进入状态二。
// 状态二（平台 · Key 详情）：左栏平台下拉 + Key 列表，右栏是选中 Key 的模型明细。
//
// 数据口径统一走 lib/modelStatus 的纯函数（groupByBaseURL / summarizePlatform / filterPlatforms），
// 页面只负责「拿数据、切状态、把事件转发给 service」，不再在模板里各算一遍数字。
import { computed, ref, watch } from 'vue'
import {
  RiLoader4Line,
  RiRefreshLine,
  RiRestartLine,
  RiStethoscopeLine,
  RiArrowLeftSLine,
  RiListCheck,
  RiGridLine,
  RiArrowDownSLine,
  RiFlaskLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useModelStatus } from '@/composables/useModelStatus'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useKeyBatchTest, CONCURRENCY_OPTIONS } from '@/composables/useKeyBatchTest'
import { useConfirm } from '@/composables/useConfirm'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import {
  filterPlatforms,
  availableModelCount,
  filterKeysByGroup,
  keySwitchState,
  type KeyGroup,
} from '@/lib/modelStatus'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import ModelHealthBadge from '@/components/model-status/ModelHealthBadge.vue'
import PlatformTagGrid from '@/components/model-status/PlatformTagGrid.vue'
import ModelStatusKeyList from '@/components/model-status/ModelStatusKeyList.vue'
import ModelStatusModelTable from '@/components/model-status/ModelStatusModelTable.vue'

const service = useModelStatus()
const { data: rawData, loading } = useListLoader(service.list)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()

// 批量测试：把一个平台下的 Key 挨个探一遍，进度回填到左栏列表。
const batchTest = useKeyBatchTest()

// 操作 key：模型状态页所有按钮级 loading 的唯一来源。
// 纯函数层与子组件内部按钮用同一套规则生成 key。
function msKey(channelId: string, action: string) {
  return `ms:${channelId}:${action}`
}

/** 展示层状态：平台总览 / 平台内 Key 详情。 */
const state = ref<'platforms' | 'keys'>('platforms')
/** 模型明细的排布：表格 / 标签。 */
const mode = ref<'table' | 'tags'>('table')

const keyword = ref('')
const health = ref<'all' | 'issue' | 'ok'>('all')
/** 已提交的筛选条件：输入框改动不实时过滤，点「筛选」才生效（与改造前行为一致）。 */
const applied = ref<{ keyword?: string; health: 'all' | 'issue' | 'ok' }>({ health: 'all' })

const selectedBaseUrl = ref('')
const selectedKeyId = ref('')

/**
 * 左栏状态筛选标签的当前值。
 *
 * 放在页面而不是左栏组件里：批量测试要按「列表里实际显示的这些 Key」来跑，
 * 两处必须读到同一个值。
 */
const groupFilter = ref<'all' | KeyGroup>('all')

const platforms = computed(() => filterPlatforms(rawData.value || [], applied.value))

/** 当前平台：优先按选中 base_url 找，找不到（如筛选后消失）回落到第一个。 */
const activePlatform = computed(
  () => platforms.value.find((p) => p.baseUrl === selectedBaseUrl.value) || platforms.value[0],
)

/**
 * 左栏当前显示的 Key：可用 → 手动关闭 → 异常 → 不可用，再按选中的标签过滤。
 *
 * 批量测试与列表渲染共用这一份结果：各算一遍的话，界面上写「测试 7 项」却跑了
 * 17 个，这种事迟早发生。
 */
const visibleKeys = computed(() =>
  filterKeysByGroup(activePlatform.value?.keys || [], groupFilter.value),
)

/**
 * 当前 Key：在当前显示的这份列表里找选中项，找不到（刚切了标签）就用第一条。
 *
 * 必须按 visibleKeys 而不是平台全部 Key 来找：否则切到「不可用」后，右栏还在
 * 显示上一份列表里选中的那个可用 Key，而左边列表里根本没有它 —— 看着像是选错了。
 */
const activeKey = computed(() => {
  const keys = visibleKeys.value
  return keys.find((k) => k.channel.id === selectedKeyId.value) || keys[0]
})

// 平台筛选后原来选中的平台/Key 可能消失：这里把选中值同步回落，避免右栏空转。
watch(
  activePlatform,
  (p) => {
    if (p && p.baseUrl !== selectedBaseUrl.value) {
      selectedBaseUrl.value = p.baseUrl
      selectedKeyId.value = p.keys[0]?.channel.id || ''
    }
  },
  { immediate: true },
)
watch(activeKey, (k) => {
  if (k && k.channel.id !== selectedKeyId.value) selectedKeyId.value = k.channel.id
})

// 切换状态标签等于换了一份列表：选中项落到新列表的第一条，右栏跟着一起切。
watch(groupFilter, () => {
  selectedKeyId.value = visibleKeys.value[0]?.channel.id || ''
})

function applyFilters() {
  applied.value = { keyword: keyword.value, health: health.value }
}
function resetFilters() {
  keyword.value = ''
  health.value = 'all'
  applied.value = { health: 'all' }
}

function openPlatform(baseUrl: string) {
  selectedBaseUrl.value = baseUrl
  selectedKeyId.value = ''
  state.value = 'keys'
}
function selectPlatform(baseUrl: string) {
  selectedBaseUrl.value = baseUrl
  selectedKeyId.value = ''
}
function selectKey(key: ChannelStatus) {
  selectedKeyId.value = key.channel.id
}
function backToPlatforms() {
  state.value = 'platforms'
}

async function fetchFresh(): Promise<ChannelStatus[] | null> {
  try {
    return await service.list()
  } catch (error) {
    toast.error(error instanceof Error ? error.message : '刷新失败')
    return null
  }
}

// 无损刷新：静默拉取最新数据后只替换目标渠道，不触发 loading、不重置当前选中的平台/Key。
async function patchChannel(id: string) {
  const fresh = await fetchFresh()
  if (!fresh || !rawData.value) return
  const next = fresh.find((c) => c.channel.id === id)
  if (!next) return
  const idx = rawData.value.findIndex((c) => c.channel.id === id)
  if (idx !== -1) rawData.value[idx] = next
}

// 静默全量刷新：不置 loading，避免整页闪烁（用于健康检查、手动刷新）。
async function silentRefresh() {
  await run('refresh', async () => {
    const fresh = await fetchFresh()
    if (fresh) rawData.value = fresh
  })
}

async function channelToggle(item: ChannelStatus, enabled: boolean) {
  await run(
    msKey(item.channel.id, 'toggle'),
    async () => {
      await service.setChannel(item.channel.id, enabled)
      await patchChannel(item.channel.id)
    },
    '渠道开关已更新',
  )
}
async function modelToggle(item: ChannelStatus, model: ModelStatus, enabled: boolean) {
  await run(
    msKey(item.channel.id, `model-toggle:${model.model}`),
    async () => {
      await service.setModel(item.channel.id, model.model, enabled)
      await patchChannel(item.channel.id)
    },
    '模型开关已更新',
  )
}
async function deleteModel(item: ChannelStatus, model: ModelStatus) {
  const confirmed = await confirmDialog({
    title: `删除模型「${model.model}」？`,
    description: '将同时清除该模型的健康状态记录（失败计数、手动开关等）。此操作不可恢复。',
    confirmText: '删除',
  })
  if (!confirmed) return
  await run(
    msKey(item.channel.id, `model-delete:${model.model}`),
    async () => {
      await service.deleteModel(item.channel.id, model.model)
      await patchChannel(item.channel.id)
    },
    '模型已删除',
  )
}
async function batchModelToggle(item: ChannelStatus, models: ModelStatus[], enabled: boolean) {
  const action = enabled ? '开启' : '关闭'
  const confirmed = await confirmDialog({
    title: `批量${action} ${models.length} 个模型？`,
    description: `将${action}「${item.channel.name}」下 ${models.length} 个模型的手动开关。`,
    confirmText: action,
  })
  if (!confirmed) return
  await run(
    msKey(item.channel.id, `batch-toggle:${enabled ? 'on' : 'off'}`),
    async () => {
      await service.setModels(
        item.channel.id,
        models.map((m) => m.model),
        enabled,
      )
      await patchChannel(item.channel.id)
    },
    `已${action} ${models.length} 个模型`,
  )
}
async function batchRecoverModel(item: ChannelStatus, models: ModelStatus[]) {
  await run(
    msKey(item.channel.id, 'batch-recover'),
    async () => {
      await service.recoverModels(
        item.channel.id,
        models.map((m) => m.model),
      )
      await patchChannel(item.channel.id)
    },
    `已恢复 ${models.length} 个模型`,
  )
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
  await run(
    msKey(item.channel.id, 'batch-delete'),
    async () => {
      await service.deleteModels(
        item.channel.id,
        manualOnly.map((m) => m.model),
      )
      await patchChannel(item.channel.id)
    },
    `已删除 ${manualOnly.length} 个手动模型`,
  )
}
async function recoverChannel(item: ChannelStatus) {
  await run(
    msKey(item.channel.id, 'recover'),
    async () => {
      await service.recoverChannel(item.channel.id)
      await patchChannel(item.channel.id)
    },
    'Key 已恢复',
  )
}
async function recoverModel(item: ChannelStatus, model: ModelStatus) {
  await run(
    msKey(item.channel.id, `model-recover:${model.model}`),
    async () => {
      await service.recoverModel(item.channel.id, model.model)
      await patchChannel(item.channel.id)
    },
    '模型已恢复',
  )
}
async function check() {
  await run(
    'check',
    async () => {
      await service.check()
      await silentRefresh()
    },
    '健康检查已启动',
  )
}
// 平台级恢复（「恢复本平台」）：按 base_url 一键清掉该平台下全部 Key 的自动熔断，
// 不强制打开手动关闭的开关。
async function recoverPlatform() {
  const platform = activePlatform.value
  if (!platform) return
  const affected = platform.keys.filter((k) => !k.effective_available).length
  if (affected === 0) {
    toast.info('当前平台没有需要恢复的异常 Key')
    return
  }
  const confirmed = await confirmDialog({
    title: `恢复「${platform.name}」全部异常 Key？`,
    description: `将清空该平台 ${affected} 个异常 Key 的自动熔断（Key 级 + 模型级），恢复其自动状态。此操作不会改动手动开关。`,
    confirmText: '恢复本平台',
  })
  if (!confirmed) return
  await run(
    'recover-platform',
    async () => {
      await service.recoverPlatform(platform.baseUrl)
      await silentRefresh()
    },
    `已恢复「${platform.name}」全部异常 Key`,
  )
}

// 平台级「强制开启本平台全部模型」（破坏性）：清自动熔断并强制打开手动开关。
async function recoverPlatformForced() {
  const platform = activePlatform.value
  if (!platform) return
  const confirmed = await confirmDialog({
    title: `强制开启「${platform.name}」全部模型？`,
    description:
      '将清空该平台下所有 Key 的自动熔断，并把所有模型的手动开关强制打开。此操作会覆盖你主动关闭的开关。',
    confirmText: '强制开启',
  })
  if (!confirmed) return
  await run(
    'recover-platform-forced',
    async () => {
      await service.recoverPlatformForced(platform.baseUrl)
      await silentRefresh()
    },
    `已强制开启「${platform.name}」全部模型`,
  )
}

// 全平台操作：「恢复全部平台」——清所有 Key 的自动熔断（Key 级 + 模型级），不碰手动开关。
async function recoverAllChannelsGlobal() {
  const affected = (rawData.value || []).filter((ch) => !ch.effective_available).length
  if (affected === 0) {
    toast.info('没有需要恢复的异常平台')
    return
  }
  const confirmed = await confirmDialog({
    title: '恢复全部平台？',
    description: `将清空 ${affected} 个异常 Key 的自动熔断（Key 级 + 模型级）与失败计数，恢复其自动状态。此操作不会改动任何手动开关。`,
    confirmText: '恢复全部平台',
  })
  if (!confirmed) return
  await run(
    'recover-all-channels',
    async () => {
      await service.recoverAllChannels()
      await silentRefresh()
    },
    '已恢复全部平台',
  )
}

// 全平台操作：「强制开启全部模型」（破坏性）——清所有熔断 + 强制打开所有手动开关。
async function recoverAllModelsGlobal() {
  const summary = (rawData.value || []).reduce(
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
    title: '强制开启全平台全部模型？',
    description: `将把全平台 ${summary.disabled} 个异常模型的手动开关强制打开，并清空所有自动失败计数。此操作会覆盖你主动关闭的开关。`,
    confirmText: '强制开启',
  })
  if (!confirmed) return
  await run(
    'recover-all-models',
    async () => {
      await service.recoverAll()
      await silentRefresh()
    },
    '已强制开启全平台全部模型',
  )
}

/** 平台总览的合计文案，随筛选变化。 */
const totals = computed(() => {
  const list = platforms.value
  return {
    platforms: list.length,
    models: list.reduce((n, p) => n + p.availableModelCount, 0),
  }
})

/** 点左半边「测试」：按当前并发数开跑。 */
async function runBatchTest() {
  if (batchTest.running.value) {
    batchTest.stop()
    toast.info('已停止，正在跑的请求会自然结束')
    return
  }
  // 批量测试的范围就是左栏当前显示的这些 Key：界面上写着「不可用 7」，
  // 点测试就该只测这 7 个。复用 visibleKeys（而不是另算一份），两份结果
  // 不可能对不上。
  const targets = visibleKeys.value
  if (!targets.length) {
    toast.info('当前筛选下没有可测试的 Key')
    return
  }
  await batchTest.testKeys(targets)
  // 只统计这一批的结果：别的平台或上一轮留下的失败标记不该算进来。
  const tested = new Set(targets.map((key) => key.channel.id))
  const remaining = Object.entries(batchTest.states.value).filter(
    ([id, state]) => state.phase === 'failed' && tested.has(id),
  ).length
  // 测试走的是自家网关，成功/失败都会真实改动渠道健康状态（熔断、恢复、
  // 失败计数），所以跑完必须拉一次最新数据，否则右侧表格还是测试前的旧状态。
  await silentRefresh()
  if (remaining > 0) toast.error(`测试完成，${remaining} 个 Key 未通过`)
  else toast.success('测试完成，全部 Key 通过')
}

/** 右栏表格里某一行的「测试」：只测当前 Key 下的这个模型。 */
async function testModel(item: ChannelStatus, model: ModelStatus) {
  const result = await batchTest.testOne(item, model.model)
  // 同上：单测也会改健康状态，立刻刷一遍让表格跟上。
  await silentRefresh()
  if (result.ok) toast.success(`「${model.model}」测试通过`)
  else toast.error(`「${model.model}」测试失败：${result.error}`)
}
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col gap-6">
    <PageHeader
      title="模型状态"
      description="手动开关与自动健康状态分别管理；自动状态不会重新打开手动关闭的对象。"
    >
      <template #actions>
        <Button variant="outline" :disabled="isPending('refresh')" @click="silentRefresh">
          <RiLoader4Line v-if="isPending('refresh')" class="animate-spin" size="16" />
          <RiRefreshLine v-else size="16" />刷新
        </Button>
        <Button :disabled="isPending('check')" @click="check">
          <RiLoader4Line v-if="isPending('check')" class="animate-spin" size="16" />
          <RiStethoscopeLine v-else size="16" />健康检查
        </Button>
        <Button
          variant="outline"
          :disabled="isPending('recover-all-channels')"
          @click="recoverAllChannelsGlobal"
        >
          <RiLoader4Line v-if="isPending('recover-all-channels')" class="animate-spin" size="16" />
          <RiRefreshLine v-else size="16" />恢复全部平台
        </Button>
        <!-- 破坏性操作（会覆盖手动关闭的开关），保留强化确认框。 -->
        <Button
          variant="outline"
          :disabled="isPending('recover-all-models')"
          @click="recoverAllModelsGlobal"
        >
          <RiLoader4Line v-if="isPending('recover-all-models')" class="animate-spin" size="16" />
          <RiRestartLine v-else size="16" />强制开启全部模型
        </Button>
      </template>
    </PageHeader>

    <!-- 筛选：搜索与状态对两层都生效；输入框改动需点「筛选」才应用。 -->
    <form class="flex flex-wrap items-end gap-3" @submit.prevent="applyFilters">
      <div class="min-w-72 flex-1 space-y-1">
        <Label for="ms-keyword">搜索（平台 / Key / 模型，支持 * 通配符）</Label>
        <Input id="ms-keyword" v-model="keyword" placeholder="如 workbuddy、deepseek*、acct-1" />
      </div>
      <div class="w-36 space-y-1">
        <Label for="ms-health">状态</Label>
        <Select v-model="health">
          <SelectTrigger id="ms-health" class="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
            <SelectGroup>
              <SelectItem value="all">全部状态</SelectItem>
              <SelectItem value="issue">存在异常</SelectItem>
              <SelectItem value="ok">全部可用</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
      </div>
      <div class="flex items-center gap-2">
        <Button type="submit">筛选</Button>
        <Button type="button" variant="outline" @click="resetFilters">重置</Button>
      </div>
      <div class="ml-auto text-xs text-muted-foreground tabular-nums">
        {{ totals.platforms }} 个平台 · 可用模型合计 {{ totals.models }}
      </div>
    </form>

    <LoadingBlock v-if="loading" />

    <template v-else>
      <!-- ============ 状态一：平台总览 ============ -->
      <template v-if="state === 'platforms'">
        <PlatformTagGrid :platforms="platforms" @select="openPlatform" />
        <EmptyState
          v-if="!platforms.length"
          title="没有匹配的平台"
          description="换个搜索词，或把状态筛选改回「全部状态」。"
        />
      </template>

      <!-- ============ 状态二：平台 · Key 详情 ============ -->
      <div v-else class="flex min-h-0 flex-1 flex-col gap-3">
        <div class="flex items-center gap-2 text-sm text-muted-foreground">
          <Button
            variant="ghost"
            size="sm"
            class="gap-0.5 pl-1 text-muted-foreground"
            @click="backToPlatforms"
          >
            <RiArrowLeftSLine size="16" />平台总览
          </Button>
          <span class="text-border">/</span>
          <span class="font-medium text-foreground">{{ activePlatform?.name }}</span>
          <!-- 平台级恢复操作区：放在面包屑右侧，一次作用于本平台下所有 Key。 -->
          <!-- text-foreground：面包屑这行是 muted 文字色，按钮不继承它，
               否则默认态会显示灰色、只有 hover 才变正常色。 -->
          <div class="ml-3 flex items-center gap-2 text-foreground">
            <Button
              variant="outline"
              size="sm"
              class="gap-1"
              :disabled="isPending('recover-platform')"
              @click="recoverPlatform"
            >
              <RiLoader4Line v-if="isPending('recover-platform')" class="animate-spin" size="14" />
              <RiRefreshLine v-else size="14" />恢复本平台
            </Button>
            <Button
              variant="outline"
              size="sm"
              class="gap-1"
              :disabled="isPending('recover-platform-forced')"
              @click="recoverPlatformForced"
            >
              <RiLoader4Line
                v-if="isPending('recover-platform-forced')"
                class="animate-spin"
                size="14"
              />
              <RiRestartLine v-else size="14" />强制开启本平台
            </Button>
          </div>
          <!--
            右侧操作区：批量测试 + 视图切换。
            批量测试是一个整体按钮，内部分成「测试」与「并发数」两半，中间一条
            分隔线；两半各自 hover、各自可点，上下留出 padding 让它有实体感。
          -->
          <div class="ml-auto flex items-center gap-2">
            <!--
              一个整体按钮：外层出圆角与描边，内部用 1px 分隔线把「测试」与
              「并发数」分成两个可点区域。不能用两个 Button 拼——Button 各自带
              圆角，会渲染成两个分开的小药丸。
              hover 取 hover:bg-muted：与左侧「强制开启本平台」Button 的悬停色
              同一个类。带透明度的 hover:bg-muted/60 在本项目里不会被样式层生成
              （实测该规则不存在），写了也是白写。
            -->
            <div
              class="inline-flex h-7 items-stretch overflow-hidden rounded-md border border-border"
            >
              <!--
                左半「测试 N 项」：N 跟着左栏筛选标签走，点「不可用」就是 7 项，
                点「全部」就是 17 项 —— 点了会跑多少，按之前先写清楚。
              -->
              <button
                type="button"
                class="flex h-full items-center gap-1.5 px-2.5 text-[0.8rem] font-medium text-foreground transition-colors hover:bg-muted disabled:cursor-not-allowed disabled:opacity-60"
                :title="`按并发 ${batchTest.concurrency.value} 测试本平台全部 Key`"
                @click="runBatchTest"
              >
                <RiLoader4Line v-if="batchTest.running.value" class="animate-spin" size="14" />
                <RiFlaskLine v-else size="14" />
                <span class="tabular-nums">
                  <template v-if="batchTest.running.value">
                    测试中 {{ batchTest.progress.value }}
                  </template>
                  <!--
                    空闲态直接报出这一批有多少项：数量跟着左栏的筛选标签走，
                    用户点「不可用」时看到的就该是 7 项，而不是整个平台的 17。
                  -->
                  <template v-else-if="visibleKeys.length">
                    测试 {{ visibleKeys.length }} 项
                  </template>
                  <template v-else>测试</template>
                </span>
              </button>
              <span class="w-px shrink-0 bg-border" aria-hidden="true" />
              <DropdownMenu>
                <DropdownMenuTrigger as-child>
                  <button
                    type="button"
                    class="flex h-full items-center gap-1 px-2 text-[0.8rem] text-muted-foreground transition-colors hover:bg-muted hover:text-foreground disabled:cursor-not-allowed disabled:opacity-60"
                    aria-label="选择并发数"
                    :disabled="batchTest.running.value"
                    :title="`测试并发数：当前 ${batchTest.concurrency.value}`"
                  >
                    <span class="tabular-nums text-[11px]">{{ batchTest.concurrency.value }}</span>
                    <RiArrowDownSLine size="14" />
                  </button>
                </DropdownMenuTrigger>
                <DropdownMenuContent align="end" :side-offset="4" class="min-w-44">
                  <DropdownMenuLabel class="text-xs font-normal text-muted-foreground">
                    测试并发数（当前 {{ batchTest.concurrency.value }}）
                  </DropdownMenuLabel>
                  <DropdownMenuSeparator />
                  <DropdownMenuRadioGroup v-model="batchTest.concurrency.value">
                    <DropdownMenuRadioItem
                      v-for="option in CONCURRENCY_OPTIONS"
                      :key="option"
                      :value="option"
                      :class="
                        option === batchTest.concurrency.value
                          ? 'bg-accent font-semibold text-accent-foreground'
                          : ''
                      "
                    >
                      {{ option }} 个并发
                    </DropdownMenuRadioItem>
                  </DropdownMenuRadioGroup>
                </DropdownMenuContent>
              </DropdownMenu>
            </div>
            <div class="mx-2 inline-flex h-7 overflow-hidden rounded-md border border-border">
              <button
                type="button"
                class="h-full px-2 transition-colors"
                :class="
                  mode === 'table'
                    ? 'bg-muted font-medium text-foreground'
                    : 'text-muted-foreground hover:bg-muted'
                "
                aria-label="表格模式"
                @click="mode = 'table'"
              >
                <RiListCheck size="14" />
              </button>
              <button
                type="button"
                class="h-full border-l border-border px-2 transition-colors"
                :class="
                  mode === 'tags'
                    ? 'bg-muted font-medium text-foreground'
                    : 'text-muted-foreground hover:bg-muted'
                "
                aria-label="标签模式"
                @click="mode = 'tags'"
              >
                <RiGridLine size="14" />
              </button>
            </div>
          </div>
        </div>

        <div
          class="flex min-h-[32rem] shrink-0 flex-col overflow-hidden rounded-lg border border-border md:flex-1! md:flex-row!"
        >
          <ModelStatusKeyList
            :platforms="platforms"
            :active-base-url="activePlatform?.baseUrl || ''"
            :active-key-id="activeKey?.channel.id"
            :is-pending="isPending"
            :test-states="batchTest.states.value"
            v-model:group-filter="groupFilter"
            @select-platform="selectPlatform"
            @select-key="selectKey"
          />

          <div class="flex min-w-0 flex-1 flex-col">
            <template v-if="activeKey">
              <!-- 右栏头部：当前 Key 的开关、状态徽标与地址。 -->
              <div class="flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5">
                <div class="flex items-center gap-2">
                  <!--
                    开关只表达「这个 Key 现在能不能用」：可用=on（可拨关），
                    手动关闭=off（可拨开，后端顺带清自动熔断），
                    自动熔断=off 且置灰（正确出口是下面的「恢复 Key」按钮）。
                  -->
                  <Switch
                    :id="`channel-${activeKey.channel.id}`"
                    :model-value="keySwitchState(activeKey).value"
                    :disabled="
                      keySwitchState(activeKey).disabled ||
                      isPending(msKey(activeKey.channel.id, 'toggle'))
                    "
                    @update:model-value="channelToggle(activeKey, Boolean($event))"
                  />
                  <Label :for="`channel-${activeKey.channel.id}`" class="text-sm font-semibold">
                    {{ activeKey.channel.name }}
                  </Label>
                </div>
                <ModelHealthBadge
                  :status="activeKey.health_status"
                  :available="activeKey.effective_available"
                  :manual-enabled="activeKey.manual_enabled"
                  :failure-class="activeKey.failure_class"
                  :rule-id="activeKey.last_rule_id"
                  :rule-name="activeKey.last_rule_name"
                  :last-error="activeKey.reason"
                />
                <!-- 自动熔断的 Key：在头部给一个明确的「恢复 Key」按钮，
                     不用去猜工具栏里哪个按钮管这件事。 -->
                <Button
                  v-if="keySwitchState(activeKey).disabled"
                  variant="outline"
                  size="sm"
                  class="gap-1"
                  :disabled="isPending(msKey(activeKey.channel.id, 'recover'))"
                  @click="recoverChannel(activeKey)"
                >
                  <RiLoader4Line
                    v-if="isPending(msKey(activeKey.channel.id, 'recover'))"
                    class="animate-spin"
                    size="14"
                  />
                  <RiRefreshLine v-else size="14" />恢复 Key
                </Button>
                <span class="text-xs tabular-nums text-muted-foreground">
                  {{ availableModelCount(activeKey) }} / {{ activeKey.models.length }} 模型可用
                </span>
                <span
                  class="ml-auto hidden min-w-0 truncate font-mono text-[11px] text-muted-foreground lg:block"
                  :title="activeKey.channel.base_url"
                >
                  {{ activeKey.channel.base_url }}
                </span>
              </div>
              <ModelStatusModelTable
                :key="activeKey.channel.id"
                :item="activeKey"
                :mode="mode"
                :is-pending="isPending"
                :is-testing="(model) => batchTest.isTesting(activeKey.channel.id, model)"
                @model-toggle="(m, enabled) => modelToggle(activeKey, m, enabled)"
                @recover-model="(m) => recoverModel(activeKey, m)"
                @test-model="(m) => testModel(activeKey, m)"
                @batch-model-toggle="
                  (models, enabled) => batchModelToggle(activeKey, models, enabled)
                "
                @batch-recover-model="(models) => batchRecoverModel(activeKey, models)"
                @batch-delete-model="(models) => batchDeleteModel(activeKey, models)"
                @delete-model="(m) => deleteModel(activeKey, m)"
              />
            </template>
            <EmptyState
              v-else
              title="该平台没有 Key"
              description="这个平台下还没有任何 Key，先在「渠道与模型」里添加。"
            />
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
