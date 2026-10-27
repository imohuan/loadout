<script setup lang="ts">
// 状态二右侧：某个 Key（渠道）下的模型明细表。
//
// 从原来的 ModelStatusChannel 里抽出来：那时它同时负责「Key 头部 + 工具栏 + 表格」，
// 现在左栏已经承担了 Key 切换，这里只保留「工具栏 + 模型表格」这一件事。
import { computed, ref } from 'vue'
import {
  RiLoader4Line,
  RiRefreshLine,
  RiDeleteBinLine,
  RiToggleLine,
  RiCloseLine,
  RiFlaskLine,
} from '@remixicon/vue'
import type { ChannelStatus, ModelStatus } from '@/lib/types'
import ModelHealthBadge from '@/components/model-status/ModelHealthBadge.vue'
import BulkSelectButtons from '@/components/BulkSelectButtons.vue'
import { formatShortCN } from '@/lib/format'
import AxTable from '@/components/ui/AxTable.vue'

const props = defineProps<{
  item: ChannelStatus
  /**
   * 当前 Key（账号）是否能参与路由。
   *
   * 模型行自己的 effective_available 只反映 model_states，账号被 401/403 熔断时
   * 它仍可能为 true；两者必须同时成立才是「真的可用」，否则页面会把整组账号
   * 全部停用显示成「9/11 模型可用」。
   */
  keyEffectiveAvailable?: boolean
  mode: 'table' | 'tags'
  isPending?: (key: string) => boolean
  /** 这个「Key + 模型」组合是否正在测试：每行按钮各自的 loading。 */
  isTesting?: (model: string) => boolean
}>()

const emit = defineEmits<{
  modelToggle: [model: ModelStatus, enabled: boolean]
  recoverModel: [model: ModelStatus]
  batchModelToggle: [models: ModelStatus[], enabled: boolean]
  batchRecoverModel: [models: ModelStatus[]]
  batchDeleteModel: [models: ModelStatus[]]
  deleteModel: [model: ModelStatus]
  testModel: [model: ModelStatus]
}>()

const selected = ref(new Set<string>())

// 可用模型数：在 model.effective_available 维度上计数（独立于 Key 自身开关）。
const availableModels = computed(() => {
  if (props.keyEffectiveAvailable === false) return 0
  return props.item.models.filter((m) => m.effective_available).length
})
const modelAvailable = (m: ModelStatus) =>
  props.keyEffectiveAvailable !== false && m.effective_available
/** 账号级熔断时，表格的“最后错误”要指向账号原因；模型自己的旧错误会误导排查。 */
const modelError = (m: ModelStatus) =>
  props.keyEffectiveAvailable === false
    ? props.item.last_error || props.item.reason || m.last_error || m.reason
    : m.last_error || m.reason
/**
 * 故障作用域标签：区分「账号级」和「模型级」。
 *
 * 账号级 = 整个账号停用，它名下所有模型都不能请求。
 * 模型级 = 只停这个模型，同账号其他模型照常可用。
 * 两者混在一起时，用户会误以为「一个模型额度用尽 → 整号不能用」。
 */
const scopeLabel = (m: ModelStatus): { text: string; cls: string } | null => {
  if (props.keyEffectiveAvailable === false) {
    return { text: '账号级', cls: 'bg-red-500/15 text-red-700 dark:text-red-300' }
  }
  if (!m.effective_available) {
    return { text: '模型级', cls: 'bg-amber-500/15 text-amber-700 dark:text-amber-300' }
  }
  return null
}
const selectedModels = computed(() => props.item.models.filter((m) => selected.value.has(m.model)))
const hasSelection = computed(() => selected.value.size > 0)
const allSelected = computed(
  () => props.item.models.length > 0 && selected.value.size === props.item.models.length,
)

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

/**
 * 这一行的测试按钮是否在转。
 *
 * 只认自己这一行的状态：之前用 Key 级状态驱动整列按钮，点一个就整列一起转，
 * 用户根本看不出是哪个在跑。
 */
