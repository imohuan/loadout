<script setup lang="ts">
// 状态一：渠道平台 tag 云。
//
// 每个 tag = 一个平台（同 base_url 的一组 Key），tag 上给：
//   1) 平台名 + 启用状态徽标（全部启用 / 部分启用 / 全部禁用）
//   2) 计数：N 个 Key、启用 A/B、去重后的模型数
//   3) 模型名方块（超出收进「+N」，悬停给完整清单）
// 点 tag 进入状态二（该平台的 Key 明细）。
import { RiArrowRightSLine } from '@remixicon/vue'
import type { ChannelPlatform } from '@/lib/channels'
import HoverTextCard from '@/components/ui/HoverTextCard.vue'

const props = withDefaults(
  defineProps<{
    platforms: ChannelPlatform[]
    /** 每个平台最多展示几个模型名，其余收进「+N」。 */
    maxChips?: number
  }>(),
  { maxChips: 4 },
)
const emit = defineEmits<{ select: [baseUrl: string] }>()

// 启用状态徽标：全部启用=绿、部分=琥珀、全禁用=灰。
const STATE_BADGE: Record<string, string> = {
  all: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  partial: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  none: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
}
const STATE_LABEL: Record<string, string> = {
  all: '全部启用',
  partial: '部分启用',
  none: '全部禁用',
}

/** chip 列表：超出 maxChips 折叠成「+N」。 */
function chips(p: ChannelPlatform) {
  return {
    shown: p.models.slice(0, props.maxChips),
    rest: Math.max(0, p.models.length - props.maxChips),
  }
}
</script>

<template>
  <div class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(19rem,1fr))]">
    <button
      v-for="p in platforms"
      :key="p.baseUrl"
      type="button"
      class="group flex min-w-0 flex-col rounded-lg border border-border bg-background p-3 text-left transition-colors hover:border-muted-foreground/40 hover:bg-muted/30"
      :class="p.enabledState === 'none' ? 'bg-muted/30' : ''"
      :aria-label="`查看 ${p.name} 的 Key 明细`"
      @click="emit('select', p.baseUrl)"
    >
      <div class="flex w-full items-center gap-2">
        <span class="min-w-0 flex-1 truncate text-sm font-semibold">{{ p.name }}</span>
        <Badge
          variant="outline"
          class="shrink-0 border text-[11px] font-medium"
          :class="STATE_BADGE[p.enabledState]"
        >
          {{ STATE_LABEL[p.enabledState] }}
        </Badge>
        <RiArrowRightSLine
          size="16"
          class="shrink-0 text-muted-foreground opacity-0 transition-opacity group-hover:opacity-100"
        />
      </div>

      <p class="mt-1 w-full truncate font-mono text-[11px] text-muted-foreground">
        {{ p.baseUrl }}
      </p>

      <div class="mt-2 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-muted-foreground">
        <span
          ><b class="font-semibold tabular-nums text-foreground">{{ p.keyCount }}</b> 个 Key</span
        >
        <span class="text-border">·</span>
        <span
          >启用
          <b class="font-semibold tabular-nums text-foreground"
            >{{ p.enabledKeyCount }}/{{ p.keyCount }}</b
          ></span
        >
        <span class="text-border">·</span>
        <span
          >模型
          <b class="font-semibold tabular-nums text-foreground">{{ p.modelCount }}</b> 个</span
        >
      </div>

      <div v-if="p.models.length" class="mt-2 flex flex-wrap gap-1">
        <span
          v-for="m in chips(p).shown"
          :key="m"
          class="max-w-40 truncate rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground/80"
          :title="m"
          >{{ m }}</span
        >
        <HoverTextCard v-if="chips(p).rest" :text="p.models.join('\n')" label="全部模型">
          <span
            class="cursor-default rounded border border-border bg-background px-1.5 py-0.5 text-[11px] text-muted-foreground"
            >+{{ chips(p).rest }}</span
          >
        </HoverTextCard>
      </div>
      <p v-else class="mt-2 text-[11px] text-muted-foreground">
        {{ p.probedFailed ? '模型探测失败' : '暂无模型' }}
      </p>
    </button>
  </div>
</template>
