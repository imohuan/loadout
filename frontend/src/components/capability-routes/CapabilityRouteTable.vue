<script setup lang="ts">
import { RiDeleteBinLine, RiEditLine } from '@remixicon/vue'
import type { CapabilityRoute, Channel } from '@/lib/types'
import EmptyState from '@/components/EmptyState.vue'

defineProps<{ routes: CapabilityRoute[]; channels: Channel[]; pending?: boolean }>()
const emit = defineEmits<{ edit: [route: CapabilityRoute]; remove: [route: CapabilityRoute] }>()

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
function channelName(channels: Channel[], id?: string) {
  if (!id) return '自动路由'
  return channels.find((channel) => channel.id === id)?.name || id
}
function viaLabel(channels: Channel[], via: { via_model: string; channel_id?: string }) {
  return via.via_model + (via.channel_id ? ` @${channelName(channels, via.channel_id)}` : '')
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
                <TableHead>能力</TableHead>
                <TableHead>路由方式</TableHead>
                <TableHead>视觉候选</TableHead>
                <TableHead class="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              <TableRow v-for="route in routes" :key="(route.models || []).join(',') + ':' + route.capability">
                <TableCell class="max-w-md text-sm text-muted-foreground">
                  <div
                    class="whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                    :title="(route.models || []).join(', ')"
                  >
                    {{ (route.models || []).join(', ') || '-' }}
                  </div>
                </TableCell>
                <TableCell class="text-center tabular-nums">
                  <div class="min-w-[100px]">{{ route.models?.length || 0 }}</div>
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
                  <div
                    class="whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                    :title="(route.via_options || []).map((o) => viaLabel(channels, o)).join(' → ')"
                  >
                    {{
                      (route.via_options || []).length
                        ? route.via_options?.map((o) => viaLabel(channels, o)).join(' → ')
                        : '—'
                    }}
                  </div>
                </TableCell>
                <TableCell>
                  <div class="flex justify-end gap-1">
                    <Tooltip>
                      <TooltipTrigger as-child><Button variant="ghost" size="icon" aria-label="编辑" :disabled="pending"
                          @click="emit('edit', route)">
                          <RiEditLine size="16" />
                        </Button></TooltipTrigger>
                      <TooltipContent>编辑</TooltipContent>
                    </Tooltip>
                    <Tooltip>
                      <TooltipTrigger as-child><Button variant="ghost" size="icon" aria-label="删除" :disabled="pending"
                          @click="emit('remove', route)">
                          <RiDeleteBinLine size="16" />
                        </Button></TooltipTrigger>
                      <TooltipContent>删除</TooltipContent>
                    </Tooltip>
                  </div>
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </div>
        <EmptyState v-else title="还没有能力路由" description="给不支持视觉的模型添加代理路由，自动附加视觉能力。" />
      </CardContent>
    </Card>
  </TooltipProvider>
</template>
