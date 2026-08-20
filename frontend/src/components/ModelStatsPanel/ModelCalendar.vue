<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RiArrowLeftSLine, RiArrowRightSLine } from '@remixicon/vue'
import type { ModelCalendarPoint } from '@/lib/types'

const props = defineProps<{
  calendar: ModelCalendarPoint[]
  days?: number
}>()

function formatLocalDate(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${y}-${m}-${day}`
}

const today = new Date()
const todayKey = formatLocalDate(today)

interface DayCell {
  date: string | null
  day: number
  tokens: number
  isToday: boolean
  inMonth: boolean
}

const calendarMap = computed(() => {
  const m: Record<string, number> = {}
  for (const p of props.calendar) m[p.date] = (m[p.date] ?? 0) + p.tokens
  return m
})

const maxTokens = computed(() => Math.max(1, ...props.calendar.map((c) => c.tokens)))

function pickInitialCursor() {
  if (props.calendar.length > 0) {
    let latest = props.calendar[0].date
    for (const p of props.calendar) if (p.date > latest) latest = p.date
    const [y, m] = latest.split('-').map(Number)
    return { year: y, month: m - 1 }
  }
  return { year: today.getFullYear(), month: today.getMonth() }
}

const cursor = ref(pickInitialCursor())

watch(() => props.calendar, () => {
  cursor.value = pickInitialCursor()
})

function prevMonth() {
  const d = new Date(cursor.value.year, cursor.value.month - 1, 1)
  cursor.value = { year: d.getFullYear(), month: d.getMonth() }
}
function nextMonth() {
  const d = new Date(cursor.value.year, cursor.value.month + 1, 1)
  cursor.value = { year: d.getFullYear(), month: d.getMonth() }
}

const monthLabel = computed(() => `${cursor.value.year} 年 ${cursor.value.month + 1} 月`)

const weeks = computed<DayCell[][]>(() => {
  const { year, month } = cursor.value
  const first = new Date(year, month, 1)
  const startWeekday = first.getDay() // 0 = Sun
  const daysInMonth = new Date(year, month + 1, 0).getDate()
  const cells: DayCell[] = []

  // 上月填充
  for (let i = 0; i < startWeekday; i++) {
    cells.push({ date: null, day: 0, tokens: 0, isToday: false, inMonth: false })
  }
  // 当月日期
  for (let d = 1; d <= daysInMonth; d++) {
    const date = new Date(year, month, d)
    const key = formatLocalDate(date)
    cells.push({
      date: key,
      day: d,
      tokens: calendarMap.value[key] ?? 0,
      isToday: key === todayKey,
      inMonth: true,
    })
  }
  // 下月填充到 6 行 = 42 格
  while (cells.length < 42) {
    cells.push({ date: null, day: 0, tokens: 0, isToday: false, inMonth: false })
  }

  const result: DayCell[][] = []
  for (let i = 0; i < 42; i += 7) result.push(cells.slice(i, i + 7))
  return result
})

function color(tokens: number) {
  if (tokens <= 0) return 'transparent'
  const ratio = tokens / maxTokens.value
  const alpha = Math.min(0.7, 0.1 + ratio * 0.6)
  return `rgba(245, 158, 11, ${alpha.toFixed(2)})`
}

function fmt(n: number): string {
  if (n >= 10000) return (n / 1000).toFixed(1) + 'K'
  if (n >= 1000) return (n / 1000).toFixed(2) + 'K'
  return String(Math.round(n))
}
</script>

<template>
  <TooltipProvider>
    <Card class="rounded-md">
    <CardHeader>
      <div class="flex items-center justify-between gap-2">
        <CardTitle class="text-base">积分消耗月历</CardTitle>
        <div class="flex items-center gap-1">
          <button
            type="button"
            class="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="上月"
            @click="prevMonth"
          >
            <RiArrowLeftSLine size="16" />
          </button>
          <span class="min-w-[6.5rem] text-center text-sm tabular-nums">{{ monthLabel }}</span>
          <button
            type="button"
            class="flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-muted hover:text-foreground"
            aria-label="下月"
            @click="nextMonth"
          >
            <RiArrowRightSLine size="16" />
          </button>
        </div>
      </div>
      <CardDescription>近 {{ days ?? 30 }} 天 · 以 Token 计 · 本地无积分</CardDescription>
    </CardHeader>
    <CardContent>
      <div v-if="calendar.length === 0" class="py-8 text-center text-sm text-muted-foreground">暂无数据</div>
      <template v-else>
        <div class="grid grid-cols-7 gap-1 px-1 text-center text-xs text-muted-foreground">
          <div v-for="w in ['日', '一', '二', '三', '四', '五', '六']" :key="w" class="py-1">{{ w }}</div>
        </div>
        <div class="grid grid-cols-7 gap-1 text-center text-xs">
          <template v-for="(week, wi) in weeks" :key="wi">
            <Tooltip v-for="(cell, ci) in week" :key="ci" :disabled="!cell.date">
              <TooltipTrigger as-child>
                <div
                  class="flex min-h-[44px] flex-col items-center justify-center rounded-md border p-1 transition-colors"
                  :class="[
                    cell.isToday
                      ? 'border-foreground bg-foreground text-background'
                      : cell.tokens > 0
                        ? 'border-amber-300/50'
                        : 'border-transparent bg-muted/30',
                    !cell.inMonth ? 'invisible' : '',
                  ]"
                  :style="cell.tokens > 0 && !cell.isToday ? { backgroundColor: color(cell.tokens) } : {}"
                >
                  <span class="text-[10px] leading-none opacity-80">{{ cell.day }}</span>
                  <span class="mt-0.5 font-medium tabular-nums leading-none">{{ fmt(cell.tokens) }}</span>
                </div>
              </TooltipTrigger>
              <TooltipContent>
                {{ cell.date }} · {{ cell.tokens.toLocaleString() }} tokens
              </TooltipContent>
            </Tooltip>
          </template>
        </div>
        <div class="mt-3 flex items-center justify-end gap-3 text-[11px] text-muted-foreground">
          <span class="inline-flex items-center gap-1"><span class="inline-block h-3 w-3 rounded border bg-muted/30"></span>无消耗</span>
          <span class="inline-flex items-center gap-1"><span class="inline-block h-3 w-3 rounded" style="background-color: rgba(245, 158, 11, 0.6)"></span>有消耗</span>
          <span class="inline-flex items-center gap-1"><span class="inline-block h-3 w-3 rounded bg-foreground"></span>今天</span>
        </div>
      </template>
    </CardContent>
    </Card>
    </TooltipProvider>
  </template>
