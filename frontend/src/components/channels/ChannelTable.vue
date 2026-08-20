<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiArrowUpLine,
  RiDeleteBinLine,
  RiEditLine,
  RiLoader4Line,
  RiRefreshLine,
} from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { groupChannelsByBaseURL, type ChannelGroup } from '@/composables/useChannels'
import EmptyState from '@/components/EmptyState.vue'

const props = defineProps<{ channels: Channel[]; isPending?: (key: string) => boolean }>()
const emit = defineEmits<{
  addKey: [baseUrl: string]
  toggleKey: [channel: Channel]
  refreshKey: [channel: Channel]
  editKey: [channel: Channel]
  moveKey: [channel: Channel, direction: 'up' | 'down']
  removeKey: [channel: Channel]
  refreshGroup: [baseUrl: string]
  moveGroup: [baseUrl: string, direction: 'up' | 'down']
  removeGroup: [baseUrl: string]
}>()

// 展开状态：按 base_url 记录（参考 McpPanel 的 expandedServers 模式）。
const expandedGroups = ref<string[]>([])
function toggleGroup(baseUrl: string) {
  const index = expandedGroups.value.indexOf(baseUrl)
  if (index >= 0) expandedGroups.value.splice(index, 1)
  else expandedGroups.value.push(baseUrl)
}
function isGroupExpanded(baseUrl: string) {
  return expandedGroups.value.includes(baseUrl)
}

// 操作 key：与 ChannelsView.run() 使用的 key 必须完全一致，按钮级 loading/禁用才能对上。
// 组操作锁整组按钮，key 操作锁单个 key 的按钮。
function groupKey(baseUrl: string, action: string) {
  return `group:${baseUrl}:${action}`
}
function keyKey(channel: Channel, action: string) {
  return `key:${channel.id}:${action}`
}
/** 指定操作是否正在执行（按钮级 loading / disabled） */
function busy(key: string) {
  return props.isPending ? props.isPending(key) : false
}

const groups = computed(() => groupChannelsByBaseURL(props.channels))

// key 的手动开关：manual_enabled 优先，兼容旧 JSON 的 enabled 字段，默认启用。
function keyEnabled(channel: Channel) {
  return channel.manual_enabled ?? channel.enabled ?? true
}
// 组内模型数汇总：所有 key 的启用模型并集（跨 key 去重）。
function groupModelCount(keys: Channel[]) {
  const set = new Set<string>()
  for (const key of keys) {
    for (const model of key.models || []) set.add(model)
  }
  return set.size
}
// 组内启用状态汇总：全部启用 / 部分启用 / 全部禁用。
function groupEnabledState(keys: Channel[]) {
  const anyOn = keys.some(keyEnabled)
  const allOn = keys.length > 0 && keys.every(keyEnabled)
  if (allOn) return 'all'
  if (anyOn) return 'partial'
  return 'none'
}
function groupEnabledLabel(keys: Channel[]) {
  const state = groupEnabledState(keys)
  if (state === 'all') return '全部启用'
  if (state === 'partial') return '部分启用'
  return '全部禁用'
}
function modelCount(channel: Channel) {
  return channel.models?.length || (channel.models_error ? -1 : 0)
}
function modelCountLabel(channel: Channel) {
  const count = modelCount(channel)
  if (count > 0) return `${count} 个`
  if (count < 0) return '探测失败'
  return '未知'
}
// 组标题：渠道名称（channel_name）→ 兜底首个 Key 名 → 兜底 Base URL。
function groupTitle(group: ChannelGroup) {
  const first = group.keys[0]
  return first?.channel_name || first?.name || group.baseUrl
}
</script>

