<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { RiDatabase2Line, RiLoader4Line } from '@remixicon/vue'
import { useRequestLogs } from '@/composables/useRequestLogs'
import { formatBytes, formatDate } from '@/lib/format'
import type { RequestLogStats } from '@/lib/types'

/**
 * 转发日志（完整请求日志库）的保留策略设置卡。
 *
 * 两个阈值都允许为 0 = 不限制（与后端语义一致）：
 *   - 只存最近 N 天；
 *   - 日志库最大占用 M MB，超了按时间从旧到新删（FIFO）。
 * 生效时机是「写新日志时顺带清理一次」+ 服务启动时一次——不跑定时器，
 * 因为日志库只会在有请求时变大。
 */
const props = defineProps<{
  maxAgeDays: number
  maxSizeMb: number
  saving?: boolean
}>()
const emit = defineEmits<{
  'update:maxAgeDays': [value: number]
  'update:maxSizeMb': [value: number]
  save: []
}>()

const service = useRequestLogs()
const stats = ref<RequestLogStats | null>(null)

/** 读当前日志库占用，给用户一个「现在多大」的参照，方便判断阈值该设多少。 */
async function refresh() {
  try {
    stats.value = await service.stats()
  } catch {
    // 静默：读不到统计不影响设置本身，卡片只是少一行参考信息。
  }
}
onMounted(refresh)
defineExpose({ refresh })

/** 输入框与 props 双向绑定：空/非法一律归 0（= 不限），避免把 NaN 提交给后端。 */
function toNumber(value: string) {
  const n = Number.parseInt(value, 10)
  return Number.isFinite(n) && n > 0 ? n : 0
}

const ageInput = computed({
  get: () => props.maxAgeDays,
  set: (value: number) => emit('update:maxAgeDays', value),
})
const sizeInput = computed({
  get: () => props.maxSizeMb,
  set: (value: number) => emit('update:maxSizeMb', value),
})

/** 当前占用摘要：让用户直观看到「现在多大」，而不是只看配置数字。 */
const usageText = computed(() => {
  const s = stats.value
  if (!s) return '正在读取日志占用…'
  const range = s.oldest_started_at ? `，最早一条 ${formatDate(s.oldest_started_at)}` : ''
  return `当前占用 ${formatBytes(s.size)}，共 ${s.count} 条${range}`
})

/** 是否已配置限制：两个阈值都为 0 时提示用户日志会无限增长。 */
const unlimited = computed(() => !props.maxAgeDays && !props.maxSizeMb)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader>
      <CardTitle class="flex items-center gap-2 text-base">
        <RiDatabase2Line size="16" />
        日志保留
      </CardTitle>
      <CardDescription>
        控制「完整请求日志」的保留量。写新日志时自动清理旧日志，删除顺序为从旧到新。
      </CardDescription>
    </CardHeader>
    <CardContent class="space-y-4">
      <div class="flex items-center gap-2 rounded-md border bg-muted/40 px-3 py-2 text-sm">
        <span class="text-muted-foreground">{{ usageText }}</span>
        <span v-if="unlimited" class="ml-auto shrink-0 text-xs text-amber-600"
          >未设限制，日志会一直增长</span
        >
      </div>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="space-y-1">
          <Label for="log-max-age-days">只保留最近多少天</Label>
          <Input
            id="log-max-age-days"
            type="number"
            min="0"
            :model-value="ageInput"
            placeholder="0"
            @update:model-value="ageInput = toNumber($event)"
          />
          <p class="text-xs text-muted-foreground">填 0 表示不按时间清理。</p>
        </div>
        <div class="space-y-1">
          <Label for="log-max-size-mb">最大占用（MB）</Label>
          <Input
            id="log-max-size-mb"
            type="number"
            min="0"
            :model-value="sizeInput"
            placeholder="0"
            @update:model-value="sizeInput = toNumber($event)"
          />
          <p class="text-xs text-muted-foreground">
            填 0 表示不限容量。超限时删最旧的日志，直到降回该值以下。
          </p>
        </div>
      </div>
      <Button type="button" :disabled="saving" @click="emit('save')">
        <RiLoader4Line v-if="saving" class="animate-spin" size="16" />保存日志保留设置
      </Button>
    </CardContent>
  </Card>
</template>
