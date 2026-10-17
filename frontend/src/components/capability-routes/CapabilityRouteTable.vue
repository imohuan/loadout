<script setup lang="ts">
import {
  RiDeleteBinLine,
  RiDraggable,
  RiEditLine,
  RiLoader4Line,
} from '@remixicon/vue'
import { VueDraggable } from 'vue-draggable-plus'
import { ref, watch } from 'vue'
import type { CapabilityRoute, Channel } from '@/lib/types'
import {
  formatChannelGroupLabel,
  channelLevelSegments,
  groupSegmentsFor,
  mergeSegments,
} from '@/composables/useChannelRef'
import ModelChannelRef from '@/components/ModelChannelRef.vue'
import EmptyState from '@/components/EmptyState.vue'
import AxTable from '@/components/ui/AxTable.vue'

const props = defineProps<{
  routes: CapabilityRoute[]
  channels: Channel[]
  isPending?: (key: string) => boolean
}>()
const emit = defineEmits<{
  edit: [route: CapabilityRoute]
  remove: [route: CapabilityRoute]
  reorder: [routes: CapabilityRoute[]]
}>()

// 路由唯一 key：与 CapabilityRoutesView.key() 一致（capability|models|channel_ids|channel_base_urls）。
function routeKey(route: CapabilityRoute) {
  return (
    route.capability +
    '|' +
    (route.models || []).join(',') +
    '|' +
    (route.channel_ids || []).join(',') +
    '|' +
    (route.channel_base_urls || []).join(',')
  )
}
function busy(route: CapabilityRoute, action: string) {
  return props.isPending ? props.isPending(`route:${routeKey(route)}:${action}`) : false
}

// 拖拽重排（vue-draggable-plus）：仅左侧把手可拖，整行拖拽会误触编辑/删除。
// VueDraggable 需要可写数组承载内部移动，用本地副本，结束后回传新顺序。
const dragList = ref<CapabilityRoute[]>([])
watch(
  () => props.routes,
  (v) => {
    dragList.value = [...v]
  },
  { immediate: true },
)
function onReorder(list: CapabilityRoute[]) {
  dragList.value = list
  emit('reorder', list)
}

const routeLabel: Record<string, string> = {
  native: '原生透传',
  proxy: '附加代理',
}
const routeVariant: Record<string, 'outline' | 'default' | 'destructive'> = {
  native: 'outline',
  proxy: 'default',
}
// 视觉候选数组（模板里逐项渲染 <ChannelRef>，格式统一为「模型 @渠道名(Keys)」）。
function viaOptions(route: CapabilityRoute) {
  return route.via_options || []
}
// 敏感词替换规则摘要：from → to（正则规则加 [正则] 标记）。
function replacementLabel(r: { from: string; to: string; regex?: boolean }) {
  return (r.regex ? `[正则] ${r.from}` : r.from) + ' → ' + r.to
}
// sensitive_filter 的 proxy 配置展示（文本）。
function proxyReplacementsLabel(route: CapabilityRoute) {
  return (route.replacements || []).map((r) => replacementLabel(r)).join('\n')
}
// message_inject 的注入配置摘要：每条输出「[位置] role: 内容」。
const injectPositionLabel: Record<string, string> = {
  prepend: '新增为第一条',
  append: '新增为最后一条',
  prepend_first: '拼到第一条开头',
  append_first: '拼到第一条结尾',
}
function messageInjectLabel(route: CapabilityRoute): string {
  return (route.injections || [])
    .map((i) => {
      const pos = injectPositionLabel[i.position] || i.position || 'prepend'
      const content = (i.content || '').replace(/\n/g, '⏎')
      return `[${pos}] ${i.role || 'system'}: ${content}`
    })
    .join('\n')
}
// field_filter 的字段过滤规则摘要：非空项逐行输出「方向+动作: 字段路径」，空项省略。
function fieldRulesLabel(route: CapabilityRoute): string {
  const r = route.field_rules
  if (!r) return ''
  const lines: string[] = []
  const push = (label: string, list?: string[]) => {
    if (list && list.length) lines.push(`${label}: ${list.join(', ')}`)
  }
  push('请求体剔除', r.request_strip)
  push('请求体保留', r.request_keep)
  push('请求头剔除', r.request_header_strip)
  push('响应体剔除', r.response_strip)
  push('响应体保留', r.response_keep)
  push('响应头剔除', r.response_header_strip)
  return lines.join('\n')
}
// key_rate_limit 的限流摘要：如「每 Key 60s 最多 20 次，超额换 Key」。
function rateLimitLabel(route: CapabilityRoute): string {
  const rl = route.rate_limit
  if (!rl || (!rl.max_requests && !rl.max_concurrency)) return ''
  const parts: string[] = []
  if (rl.max_requests) {
    const secs = rl.window_seconds && rl.window_seconds > 0 ? rl.window_seconds : 60
    parts.push(`每 ${secs}s 每 Key 最多 ${rl.max_requests} 次`)
  }
  if (rl.max_concurrency) parts.push(`并发 ${rl.max_concurrency}`)
  const tail = rl.on_exhausted === 'reuse' ? '全部用满继续使用' : '全部用满返回 429'
  return `${parts.join(' + ')}（${tail}）`
}
// 目标渠道展示：空 / 含 `*`（老数据全匹配）= 全渠道；否则按 base_url 分组聚合「渠道名(Key1, Key2)」
// （渠道级段无括号、Key 级段带括号——与 ChannelRef 组件规范一致）。
function channelScopeLabel(channels: Channel[], ids?: string[], baseURLs?: string[]) {
  const allIDs = ids || []
  const baseURLsList = baseURLs || []
  if (!allIDs.length && !baseURLsList.length) return '全渠道'
  if (allIDs.includes('*')) return '全渠道'
  return mergeSegments(
    channelLevelSegments(channels, baseURLsList),
    groupSegmentsFor(channels, allIDs),
  )
    .map(formatChannelGroupLabel)
    .join(', ')
}
</script>

