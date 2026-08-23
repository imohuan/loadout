<script setup lang="ts">
import {
  RiArrowDownLine,
  RiArrowUpLine,
  RiDeleteBinLine,
  RiEditLine,
  RiLoader4Line,
} from '@remixicon/vue'
import type { CapabilityRoute, Channel } from '@/lib/types'
import {
  formatChannelGroupLabel,
  channelLevelSegments,
  groupSegmentsFor,
  mergeSegments,
} from '@/composables/useChannelRef'
import ModelChannelRef from '@/components/ModelChannelRef.vue'
import EmptyState from '@/components/EmptyState.vue'

const props = defineProps<{
  routes: CapabilityRoute[]
  channels: Channel[]
  isPending?: (key: string) => boolean
}>()
const emit = defineEmits<{
  edit: [route: CapabilityRoute]
  remove: [route: CapabilityRoute]
  move: [index: number, direction: -1 | 1]
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
          >目标模型 × 能力 命中后按路由方式处理；代理时按候选顺序兜底。</CardDescription
        >
      </CardHeader>
      <CardContent class="p-0">
        <div v-if="routes.length" class="overflow-x-auto">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>目标模型</TableHead>
                <TableHead class="w-16 text-center">数量</TableHead>
                <TableHead>渠道</TableHead>
                <TableHead>能力</TableHead>
                <TableHead>路由方式</TableHead>
                <TableHead>代理配置</TableHead>
                <TableHead class="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow
                v-for="(route, index) in routes"
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
                <TableCell class="text-center tabular-nums">
                  <div class="min-w-[100px]">{{ route.models?.length || 0 }}</div>
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
                        <span class="whitespace-pre-wrap">{{ fieldRulesLabel(route) || '—' }}</span>
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
                          aria-label="上移"
                          :disabled="index === 0 || busy(route, 'move')"
                          @click="emit('move', index, -1)"
                        >
                          <RiArrowUpLine size="16" /> </Button
                      ></TooltipTrigger>
                      <TooltipContent>上移</TooltipContent>
                    </Tooltip>
                    <Tooltip>
                      <TooltipTrigger as-child
                        ><Button
                          variant="ghost"
                          size="icon"
                          aria-label="下移"
                          :disabled="index === routes.length - 1 || busy(route, 'move')"
                          @click="emit('move', index, 1)"
                        >
                          <RiArrowDownLine size="16" /> </Button
                      ></TooltipTrigger>
                      <TooltipContent>下移</TooltipContent>
                    </Tooltip>
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
            </TableBody>
          </Table>
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