<template>
  <TooltipProvider>
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">渠道列表</CardTitle>
        <CardDescription>
          同一 Base URL 的所有 Key 归为一组，组内顺序即该渠道的 Key 优先级；列表顺序即普通模型路由时的优先级。
        </CardDescription>
      </CardHeader>
      <CardContent class="p-0">
        <div v-if="groups.length" class="w-full overflow-hidden">
          <Table class="table-fixed w-full">
            <colgroup>
              <col class="w-10" />
              <col />
              <col class="w-20" />
              <col class="w-20" />
              <col class="w-24" />
              <col class="w-[190px]" />
            </colgroup>
            <TableHeader>
              <TableRow>
                <TableHead></TableHead>
                <TableHead>Base URL</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>状态</TableHead>
                <TableHead class="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <template v-for="(group, groupIndex) in groups" :key="group.baseUrl">
                <TableRow>
                  <TableCell class="px-0">
                    <Button
                      variant="ghost"
                      size="icon"
                      class="size-8"
                      :aria-label="isGroupExpanded(group.baseUrl) ? '收起 Key 列表' : '展开 Key 列表'"
                      :aria-expanded="isGroupExpanded(group.baseUrl)"
                      @click="toggleGroup(group.baseUrl)"
                    >
                      <RiArrowDownSLine v-if="isGroupExpanded(group.baseUrl)" size="16" />
                      <RiArrowRightSLine v-else size="16" />
                    </Button>
                  </TableCell>
                  <TableCell>
                    <div class="min-w-0">
                      <div class="truncate text-sm font-medium">{{ groupTitle(group) }}</div>
                      <div class="truncate font-mono text-xs text-muted-foreground">
                        {{ group.baseUrl }}
                      </div>
                    </div>
                  </TableCell>
                  <TableCell>
                    <Badge variant="secondary">{{ group.keys.length }} 个</Badge>
                  </TableCell>
                  <TableCell>{{ groupModelCount(group.keys) }} 个</TableCell>
                  <TableCell>
                    <Badge :variant="groupEnabledState(group.keys) === 'none' ? 'secondary' : 'default'">
                      {{ groupEnabledLabel(group.keys) }}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <div class="flex justify-end gap-1">
                      <Tooltip>
                        <TooltipTrigger as-child>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="刷新全部 Key 模型"
                            :disabled="busy(groupKey(group.baseUrl, 'refresh'))"
                            @click="emit('refreshGroup', group.baseUrl)"
                            ><RiLoader4Line v-if="busy(groupKey(group.baseUrl, 'refresh'))" class="animate-spin" size="16" /><RiRefreshLine v-else size="16" /></Button>
                        </TooltipTrigger>
                        <TooltipContent>刷新全部 Key 模型</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger as-child>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="提高整组优先级"
                            :disabled="busy(groupKey(group.baseUrl, 'move-up')) || groupIndex === 0"
                            @click="emit('moveGroup', group.baseUrl, 'up')"
                            ><RiLoader4Line v-if="busy(groupKey(group.baseUrl, 'move-up'))" class="animate-spin" size="16" /><RiArrowUpLine v-else size="16" /></Button>
                        </TooltipTrigger>
                        <TooltipContent>提高整组优先级</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger as-child>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="降低整组优先级"
                            :disabled="busy(groupKey(group.baseUrl, 'move-down')) || groupIndex === groups.length - 1"
                            @click="emit('moveGroup', group.baseUrl, 'down')"
                            ><RiLoader4Line v-if="busy(groupKey(group.baseUrl, 'move-down'))" class="animate-spin" size="16" /><RiArrowDownLine v-else size="16" /></Button>
                        </TooltipTrigger>
                        <TooltipContent>降低整组优先级</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger as-child>
                          <Button
                            variant="ghost"
                            size="icon"
                            aria-label="删除整组"
                            :disabled="busy(groupKey(group.baseUrl, 'remove'))"
                            @click="emit('removeGroup', group.baseUrl)"
                            ><RiLoader4Line v-if="busy(groupKey(group.baseUrl, 'remove'))" class="animate-spin" size="16" /><RiDeleteBinLine v-else size="16" /></Button>
                        </TooltipTrigger>
                        <TooltipContent>删除整组（全部 Key）</TooltipContent>
                      </Tooltip>
                    </div>
                  </TableCell>
                </TableRow>
                <TableRow v-if="isGroupExpanded(group.baseUrl)" class="bg-muted/30 hover:bg-muted/30">
                  <TableCell :colspan="6" class="whitespace-normal p-0 w-full overflow-hidden">
                    <div class="space-y-3 px-4 py-4">
                      <div class="flex items-center justify-between gap-3">
                        <div class="min-w-0">
                          <div class="font-medium">Key 列表</div>
                          <div class="text-xs text-muted-foreground">
                            每个 Key 是一个独立账号：独立模型目录、独立健康状态、独立开关。
                          </div>
                        </div>
                        <div class="flex shrink-0 items-center gap-2">
                          <Badge variant="outline">{{ group.keys.length }} 个 Key</Badge>
                          <Button variant="outline" size="sm" @click="emit('addKey', group.baseUrl)">
                            <RiAddLine size="16" />添加 Key
                          </Button>
                        </div>
                      </div>
                      <div v-if="group.keys.length" class="divide-y rounded-md border bg-background">
                        <div
                          v-for="(key, keyIndex) in group.keys"
                          :key="key.id"
                          class="flex flex-col gap-3 px-3 py-3 sm:flex-row sm:items-center sm:justify-between"
                        >
                          <div class="min-w-0 flex-1 overflow-hidden">
                            <div class="truncate text-sm font-medium">{{ key.name }}</div>
                            <div class="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                              <span>{{ modelCountLabel(key) }}</span>
                              <span
                                >费用同步：
                                <Badge :variant="key.sync_billing ? 'default' : 'secondary'">{{
                                  key.sync_billing ? '开启' : '关闭'
                                }}</Badge></span
                              >
                            </div>
                          </div>
                          <div class="flex shrink-0 items-center gap-2">
                            <Badge :variant="keyEnabled(key) ? 'default' : 'secondary'">{{
                              keyEnabled(key) ? '启用' : '禁用'
                            }}</Badge>
                            <Button variant="outline" size="sm" :disabled="busy(keyKey(key, 'toggle'))" @click="emit('toggleKey', key)">
                              <RiLoader4Line v-if="busy(keyKey(key, 'toggle'))" class="animate-spin" size="16" />{{
                                busy(keyKey(key, 'toggle')) ? '处理中' : keyEnabled(key) ? '停用' : '启用'
                              }}
                            </Button>
                            <Button variant="ghost" size="sm" :disabled="busy(keyKey(key, 'refresh'))" @click="emit('refreshKey', key)">
                              <RiLoader4Line v-if="busy(keyKey(key, 'refresh'))" class="animate-spin" size="16" /><RiRefreshLine v-else size="16" />刷新模型
                            </Button>
                            <Button variant="ghost" size="sm" @click="emit('editKey', key)">
                              <RiEditLine size="16" />编辑
                            </Button>
                            <TooltipProvider>
                              <Tooltip>
                                <TooltipTrigger as-child>
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    aria-label="上移 Key"
                                    :disabled="busy(keyKey(key, 'move-up')) || keyIndex === 0"
                                    @click="emit('moveKey', key, 'up')"
                                    ><RiLoader4Line v-if="busy(keyKey(key, 'move-up'))" class="animate-spin" size="16" /><RiArrowUpLine v-else size="16" /></Button>
                                </TooltipTrigger>
                                <TooltipContent>上移 Key</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger as-child>
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    aria-label="下移 Key"
                                    :disabled="busy(keyKey(key, 'move-down')) || keyIndex === group.keys.length - 1"
                                    @click="emit('moveKey', key, 'down')"
                                    ><RiLoader4Line v-if="busy(keyKey(key, 'move-down'))" class="animate-spin" size="16" /><RiArrowDownLine v-else size="16" /></Button>
                                </TooltipTrigger>
                                <TooltipContent>下移 Key</TooltipContent>
                              </Tooltip>
                            </TooltipProvider>
                            <Tooltip>
                              <TooltipTrigger as-child>
                                <Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="删除该 Key"
                                  :disabled="busy(keyKey(key, 'remove'))"
                                  @click="emit('removeKey', key)"
                                  ><RiLoader4Line v-if="busy(keyKey(key, 'remove'))" class="animate-spin" size="16" /><RiDeleteBinLine v-else size="16" /></Button>
                              </TooltipTrigger>
                              <TooltipContent>删除该 Key</TooltipContent>
                            </Tooltip>
                          </div>
                        </div>
                      </div>
                    </div>
                  </TableCell>
                </TableRow>
              </template>
            </TableBody>
          </Table>
        </div>
        <EmptyState
          v-else
          title="还没有渠道"
          description="先添加一个上游服务，再探测可用模型。" />
      </CardContent>
    </Card>
  </TooltipProvider>
</template>
