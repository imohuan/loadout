<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiLoader4Line,
  RiRefreshLine,
  RiRestartLine,
  RiDeleteBinLine,
  RiToggleLine,
  RiCheckboxMultipleLine,
  RiCloseLine,
  RiCloseCircleLine,
} from '@remixicon/vue'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import StatusBadge from '@/components/StatusBadge.vue'
import { formatDate } from '@/lib/format'

const props = defineProps<{
  item: ChannelStatus
  mode: 'table' | 'tags'
  isPending?: (key: string) => boolean
  /** 顶层「全部折叠/展开」联动控制：null 表示不强制（使用默认/用户手动态）。 */
  expandAll?: 'expanded' | 'collapsed' | null
}>()

const emit = defineEmits<{
  channelToggle: [enabled: boolean]
  modelToggle: [model: ModelStatus, enabled: boolean]
  recoverChannel: []
  recoverModel: [model: ModelStatus]
  recoverAllModels: []
  batchModelToggle: [models: ModelStatus[], enabled: boolean]
  batchRecoverModel: [models: ModelStatus[]]
  batchDeleteModel: [models: ModelStatus[]]
  deleteModel: [model: ModelStatus]
}>()

// 默认折叠：仅显示 Key 头部，点开后查看模型明细（配合组默认折叠，列表首屏只显示汇总信息）。
const expanded = ref(false)

// 顶层联动：与 ChannelGroup 同步处理，仅在 expandAll 给出明确指令时覆盖本地状态。
// `immediate` 必须开：Channel 在 ChannelGroup 的 v-if 之后挂载，挂载瞬间 prop 已是目标值，
// 懒 watch 不会触发，会导致「一级展开、二级不展开」。
watch(
  () => props.expandAll,
  (v) => {
    if (v === 'expanded') expanded.value = true
    else if (v === 'collapsed') expanded.value = false
  },
  { immediate: true },
)
const selected = ref(new Set<string>())

// 可用模型数：在 model.effective_available 维度上计数（独立于渠道 Key 自身开关）。
// 渠道 Key 已关闭时为 0；若 Key 可用，则反映当前"手动开 + 健康可用"的模型数。
const availableModels = computed(
  () => props.item.models.filter((m) => m.effective_available).length,
)
const selectedModels = computed(() => props.item.models.filter((m) => selected.value.has(m.model)))
const hasSelection = computed(() => selected.value.size > 0)
const allSelected = computed(
  () => props.item.models.length > 0 && selected.value.size === props.item.models.length,
)

function toggleExpand() {
  expanded.value = !expanded.value
}
function toggleSelect(model: string) {
  const next = new Set(selected.value)
  next.has(model) ? next.delete(model) : next.add(model)
  selected.value = next
}
function selectAll() {
  selected.value = new Set(props.item.models.map((m) => m.model))
}
function invertSelection() {
  const next = new Set<string>()
  for (const m of props.item.models) {
    if (!selected.value.has(m.model)) next.add(m.model)
  }
  selected.value = next
}
function clearSelection() {
  selected.value = new Set()
}
function toggleAllSelect() {
  allSelected.value ? clearSelection() : selectAll()
}

// 操作 key：与 ModelStatusView.run() 的 key 规则完全一致（ms:{channelId}:{action}）。
// 按钮级 loading/disabled 依赖 key 精确匹配，不能改格式。
function busy(action: string) {
  return props.isPending ? props.isPending(`ms:${props.item.channel.id}:${action}`) : false
}
</script>