function testing(model: ModelStatus) {
  return props.isTesting ? props.isTesting(model.model) : false
}

// 切换 Key 时清空勾选：否则会看到「已选 3 个」但表格里一个都没勾。
function resetSelection() {
  selected.value = new Set()
}
defineExpose({ resetSelection })
</script>

<template>
  <div class="flex min-h-0 flex-1 flex-col">
    <!-- 工具栏：渠道级操作 + 批量操作。选中项非空时切换成批量按钮，避免两排按钮打架。 -->
    <div
      class="flex flex-wrap items-center gap-2 border-b border-border bg-muted/30 px-3 py-2 text-sm"
    >
      <div class="flex flex-1 flex-wrap items-center gap-2">
        <template v-if="!hasSelection">
          <span class="text-xs text-muted-foreground tabular-nums">
            {{ availableModels }} / {{ item.models.length }} 个模型可用
          </span>
          <span v-if="item.reason" class="min-w-40 flex-1 truncate text-xs text-muted-foreground">{{
            item.reason
          }}</span>
          <span v-else class="flex-1" />
          <!-- 「恢复 Key」只在右栏头部保留一个（避免同动作重复出现）；
               「强制开启本 Key」是破坏性操作，收进平台级「更多 ▾」菜单。 -->
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-toggle:off')"
            @click="emit('batchModelToggle', item.models, false)"
          >
            <RiLoader4Line v-if="busy('batch-toggle:off')" class="animate-spin" size="14" />
            关闭全部
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-toggle:on')"
            @click="emit('batchModelToggle', item.models, true)"
          >
            <RiLoader4Line v-if="busy('batch-toggle:on')" class="animate-spin" size="14" />
            开启全部
          </Button>
        </template>
        <template v-else>
          <span class="text-xs text-muted-foreground">已选 {{ selected.size }} 个</span>
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-delete')"
            @click="emit('batchDeleteModel', selectedModels)"
          >
            <RiLoader4Line v-if="busy('batch-delete')" class="animate-spin" size="14" />
            <RiDeleteBinLine v-else size="14" />删除
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-toggle:on')"
            @click="emit('batchModelToggle', selectedModels, true)"
          >
            <RiLoader4Line v-if="busy('batch-toggle:on')" class="animate-spin" size="14" />
            <RiToggleLine v-else size="14" />开启
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-toggle:off')"
            @click="emit('batchModelToggle', selectedModels, false)"
          >
            <RiLoader4Line v-if="busy('batch-toggle:off')" class="animate-spin" size="14" />
            关闭
          </Button>
          <Button
            variant="outline"
            size="sm"
            :disabled="busy('batch-recover')"
            @click="emit('batchRecoverModel', selectedModels)"
          >
            <RiLoader4Line v-if="busy('batch-recover')" class="animate-spin" size="14" />
            <RiRefreshLine v-else size="14" />恢复
          </Button>
        </template>
      </div>
      <BulkSelectButtons
        :all-selected="allSelected"
        :can-operate="item.models.length > 0"
        :has-selection="hasSelection"
        @select-all="selectAll"
        @invert="invertSelection"
        @clear="clearSelection"
      />
    </div>

    <div class="min-h-0 flex-1 overflow-auto">
      <div v-if="mode === 'table'">
        <AxTable>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead class="w-10">
                  <Checkbox
                    :model-value="hasSelection && !allSelected ? 'indeterminate' : allSelected"
                    @update:model-value="toggleAllSelect"
                  />
                </TableHead>
                <!-- 右栏宽度有限：每列给一个语义化的最小宽度，被压窄时也不会把
                     「自动状态」「操作」挤成一条缝（AxTable 按 min-w-* 决定压缩下限，
                     触底后交给外层横向滚动）。 -->
                <TableHead class="min-w-32">模型</TableHead>
                <TableHead class="whitespace-nowrap">手动开关</TableHead>
                <TableHead>自动状态</TableHead>
                <TableHead class="max-w-[14rem]">最后错误</TableHead>
                <TableHead class="w-12">失败</TableHead>
                <TableHead class="whitespace-nowrap">最近成功</TableHead>
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
                <TableCell class="select-all font-mono text-xs font-medium">{{
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
                  <span
                    v-if="scopeLabel(model)"
                    class="mb-1 inline-flex rounded px-1 py-0.5 text-[10px] font-medium"
                    :class="scopeLabel(model)?.cls"
                    >{{ scopeLabel(model)?.text }}</span
                  >
                  <ModelHealthBadge
                    :status="
                      keyEffectiveAvailable === false ? item.health_status : model.health_status
                    "
                    :available="modelAvailable(model)"
                    :manual-enabled="model.manual_enabled"
                    :failure-class="
                      model.failure_class ||
                      (keyEffectiveAvailable === false ? item.failure_class : '')
                    "
                    :disabled-until="model.disabled_until"
                    :rule-id="
                      model.last_rule_id ||
                      (keyEffectiveAvailable === false ? item.last_rule_id : '')
                    "
                    :rule-name="
                      model.last_rule_name ||
                      (keyEffectiveAvailable === false ? item.last_rule_name : '')
                    "
                    :last-error="modelError(model)"
                    hide-when-available
                  />
                </TableCell>
                <TableCell class="text-xs text-muted-foreground">
                  <Tooltip v-if="modelError(model)">
                    <TooltipTrigger as-child>
                      <span class="block w-full truncate">{{
                        modelError(model)
                      }}</span>
                    </TooltipTrigger>
                    <TooltipContent class="max-w-md whitespace-normal break-words">{{
                      modelError(model)
                    }}</TooltipContent>
                  </Tooltip>
                  <span v-else>-</span>
                </TableCell>
                <TableCell class="tabular-nums">{{ model.fail_count || 0 }}</TableCell>
                <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{
                  formatShortCN(model.last_success_at)
                }}</TableCell>
                <TableCell class="flex items-center justify-end gap-1">
                  <!-- 测试：测这个模型在当前 Key 下能不能出结果，与顶部批量测试同一条链路。 -->
                  <Button
                    variant="outline"
                    size="sm"
                    :disabled="testing(model)"
                    @click="emit('testModel', model)"
                  >
                    <RiLoader4Line v-if="testing(model)" class="animate-spin" size="14" />
                    <RiFlaskLine v-else size="14" />测试
                  </Button>
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
                    />
                    <RiDeleteBinLine v-else size="14" />删除
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
                    />
                    <RiRefreshLine v-else size="14" />恢复
                  </Button>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </AxTable>
      </div>

      <!-- 标签模式：模型名 + 状态徽标平铺，点击即勾选 -->
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
          <span
            v-if="scopeLabel(model)"
            class="rounded px-1 py-0.5 text-[10px] font-medium"
            :class="scopeLabel(model)?.cls"
            >{{ scopeLabel(model)?.text }}</span
          >
          <ModelHealthBadge
            :status="keyEffectiveAvailable === false ? item.health_status : model.health_status"
            :available="modelAvailable(model)"
            :manual-enabled="model.manual_enabled"
            :failure-class="
              model.failure_class || (keyEffectiveAvailable === false ? item.failure_class : '')
            "
            :disabled-until="model.disabled_until"
            :rule-id="
              model.last_rule_id || (keyEffectiveAvailable === false ? item.last_rule_id : '')
            "
            :rule-name="
              model.last_rule_name ||
              (keyEffectiveAvailable === false ? item.last_rule_name : '')
            "
            :last-error="modelError(model)"
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
        <p v-if="!item.models.length" class="py-4 text-sm text-muted-foreground">该 Key 暂无模型</p>
      </div>
    </div>
  </div>
</template>