<template>
  <TooltipProvider>
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">能力路由列表</CardTitle>
        <CardDescription
          ><strong class="font-medium text-foreground">顺序即优先级</strong
          >：越靠上越优先。同一能力有多条规则同时命中时，只有最靠上的那条生效（例如两条都配了
          step-5-preview 的 request_log，排在前面的决定是否保留原始 base64）。拖拽或上下移动即可调整。</CardDescription
        >
      </CardHeader>
      <CardContent class="p-0">
        <div v-if="routes.length" class="overflow-x-auto">
          <AxTable>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="w-8"></TableHead>
                  <TableHead class="w-14">优先级</TableHead>
                  <TableHead>目标模型</TableHead>
                  <TableHead>渠道</TableHead>
                  <TableHead>能力</TableHead>
                  <TableHead>路由方式</TableHead>
                  <TableHead>代理配置</TableHead>
                  <TableHead class="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <VueDraggable
                  v-model="dragList"
                  tag="tbody"
                  handle=".cursor-grab"
                  :animation="150"
                  @update:model-value="onReorder"
                >
                <TableRow
                  v-for="(route, routeIndex) in dragList"
                  :key="
                    (route.models || []).join(',') +
                    ':' +
                    (route.channel_ids || []).join(',') +
                    ':' +
                    (route.channel_base_urls || []).join(',') +
                    ':' +
                    route.capability
                  "
                >
                  <TableCell class="w-8 pr-0">
                    <RiDraggable
                      class="cursor-grab text-muted-foreground/60 hover:text-foreground active:cursor-grabbing"
                      size="16"
                      aria-label="拖拽排序"
                    />
                  </TableCell>
                  <TableCell class="tabular-nums text-xs text-muted-foreground">
                    {{ routeIndex + 1 }}
                  </TableCell>
                  <TableCell class="max-w-md text-sm text-muted-foreground">
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <div
                          class="whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                        >
                          {{ (route.models || []).join(', ') || '-' }}
                        </div>
                      </TooltipTrigger>
                      <TooltipContent>{{ (route.models || []).join(', ') || '-' }}</TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell class="max-w-xs text-sm text-muted-foreground">
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <div
                          class="whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                        >
                          {{
                            channelScopeLabel(channels, route.channel_ids, route.channel_base_urls)
                          }}
                        </div>
                      </TooltipTrigger>
                      <TooltipContent>{{
                        channelScopeLabel(channels, route.channel_ids, route.channel_base_urls)
                      }}</TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline">{{ route.capability }}</Badge>
                  </TableCell>
                  <TableCell>
                    <Badge :variant="routeVariant[route.route] || 'outline'">{{
                      routeLabel[route.route] || route.route
                    }}</Badge>
                  </TableCell>
                  <TableCell class="min-w-0 max-w-md text-sm text-muted-foreground">
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <div
                          class="min-w-0 whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                        >
                          <template v-if="route.capability === 'sensitive_filter'">
                            {{ proxyReplacementsLabel(route) || '—' }}
                          </template>
                          <template v-else-if="route.capability === 'field_filter'">
                            {{ fieldRulesLabel(route) || '—' }}
                          </template>
                          <template v-else-if="route.capability === 'request_log'">
                            记录完整请求/响应
                          </template>
                          <template v-else-if="route.capability === 'request_capture'">
                            抓包保存原始报文（含 base64）
                          </template>
                          <template v-else-if="route.capability === 'force_stream'">
                            强制流式，整包非流式返回
                          </template>
                          <template v-else-if="route.capability === 'message_inject'">
                            {{ messageInjectLabel(route) || '—' }}
                          </template>
                          <template v-else-if="route.capability === 'key_rate_limit'">
                            {{ rateLimitLabel(route) || '—' }}
                          </template>
                          <template v-else>
                            <template v-for="(o, i) in viaOptions(route)" :key="i">
                              <ModelChannelRef
                                :model="o.via_model"
                                :target="o"
                                :channels="channels"
                              />
                              <template v-if="i < viaOptions(route).length - 1"
                                ><span class="mx-1">→</span></template
                              >
                            </template>
                            <span v-if="!viaOptions(route).length">—</span>
                          </template>
                        </div>
                      </TooltipTrigger>
                      <TooltipContent side="top" align="start">
                        <template v-if="route.capability === 'sensitive_filter'">
                          <span class="whitespace-pre-wrap">{{
                            proxyReplacementsLabel(route) || '—'
                          }}</span>
                        </template>
                        <template v-else-if="route.capability === 'field_filter'">
                          <span class="whitespace-pre-wrap">{{
                            fieldRulesLabel(route) || '—'
                          }}</span>
                        </template>
                        <template v-else-if="route.capability === 'message_inject'">
                          <span class="whitespace-pre-wrap">{{
                            messageInjectLabel(route) || '—'
                          }}</span>
                        </template>
                        <template v-else-if="route.capability === 'key_rate_limit'">
                          <span class="whitespace-pre-wrap"
                            >窗口内次数用满、或在途并发占满的渠道 Key
                            会被暂时跳过，请求自动落到同平台的另一个账号</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'force_stream'">
                          <span class="whitespace-pre-wrap"
                            >非流式客户端请求在此转流式请求上游，缓冲完整段后整包按非流式返回</span
                          >
                        </template>
                        <template v-else>
                          <div class="flex flex-col items-start gap-1">
                            <div
                              v-for="(o, i) in viaOptions(route)"
                              :key="i"
                              class="flex items-center gap-1.5"
                            >
                              <ModelChannelRef
                                :model="o.via_model"
                                :target="o"
                                :channels="channels"
                              />
                              <template v-if="i < viaOptions(route).length - 1"
                                ><span class="text-muted-foreground">→</span></template
                              >
                            </div>
                            <span v-if="!viaOptions(route).length">—</span>
                          </div>
                        </template>
                      </TooltipContent>
                    </Tooltip>
                  </TableCell>
                  <TableCell>
                    <div class="flex justify-end gap-1">
                      <Tooltip>
                        <TooltipTrigger as-child
                          ><Button
                            variant="ghost"
                            size="icon"
                            aria-label="编辑"
                            @click="emit('edit', route)"
                          >
                            <RiEditLine size="16" /> </Button
                        ></TooltipTrigger>
                        <TooltipContent>编辑</TooltipContent>
                      </Tooltip>
                      <Tooltip>
                        <TooltipTrigger as-child
                          ><Button
                            variant="ghost"
                            size="icon"
                            aria-label="删除"
                            :disabled="busy(route, 'remove')"
                            @click="emit('remove', route)"
                          >
                            <RiLoader4Line
                              v-if="busy(route, 'remove')"
                              class="animate-spin"
                              size="16"
                            /><RiDeleteBinLine v-else size="16" /> </Button
                        ></TooltipTrigger>
                        <TooltipContent>删除</TooltipContent>
                      </Tooltip>
                    </div>
                  </TableCell>
                </TableRow>
              </VueDraggable>
            </Table>
          </AxTable>
        </div>
        <EmptyState
          v-else
          title="还没有能力路由"
          description="给目标模型添加能力路由：视觉附加或敏感词过滤。"
        />
      </CardContent>
    </Card>
  </TooltipProvider>
</template>
