<script setup lang="ts">
import { RiDeleteBinLine, RiDraggable, RiEditLine, RiLoader4Line } from '@remixicon/vue'
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
// 窗口文案：把秒数换成好读的「1 分钟 / 2 小时 / 1 天」。
function rateWindowLabel(secs?: number): string {
  const s = secs && secs > 0 ? secs : 60
  if (s % 86400 === 0) return `${s / 86400} 天`
  if (s % 3600 === 0) return `${s / 3600} 小时`
  if (s % 60 === 0) return `${s / 60} 分钟`
  return `${s} 秒`
}
// key_rate_limit 的限流摘要：如「每 1 分钟每 Key 最多 20 次，每个 Key 并发最多 3」。
function rateLimitLabel(route: CapabilityRoute): string {
  const rl = route.rate_limit
  if (!rl || (!rl.max_requests && !rl.max_concurrency)) return ''
  const parts: string[] = []
  if (rl.max_requests) {
    parts.push(`每 ${rateWindowLabel(rl.window_seconds)}每 Key 最多 ${rl.max_requests} 次`)
  }
  if (rl.max_concurrency) parts.push(`每个 Key 并发最多 ${rl.max_concurrency}`)
  const tail = rl.on_exhausted === 'reuse' ? '全部用满继续使用' : '全部用满返回 429'
  return `${parts.join('，')}（${tail}）`
}
// 列表里的「代理配置」只展示真正会生效的配置。
// native（以及历史遗留的 error）不会改写请求，统一显示「—」，
// 避免把编辑弹窗里残留的候选/规则误读成已启用。
function routeActive(route: CapabilityRoute) {
  return route.route === 'proxy'
}
// 统一的配置标签：先看标签快速扫一眼关键开关，再看下面那行摘要了解细节。
// kind：on = 已开启的关键开关；off = 未开启/未配置；info = 数量类信息。
type ConfigTag = { text: string; kind: 'on' | 'off' | 'info' }
function configTags(route: CapabilityRoute): ConfigTag[] {
  if (route.capability === 'sensitive_filter') {
    const list = route.replacements || []
    const regexCount = list.filter((r) => r.regex).length
    const tags: ConfigTag[] = [
      {
        text: list.length ? `${list.length} 条规则` : '未配置规则',
        kind: list.length ? 'info' : 'off',
      },
    ]
    if (regexCount) tags.push({ text: `正则 ${regexCount} 条`, kind: 'info' })
    return tags
  }
  if (route.capability === 'field_filter') {
    const r = route.field_rules
    const tags: ConfigTag[] = []
    const requestCount = (r?.request_strip?.length || 0) + (r?.request_keep?.length || 0)
    const requestHeaderCount = r?.request_header_strip?.length || 0
    const responseCount = (r?.response_strip?.length || 0) + (r?.response_keep?.length || 0)
    const responseHeaderCount = r?.response_header_strip?.length || 0
    if (requestCount) tags.push({ text: `请求体 ${requestCount} 项`, kind: 'info' })
    if (requestHeaderCount) tags.push({ text: `请求头 ${requestHeaderCount} 项`, kind: 'info' })
    if (responseCount) tags.push({ text: `响应体 ${responseCount} 项`, kind: 'info' })
    if (responseHeaderCount) tags.push({ text: `响应头 ${responseHeaderCount} 项`, kind: 'info' })
    return tags.length ? tags : [{ text: '未配置规则', kind: 'off' }]
  }
  if (route.capability === 'message_inject') {
    const count = (route.injections || []).length
    return [{ text: count ? `${count} 条注入` : '未配置注入', kind: count ? 'info' : 'off' }]
  }
  if (route.capability === 'key_rate_limit') {
    const rl = route.rate_limit
    if (!rl || (!rl.max_requests && !rl.max_concurrency)) {
      return [{ text: '未配置限流', kind: 'off' }]
    }
    const tags: ConfigTag[] = []
    if (rl.max_requests) {
      tags.push({
        text: `${rateWindowLabel(rl.window_seconds)} / ${rl.max_requests} 次`,
        kind: 'info',
      })
    }
    if (rl.max_concurrency) tags.push({ text: `并发 ${rl.max_concurrency}`, kind: 'info' })
    tags.push({
      text: rl.on_exhausted === 'reuse' ? '超额继续用' : '超额 429',
      kind: rl.on_exhausted === 'reuse' ? 'info' : 'off',
    })
    return tags
  }
  if (route.capability === 'request_log') {
    return [
      route.request_log_keep_base64
        ? { text: '保留原始 base64', kind: 'on' }
        : { text: '图片打码', kind: 'off' },
    ]
  }
  // request_capture / force_stream / toolcall_repair 没有可调项，摘要一行就够。
  if (
    route.capability === 'request_capture' ||
    route.capability === 'force_stream' ||
    route.capability === 'toolcall_repair'
  ) {
    return []
  }
  if (route.capability === 'vision') {
    const count = (route.via_options || []).length
    return [{ text: count ? `${count} 个候选` : '未配置候选', kind: count ? 'info' : 'off' }]
  }
  return []
}
// 配置摘要：每个能力一句短话（最多两条），完整内容放在悬浮提示里。
function configPreview(route: CapabilityRoute): string {
  if (route.capability === 'sensitive_filter') {
    return previewLines((route.replacements || []).map((r) => replacementLabel(r)))
  }
  if (route.capability === 'field_filter') {
    return previewLines(fieldRulesLabel(route).split('\n'))
  }
  if (route.capability === 'message_inject') {
    return previewLines(messageInjectLabel(route).split('\n'))
  }
  if (route.capability === 'key_rate_limit') return rateLimitLabel(route)
  if (route.capability === 'request_log') return '记录完整请求/响应（脱敏后落库）'
  if (route.capability === 'request_capture') return '抓包保存原始报文（含 base64）'
  if (route.capability === 'force_stream') return '强制流式，整包非流式返回'
  if (route.capability === 'toolcall_repair') return '清理空工具名、补齐缺失的工具结果'
  return ''
}
// 摘要最多显示两条，多余的用「等 N 条」收尾。
function previewLines(lines: string[]): string {
  const clean = lines.filter(Boolean)
  if (!clean.length) return ''
  const shown = clean.slice(0, 2).join('；')
  return clean.length > 2 ? `${shown}；等 ${clean.length} 条` : shown
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
          step-5-preview 的 request_log，排在前面的决定是否保留原始
          base64）。拖拽或上下移动即可调整。</CardDescription
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
                        <div class="flex min-w-0 flex-col gap-0.5">
                          <span v-if="!routeActive(route)" class="text-muted-foreground">—</span>
                          <template v-else>
                            <div
                              v-if="configTags(route).length"
                              class="flex flex-wrap items-center gap-1"
                            >
                              <Badge
                                v-for="(t, i) in configTags(route)"
                                :key="i"
                                :variant="
                                  t.kind === 'on'
                                    ? 'default'
                                    : t.kind === 'off'
                                      ? 'outline'
                                      : 'secondary'
                                "
                                class="px-1.5 py-0 text-[10px] leading-3.5 font-normal"
                                :class="t.kind === 'off' ? 'text-muted-foreground' : ''"
                                >{{ t.text }}</Badge
                              >
                            </div>
                            <div
                              v-if="route.capability === 'vision'"
                              class="flex flex-wrap items-center gap-1 text-xs leading-4"
                            >
                              <template v-for="(o, i) in viaOptions(route)" :key="i">
                                <ModelChannelRef
                                  :model="o.via_model"
                                  :target="o"
                                  :channels="channels"
                                />
                                <span v-if="i < viaOptions(route).length - 1" class="mx-0.5"
                                  >→</span
                                >
                              </template>
                            </div>
                            <div
                              v-else-if="configPreview(route)"
                              class="text-xs leading-4 whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                            >
                              {{ configPreview(route) }}
                            </div>
                          </template>
                        </div>
                      </TooltipTrigger>
                      <TooltipContent side="top" align="start">
                        <template v-if="!routeActive(route)">
                          <span class="whitespace-pre-wrap"
                            >原生透传：请求/响应原样转发，此能力的配置不会生效。</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'sensitive_filter'">
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
                          <span class="whitespace-pre-wrap">{{
                            rateLimitLabel(route) || '未配置限流'
                          }}</span>
                          <span class="whitespace-pre-wrap"
                            >窗口内次数用满、或在途并发占满的渠道 Key
                            会被暂时跳过，请求自动落到同平台的另一个账号</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'request_log'">
                          <span class="whitespace-pre-wrap"
                            >记录完整请求/响应（脱敏后落库）。{{
                              route.request_log_keep_base64
                                ? '已开启「保留原始 base64」：图片原文照存，日志体积会明显变大。'
                                : '未开启「保留原始 base64」：图片以 [image: mime, NB] 占位。'
                            }}</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'request_capture'">
                          <span class="whitespace-pre-wrap"
                            >把进入中转的完整原始报文（含 Authorization 与图片 base64）打包落盘，
                            「请求抓包捕获」页可查看、下载并生成复现脚本。</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'force_stream'">
                          <span class="whitespace-pre-wrap"
                            >非流式客户端请求在此转流式请求上游，缓冲完整段后整包按非流式返回</span
                          >
                        </template>
                        <template v-else-if="route.capability === 'toolcall_repair'">
                          <span class="whitespace-pre-wrap"
                            >转发前整理工具调用历史：清掉空工具名的坏调用，补齐缺失的工具结果，合并相邻 assistant</span
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