<template>
  <TooltipProvider>
    <Card class="rounded-md py-0">
      <CardContent class="p-0">
        <button
          class="flex w-full items-center gap-3 px-4 py-2.5 text-left hover:bg-muted/50"
          type="button"
          :aria-expanded="expanded"
          :aria-label="`${expanded ? '收起' : '展开'} ${item.channel.name} 的模型状态`"
          @click="toggleExpand"
        >
          <component
            :is="expanded ? RiArrowDownSLine : RiArrowRightSLine"
            size="18"
            class="shrink-0 text-muted-foreground"
          />
          <div class="min-w-0 flex-1">
            <div class="flex items-baseline gap-3">
              <p class="truncate font-medium text-foreground">{{ item.channel.name }}</p>
              <p class="shrink-0 truncate font-mono text-xs text-muted-foreground">
                {{ item.channel.base_url }}
              </p>
            </div>
          </div>
          <StatusBadge
            :status="item.health_status"
            :available="item.effective_available"
            hide-when-available
          />
          <!--
          进度：可用/总数，让用户一眼看出渠道里还有多少模型处于可用状态，
          与渠道 Key 自身「已关闭」徽章互补（一个是 Key 维度，一个是模型维度）。
        -->
          <span class="hidden tabular-nums text-sm text-muted-foreground md:block"
            >{{ availableModels }} / {{ item.models.length }} 个模型</span
          >
        </button>
        <div
          v-if="expanded"
          class="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2 text-sm"
        >
          <div class="flex flex-1 flex-wrap items-center gap-2">
            <template v-if="!hasSelection">
              <div class="flex items-center gap-2">
                <Switch
                  :id="`channel-${item.channel.id}`"
                  :model-value="item.manual_enabled"
                  :disabled="busy('toggle')"
                  @update:model-value="emit('channelToggle', Boolean($event))"
                />
                <Label :for="`channel-${item.channel.id}`">手动启用</Label>
              </div>
              <span v-if="item.reason" class="min-w-48 flex-1 text-muted-foreground">{{
                item.reason
              }}</span>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('recover')"
                @click="emit('recoverChannel')"
              >
                <RiLoader4Line
                  v-if="busy('recover')"
                  class="animate-spin"
                  size="16"
                /><RiRefreshLine v-else size="16" />恢复渠道
              </Button>
              <Tooltip>
                <TooltipTrigger as-child>
                  <Button
                    variant="outline"
                    size="sm"
                    :disabled="busy('recover-all')"
                    @click="emit('recoverAllModels')"
                  >
                    <RiLoader4Line
                      v-if="busy('recover-all')"
                      class="animate-spin"
                      size="16"
                    /><RiRestartLine v-else size="16" />恢复全部异常
                  </Button>
                </TooltipTrigger>
                <TooltipContent>
                  清空当前渠道的自动熔断 + 强制打开该渠道所有手动开关；范围仅限当前渠道
                </TooltipContent>
              </Tooltip>
              <div class="mx-1 h-5 w-px bg-border" />
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-toggle:off')"
                @click="emit('batchModelToggle', item.models, false)"
              >
                <RiLoader4Line
                  v-if="busy('batch-toggle:off')"
                  class="animate-spin"
                  size="16"
                />关闭全部
              </Button>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-toggle:on')"
                @click="emit('batchModelToggle', item.models, true)"
              >
                <RiLoader4Line
                  v-if="busy('batch-toggle:on')"
                  class="animate-spin"
                  size="16"
                />开启全部
              </Button>
            </template>
            <template v-else>
              <span class="text-sm text-muted-foreground">已选 {{ selected.size }} 个</span>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-delete')"
                @click="emit('batchDeleteModel', selectedModels)"
              >
                <RiLoader4Line
                  v-if="busy('batch-delete')"
                  class="animate-spin"
                  size="14"
                /><RiDeleteBinLine v-else size="14" />删除
              </Button>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-toggle:on')"
                @click="emit('batchModelToggle', selectedModels, true)"
              >
                <RiLoader4Line
                  v-if="busy('batch-toggle:on')"
                  class="animate-spin"
                  size="14"
                /><RiToggleLine v-else size="14" />开启
              </Button>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-toggle:off')"
                @click="emit('batchModelToggle', selectedModels, false)"
              >
                <RiLoader4Line v-if="busy('batch-toggle:off')" class="animate-spin" size="14" />关闭
              </Button>
              <Button
                variant="outline"
                size="sm"
                :disabled="busy('batch-recover')"
                @click="emit('batchRecoverModel', selectedModels)"
              >
                <RiLoader4Line
                  v-if="busy('batch-recover')"
                  class="animate-spin"
                  size="14"
                /><RiRefreshLine v-else size="14" />恢复
              </Button>
            </template>
          </div>
          <div class="flex shrink-0 items-center gap-0.5">
            <Tooltip>
              <TooltipTrigger as-child>
                <Button size="sm" variant="ghost" @click="selectAll">
                  <RiCheckboxMultipleLine size="16" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>全选</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger as-child>
                <Button size="sm" variant="ghost" @click="invertSelection">
                  <RiCloseCircleLine size="16" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>反选</TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger as-child>
                <Button size="sm" variant="ghost" @click="clearSelection">
                  <RiCloseLine size="16" />
                </Button>
              </TooltipTrigger>
              <TooltipContent>清空选择</TooltipContent>
            </Tooltip>
          </div>
        </div>
        <div v-if="expanded" class="border-t border-border">
          <div v-if="mode === 'table'" class="overflow-x-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="w-10">
                    <Checkbox
                      :model-value="hasSelection && !allSelected ? 'indeterminate' : allSelected"
                      @update:model-value="toggleAllSelect"
                    />
                  </TableHead>
                  <TableHead>模型</TableHead>
                  <TableHead>手动开关</TableHead>
                  <TableHead>自动状态</TableHead>
                  <TableHead>最后错误</TableHead>
                  <TableHead>失败</TableHead>
                  <TableHead>最近成功</TableHead>
                  <TableHead class="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow
                  v-for="model in item.models"
                  :key="model.model"
                  :class="selected.has(model.model) ? 'bg-muted/50' : ''"
                >
                  <TableCell>
                    <Checkbox
                      :model-value="selected.has(model.model)"
                      @update:model-value="toggleSelect(model.model)"
                    />
                  </TableCell>
                  <TableCell class="font-mono text-xs font-medium select-all">{{
                    model.model
                  }}</TableCell>
                  <TableCell>
                    <Switch
                      :id="`model-${item.channel.id}-${model.model}`"
                      :model-value="model.manual_enabled"
                      :disabled="busy(`model-toggle:${model.model}`)"
                      @update:model-value="emit('modelToggle', model, Boolean($event))"
                    />
                  </TableCell>
                  <TableCell>
                    <StatusBadge
                      :status="model.health_status"
                      :available="model.effective_available"
                      hide-when-available
                    />
                  </TableCell>
                  <TableCell class="max-w-64 text-xs text-muted-foreground">
                    <Tooltip v-if="model.last_error || model.reason">
                      <TooltipTrigger as-child>
                        <span class="block w-full truncate">{{
                          model.last_error || model.reason
                        }}</span>
                      </TooltipTrigger>
                      <TooltipContent class="max-w-md whitespace-normal break-words">{{
                        model.last_error || model.reason
                      }}</TooltipContent>
                    </Tooltip>
                    <span v-else>-</span>
                  </TableCell>
                  <TableCell class="tabular-nums">{{ model.fail_count || 0 }}</TableCell>
                  <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{
                    formatDate(model.last_success_at)
                  }}</TableCell>
                  <TableCell class="flex items-center justify-end gap-1">
                    <Button
                      v-if="model.source === 'manual'"
                      variant="outline"
                      size="sm"
                      :disabled="busy(`model-delete:${model.model}`)"
                      @click="emit('deleteModel', model)"
                    >
                      <RiLoader4Line
                        v-if="busy(`model-delete:${model.model}`)"
                        class="animate-spin"
                        size="14"
                      /><RiDeleteBinLine v-else size="14" />删除
                    </Button>
                    <Button
                      variant="outline"
                      size="sm"
                      :disabled="busy(`model-recover:${model.model}`)"
                      @click="emit('recoverModel', model)"
                    >
                      <RiLoader4Line
                        v-if="busy(`model-recover:${model.model}`)"
                        class="animate-spin"
                        size="14"
                      /><RiRefreshLine v-else size="14" />恢复
                    </Button>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </div>
          <div v-else class="flex flex-wrap gap-1.5 p-3">
            <button
              v-for="model in item.models"
              :key="model.model"
              type="button"
              class="inline-flex items-center gap-1.5 rounded-md border px-2.5 py-1 text-xs font-medium transition-colors"
              :class="
                selected.has(model.model)
                  ? 'border-transparent bg-primary text-primary-foreground'
                  : 'border-border bg-background text-foreground hover:bg-muted'
              "
              :disabled="busy(`model-toggle:${model.model}`)"
              @click="toggleSelect(model.model)"
            >
              <span class="font-mono">{{ model.model }}</span>
              <StatusBadge
                :status="model.health_status"
                :available="model.effective_available"
                hide-when-available
              />
              <Tooltip v-if="model.source === 'manual'">
                <TooltipTrigger as-child>
                  <span
                    class="rounded-full p-0.5 hover:bg-destructive/20 hover:text-destructive"
                    :class="{
                      'pointer-events-none opacity-50': busy(`model-delete:${model.model}`),
                    }"
                    @click.stop="emit('deleteModel', model)"
                  >
                    <RiLoader4Line
                      v-if="busy(`model-delete:${model.model}`)"
                      class="animate-spin"
                      size="12"
                    />
                    <RiCloseLine v-else size="12" />
                  </span>
                </TooltipTrigger>
                <TooltipContent>删除（仅手动添加）</TooltipContent>
              </Tooltip>
            </button>
            <p v-if="!item.models.length" class="py-4 text-sm text-muted-foreground">
              该渠道暂无模型
            </p>
          </div>
        </div>
      </CardContent>
    </Card>
  </TooltipProvider>
</template>
