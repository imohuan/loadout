<script setup lang="ts">
import { ref, computed } from 'vue'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
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
  pending?: boolean
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

const expanded = ref(false)
const selected = ref(new Set<string>())

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
          <p class="font-medium text-foreground">{{ item.channel.name }}</p>
          <p class="truncate font-mono text-xs text-muted-foreground">
            {{ item.channel.base_url }}
          </p>
        </div>
        <StatusBadge :status="item.health_status" :available="item.effective_available" />
        <span class="hidden text-sm text-muted-foreground md:block"
          >{{ item.models.length }} 个模型</span
        >
      </button>
      <div class="flex flex-wrap items-center gap-2 border-t border-border px-4 py-2 text-sm">
        <div class="flex flex-1 flex-wrap items-center gap-2">
          <template v-if="!hasSelection">
            <div class="flex items-center gap-2">
              <Switch
                :id="`channel-${item.channel.id}`"
                :model-value="item.manual_enabled"
                :disabled="pending"
                @update:model-value="emit('channelToggle', Boolean($event))"
              />
              <Label :for="`channel-${item.channel.id}`">手动启用</Label>
            </div>
            <span v-if="item.reason" class="min-w-48 flex-1 text-muted-foreground">{{
              item.reason
            }}</span>
            <Button variant="outline" size="sm" :disabled="pending" @click="emit('recoverChannel')">
              <RiRefreshLine size="16" />恢复渠道
            </Button>
            <Tooltip>
              <TooltipTrigger as-child>
                <Button
                  variant="outline"
                  size="sm"
                  :disabled="pending"
                  @click="emit('recoverAllModels')"
                >
                  <RiRestartLine size="16" />恢复全部异常
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
              :disabled="pending"
              @click="emit('batchModelToggle', item.models, false)"
            >
              关闭全部
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="pending"
              @click="emit('batchModelToggle', item.models, true)"
            >
              开启全部
            </Button>
          </template>
          <template v-else>
            <span class="text-sm text-muted-foreground">已选 {{ selected.size }} 个</span>
            <Button
              variant="outline"
              size="sm"
              :disabled="pending"
              @click="emit('batchDeleteModel', selectedModels)"
            >
              <RiDeleteBinLine size="14" />删除
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="pending"
              @click="emit('batchModelToggle', selectedModels, true)"
            >
              <RiToggleLine size="14" />开启
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="pending"
              @click="emit('batchModelToggle', selectedModels, false)"
            >
              关闭
            </Button>
            <Button
              variant="outline"
              size="sm"
              :disabled="pending"
              @click="emit('batchRecoverModel', selectedModels)"
            >
              <RiRefreshLine size="14" />恢复
            </Button>
          </template>
        </div>
        <div class="flex items-center gap-0.5">
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
                    :disabled="pending"
                    @update:model-value="emit('modelToggle', model, Boolean($event))"
                  />
                </TableCell>
                <TableCell>
                  <StatusBadge
                    :status="model.health_status"
                    :available="model.effective_available"
                  />
                </TableCell>
                <TableCell class="max-w-64 whitespace-normal text-xs text-muted-foreground">{{
                  model.last_error || model.reason || '-'
                }}</TableCell>
                <TableCell class="tabular-nums">{{ model.fail_count || 0 }}</TableCell>
                <TableCell class="whitespace-nowrap text-xs text-muted-foreground">{{
                  formatDate(model.last_success_at)
                }}</TableCell>
                <TableCell class="flex items-center justify-end gap-1">
                  <Button
                    v-if="model.source === 'manual'"
                    variant="outline"
                    size="sm"
                    :disabled="pending"
                    @click="emit('deleteModel', model)"
                  >
                    <RiDeleteBinLine size="14" />删除
                  </Button>
                  <Button
                    variant="outline"
                    size="sm"
                    :disabled="pending"
                    @click="emit('recoverModel', model)"
                  >
                    <RiRefreshLine size="14" />恢复
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
            :disabled="pending"
            @click="toggleSelect(model.model)"
          >
            <span class="font-mono">{{ model.model }}</span>
            <StatusBadge :status="model.health_status" :available="model.effective_available" />
            <Tooltip v-if="model.source === 'manual'">
              <TooltipTrigger as-child>
                <span
                  class="rounded-full p-0.5 hover:bg-destructive/20 hover:text-destructive"
                  @click.stop="emit('deleteModel', model)"
                >
                  <RiCloseLine size="12" />
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
