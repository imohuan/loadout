<script setup lang="ts">
import { RiRefreshLine } from '@remixicon/vue'
import { TooltipProvider } from 'shadcn-vue-cdn'

/**
 * 「自动刷新」开关（受控）。
 *
 * 语义只有一条：列表会不会自己更新。**要不要跑定时器、什么时候跑**属于页面的事，
 * 这里不掺和——所以不带「仅第 1 页」这类限定语，避免用户中途翻页时产生歧义。
 * 当页面主动停表时置为关闭态，用户看到的就是「现在没有在自动刷新」，与实际相符。
 *
 * 两个 TooltipProvider 是必须的：shadcn-vue 的 Tooltip 依赖上层的 Provider 注入
 * context，项目里每个用到 Tooltip 的地方都自带一个（见 RouteLogFilters / DashboardLayout）。
 */
withDefaults(
  defineProps<{
    enabled: boolean
    /** 关闭原因，用来解释「开关是开的、但列表其实没在自动刷新」。 */
    paused?: boolean
  }>(),
  { paused: false },
)
const emit = defineEmits<{ 'update:enabled': [value: boolean] }>()
</script>

<template>
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger as-child>
        <label class="flex cursor-pointer items-center gap-2 text-sm whitespace-nowrap">
          <span class="text-muted-foreground">自动刷新</span>
          <Switch
            :model-value="enabled"
            aria-label="自动刷新"
            @update:model-value="emit('update:enabled', $event)"
          />
        </label>
      </TooltipTrigger>
      <TooltipContent>
        {{
          paused
            ? '当前不在第 1 页，已暂停自动刷新；回到第 1 页会自动继续'
            : enabled
              ? '第 1 页每 3 秒自动刷新；翻到其他页会自动暂停'
              : '已关闭：列表只在手动刷新、筛选、翻页时更新'
        }}
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
