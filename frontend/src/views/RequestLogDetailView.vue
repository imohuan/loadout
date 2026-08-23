<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useRequestLogs } from '@/composables/useRequestLogs'
import type { RequestLogDetail } from '@/lib/types'
import { formatDate, formatDuration } from '@/lib/format'

const route = useRoute()
const { detail } = useRequestLogs()

const log = ref<RequestLogDetail | null>(null)
const error = ref('')
const loading = ref(true)

onMounted(async () => {
  try {
    log.value = await detail(route.params.id as string)
  } catch (e) {
    error.value = '该请求未记录完整日志，或已被清理。'
  } finally {
    loading.value = false
  }
})

function pretty(value: unknown): string {
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

const resultTone = computed(() => {
  const r = log.value?.result
  if (r === 'success') return 'bg-green-500/10 text-green-700 dark:text-green-300 border-green-500/30'
  if (r === 'failed' || r === 'stream_interrupted')
    return 'bg-red-500/10 text-red-700 dark:text-red-300 border-red-500/30'
  return 'bg-sky-500/10 text-sky-700 dark:text-sky-300 border-sky-500/30'
})
</script>

<template>
  <div class="space-y-4">
    <div class="flex items-center justify-between">
      <div>
        <router-link to="/route-logs" class="text-sm text-primary hover:underline">← 返回转发日志</router-link>
        <h2 class="mt-1 text-base font-medium">完整请求日志</h2>
      </div>
    </div>

    <Card v-if="loading" class="rounded-md">
      <CardContent class="py-8 text-center text-sm text-muted-foreground">加载中...</CardContent>
    </Card>

    <Card v-else-if="error || !log" class="rounded-md">
      <CardContent class="py-8 text-center text-sm text-muted-foreground">{{ error || '未找到记录' }}</CardContent>
    </Card>

    <template v-else>
      <Card class="rounded-md">
        <CardContent class="space-y-3">
          <div class="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
            <span class="inline-flex items-center gap-1.5">
              <span class="text-muted-foreground">结果</span>
              <span class="rounded border px-1.5 py-0.5 text-xs font-medium" :class="resultTone">{{ log.result }}</span>
            </span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">模型</span><span class="font-mono">{{ log.model }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">渠道</span><span class="font-mono">{{ log.channel || '-' }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">状态码</span><span class="font-mono">{{ log.http_status ?? '-' }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">流式</span>{{ log.stream ? '是' : '否' }}</span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">开始</span>{{ formatDate(log.started_at) }}</span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">耗时</span>{{ formatDuration(log.duration_ms) }}</span>
            <router-link v-if="log.request_id" :to="`/route-logs?request_id=${log.request_id}`"
              class="text-xs text-primary hover:underline">跳转对应转发日志</router-link>
          </div>
          <p class="break-all font-mono text-xs text-muted-foreground">UUID：{{ log.id }}　｜　Request ID：{{ log.request_id }}</p>
        </CardContent>
      </Card>

      <Card class="rounded-md">
        <CardContent class="space-y-2">
          <details class="group" open>
            <summary class="cursor-pointer select-none text-sm font-medium">请求（request_json）</summary>
            <pre class="mt-2 max-h-96 overflow-auto rounded border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed">{{ pretty(log.request_json) }}</pre>
          </details>
          <details class="group">
            <summary class="cursor-pointer select-none text-sm font-medium">响应（response_json）<template v-if="!log.response_json">
                <span class="ml-2 text-xs text-muted-foreground">（尚无响应：被拦截或中断）</span>
              </template></summary>
            <pre v-if="log.response_json" class="mt-2 max-h-96 overflow-auto rounded border border-border bg-muted/30 p-3 font-mono text-xs leading-relaxed">{{ pretty(log.response_json) }}</pre>
          </details>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
