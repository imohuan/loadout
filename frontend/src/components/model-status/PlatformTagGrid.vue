<script setup lang="ts">
// 状态一：平台 tag 云。
//
// 每个 tag = 一个平台（同 base_url 的一组 Key），tag 里直接给三样东西：
//   1) 平台名 + 整体状态圆点/徽章（可用 / 部分异常 / 不可用 / 手动关闭）
//   2) 计数：N 个 Key、可用 Key A/B、可用模型 X/Y
//   3) 模型名 chip：可用的直接列出来，不可用的划掉，超出用「+N」
// 点 tag 进入状态二（该平台的 Key 明细）。
import { RiArrowRightSLine } from '@remixicon/vue'
import { PLATFORM_TONE_LABEL, type PlatformSummary } from '@/lib/modelStatus'
import HoverTextCard from '@/components/ui/HoverTextCard.vue'

const props = withDefaults(
  defineProps<{
    platforms: PlatformSummary[]
    /** 每个平台在 tag 里最多展示几个模型名，其余收进「+N」。 */
    maxChips?: number
  }>(),
  { maxChips: 4 },
)
const emit = defineEmits<{ select: [baseUrl: string] }>()

// 状态圆点与徽章文案：语义色与 ModelHealthBadge 对齐（绿=好、琥珀=部分、红=坏、灰=手动关）。
const TONE_DOT: Record<string, string> = {
  ok: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-red-500',
  off: 'bg-slate-400',
}
const TONE_BADGE: Record<string, string> = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  warn: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
  bad: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  off: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
}
/**
 * tag 里要展示的模型清单。
 *
 * 搜索只命中模型名时（matchedModels 有值），收窄到命中的那几个 —— 否则用户搜
 * 「deepseek*」却看到整组 10 个模型，还得自己找哪个才是命中的。命中平台名时
 * 保持完整清单，因为整个平台都相关。
 */
function visibleModels(p: PlatformSummary) {
  if (!p.matchedModels) return p.models
  const hit = new Set(p.matchedModels)
  return p.models.filter((m) => hit.has(m.model))
}

/** chip 列表：可用模型排前面；超出 maxChips 的部分折叠成「+N」。 */
function chips(p: PlatformSummary) {
  const listed = [...visibleModels(p)].sort((a, b) => Number(b.available) - Number(a.available))
  return {
    shown: listed.slice(0, props.maxChips),
    rest: Math.max(0, listed.length - props.maxChips),
  }
}

/** 完整模型清单：chip 装不下时用悬停卡片给出全文。 */
function fullModelText(p: PlatformSummary) {
  return visibleModels(p)
    .map((m) => (m.available ? '✓ ' : '✗ ') + m.model)
    .join('\n')
}
</script>

<template>
  <!-- 自适应网格：宽度够就并排，窄屏自动落成单列。 -->
  <div class="grid gap-3 [grid-template-columns:repeat(auto-fill,minmax(19rem,1fr))]">
    <button
      v-for="p in platforms"
      :key="p.baseUrl"
      type="button"
      class="group flex min-w-0 flex-col rounded-lg border border-border bg-background p-3 text-left transition-colors hover:border-muted-foreground/40 hover:bg-muted/30"
      :class="p.tone === 'off' ? 'bg-muted/30' : ''"
      :aria-label="`查看 ${p.name} 的 Key 明细`"
      @click="emit('select', p.baseUrl)"
    >
      <div class="flex w-full items-center gap-2">
        <span class="size-2 shrink-0 rounded-full" :class="TONE_DOT[p.tone]" />
        <span class="min-w-0 flex-1 truncate text-sm font-semibold">{{ p.name }}</span>
        <Badge
          variant="outline"
          class="shrink-0 border text-[11px] font-medium"
          :class="TONE_BADGE[p.tone]"
        >
          {{ PLATFORM_TONE_LABEL[p.tone] }}
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
          >可用 Key
          <b class="font-semibold tabular-nums text-foreground"
            >{{ p.availableKeyCount }}/{{ p.keyCount }}</b
          ></span
        >
        <span class="text-border">·</span>
        <span
          >可用模型
          <b class="font-semibold tabular-nums text-foreground"
            >{{ p.availableModelCount }}/{{ p.modelCount }}</b
          ></span
        >
      </div>

      <div v-if="p.models.length" class="mt-2 flex flex-wrap gap-1">
        <span
          v-for="m in chips(p).shown"
          :key="m.model"
          class="max-w-40 truncate rounded border px-1.5 py-0.5 font-mono text-[11px]"
          :class="
            m.available
              ? 'border-border bg-muted text-foreground/80'
              : 'border-red-500/20 bg-background text-red-600 line-through dark:text-red-400'
          "
          :title="m.model"
          >{{ m.model }}</span
        >
        <HoverTextCard
          v-if="chips(p).rest"
          :text="fullModelText(p)"
          label="全部模型（✓ 可用 / ✗ 不可用）"
        >
          <span
            class="cursor-default rounded border border-border bg-background px-1.5 py-0.5 text-[11px] text-muted-foreground"
            >+{{ chips(p).rest }}</span
          >
        </HoverTextCard>
      </div>
      <p v-else class="mt-2 text-[11px] text-muted-foreground">该平台暂无模型</p>
    </button>
  </div>
</template>
