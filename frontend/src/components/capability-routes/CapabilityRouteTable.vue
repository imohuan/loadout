<script setup lang="ts">
import { RiDeleteBinLine, RiEditLine, RiLoader4Line } from '@remixicon/vue'
import type { CapabilityRoute, Channel } from '@/lib/types'
import { formatChannelRef } from '@/composables/useChannelRef'
import EmptyState from '@/components/EmptyState.vue'

const props = defineProps<{
  routes: CapabilityRoute[]
  channels: Channel[]
  isPending?: (key: string) => boolean
}>()
const emit = defineEmits<{ edit: [route: CapabilityRoute]; remove: [route: CapabilityRoute] }>()

// 路由唯一 key：与 CapabilityRoutesView.key() 一致（capability|models|channel_ids）。
function routeKey(route: CapabilityRoute) {
  return (
    route.capability +
    '|' +
    (route.models || []).join(',') +
    '|' +
    (route.channel_ids || []).join(',')
  )
}
function busy(route: CapabilityRoute, action: string) {
  return props.isPending ? props.isPending(`route:${routeKey(route)}:${action}`) : false
}

const routeLabel: Record<string, string> = {
  native: '原生透传',
  proxy: '附加代理',
  error: '拒绝',
}
const routeVariant: Record<string, 'outline' | 'default' | 'destructive'> = {
  native: 'outline',
  proxy: 'default',
  error: 'destructive',
}
// 视觉候选展示：模型 + "@渠道名(Keys)"，统一走 formatChannelRef。
//   渠道级 → "@NewAPi"
//   单 Key → "@NewAPi(Key1)"
//   多 Key → "@NewAPi(Key1, Key2)"
function viaLabel(
  channels: Channel[],
  via: { via_model: string; channel_id?: string; channel_ids?: string[]; channel_base_url?: string },
) {
  const ref = formatChannelRef(channels, via)
  if (!ref) return via.via_model
  return via.via_model + ` @${ref}`
}
// 敏感词替换规则摘要：from → to（正则规则加 [正则] 标记）。
function replacementLabel(r: { from: string; to: string; regex?: boolean }) {
  return (r.regex ? `[正则] ${r.from}` : r.from) + ' → ' + r.to
}
// proxy 时展示内容：vision 显示视觉候选，sensitive_filter 显示替换规则。
function proxyContentLabel(route: CapabilityRoute, channels: Channel[]) {
  if (route.capability === 'sensitive_filter') {
    return (route.replacements || []).map((r) => replacementLabel(r)).join('\n')
  }
  return (route.via_options || []).map((o) => viaLabel(channels, o)).join(' → ')
}
// 目标渠道展示：`*` = 通用（全匹配）；空 = 全渠道；否则渠道名列表。
function channelScopeLabel(channels: Channel[], ids?: string[]) {
  if (!ids || !ids.length) return '全渠道'
  if (ids.includes('*')) return '通用（全匹配）'
  return ids.map((id) => channels.find((c) => c.id === id)?.name || id).join('、')
}
</script>

<template>
  <TooltipProvider>
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">能力路由列表</CardTitle>
        <CardDescription>目标模型 × 能力 命中后按路由方式处理；代理时按候选顺序兜底。</CardDescription>
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
                v-for="route in routes"
                :key="
                  (route.models || []).join(',') +
                  ':' +
                  (route.channel_ids || []).join(',') +
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
                        {{ channelScopeLabel(channels, route.channel_ids) }}
                      </div>
                    </TooltipTrigger>
                    <TooltipContent>{{ channelScopeLabel(channels, route.channel_ids) }}</TooltipContent>
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
                <TableCell class="max-w-md text-sm text-muted-foreground">
                  <Tooltip>
                    <TooltipTrigger as-child>
                      <div
                        class="whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                      >
                        {{ proxyContentLabel(route, channels) || '—' }}
                      </div>
                    </TooltipTrigger>
                    <TooltipContent>{{ proxyContentLabel(route, channels) || '—' }}</TooltipContent>
                  </Tooltip>
                </TableCell>
                <TableCell>
                  <div class="flex justify-end gap-1">
                    <Tooltip>
                      <TooltipTrigger as-child><Button variant="ghost" size="icon" aria-label="编辑"
                          @click="emit('edit', route)">
                          <RiEditLine size="16" />
                        </Button></TooltipTrigger>
                      <TooltipContent>编辑</TooltipContent>
                    </Tooltip>
                    <Tooltip>
                      <TooltipTrigger as-child><Button variant="ghost" size="icon" aria-label="删除" :disabled="busy(route, 'remove')"
                          @click="emit('remove', route)">
                          <RiLoader4Line v-if="busy(route, 'remove')" class="animate-spin" size="16" /><RiDeleteBinLine v-else size="16" />
                        </Button></TooltipTrigger>
                      <TooltipContent>删除</TooltipContent>
                    </Tooltip>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <EmptyState v-else title="还没有能力路由" description="给目标模型添加能力路由：视觉附加或敏感词过滤。" />
      </CardContent>
    </Card>
  </TooltipProvider>
</template>
