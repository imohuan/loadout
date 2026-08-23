<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { useRequestLogs } from '@/composables/useRequestLogs'
import type { RequestLogDetail } from '@/lib/types'
import { formatDate, formatDuration } from '@/lib/format'
import { RiArrowLeftLine, RiArrowRightUpLine, RiErrorWarningLine, RiArrowRightSLine } from '@remixicon/vue'
import AxJsonViewer from '@/components/ui/AxJsonViewer.vue'
import EmptyState from '@/components/EmptyState.vue'

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

// result 徽标配色与 RouteLogTable 保持一致（emerald=成功 / red=失败 / amber=中断 / blue=运行中）
const RESULT_TONES: Record<string, string> = {
  success: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300 border-emerald-500/20',
  failed: 'bg-red-500/15 text-red-700 dark:text-red-300 border-red-500/20',
  running: 'bg-blue-500/15 text-blue-700 dark:text-blue-300 border-blue-500/20',
  skipped: 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20',
  stream_interrupted: 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20',
}
const RESULT_LABELS: Record<string, string> = {
  success: '已成功',
  failed: '失败',
  running: '进行中',
  skipped: '已跳过',
  stream_interrupted: '已中断',
}
function resultTone(result?: string) {
  return RESULT_TONES[result || ''] || ''
}
function resultLabel(result?: string) {
  return RESULT_LABELS[result || ''] || result || '未知'
}
</script>

<template>
  <div class="space-y-4">
    <!-- 顶部：左 返回链接 / 右 标题 -->
    <div class="flex items-center justify-between">
      <Button variant="ghost" size="sm" class="-ml-2 gap-1 px-2 text-muted-foreground" as-child>
        <router-link to="/route-logs">
          <RiArrowLeftLine size="16" />
          返回转发日志
        </router-link>
      </Button>
      <h2 class="text-base font-medium">完整请求日志</h2>
    </div>

    <!-- 加载骨架 -->
    <Card v-if="loading" class="rounded-md">
      <CardContent class="space-y-3">
        <div class="flex flex-wrap items-center gap-x-6 gap-y-2">
          <Skeleton class="h-6 w-16" />
          <Skeleton class="h-6 w-36" />
          <Skeleton class="h-6 w-28" />
          <Skeleton class="h-6 w-24" />
          <Skeleton class="h-6 w-14" />
          <Skeleton class="h-6 w-40" />
          <Skeleton class="h-6 w-20" />
        </div>
        <Skeleton class="h-4 w-72" />
        <Separator />
        <Skeleton class="h-40 w-full" />
      </CardContent>
    </Card>

    <!-- 加载失败 -->
    <Alert v-else-if="error" variant="destructive" class="rounded-md">
      <RiErrorWarningLine size="16" />
      <AlertTitle>加载失败</AlertTitle>
      <AlertDescription>{{ error }}</AlertDescription>
    </Alert>

    <!-- 未找到 -->
    <EmptyState v-else-if="!log" title="未找到记录" description="该请求未记录完整日志，或已被清理。" />

    <template v-else>
      <!-- 请求概要 -->
      <Card class="rounded-md">
        <CardHeader>
          <CardTitle class="text-base">请求概要</CardTitle>
        </CardHeader>
        <CardContent class="space-y-3">
          <div class="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
            <span class="inline-flex items-center gap-1.5">
              <span class="text-muted-foreground">结果</span>
              <Badge :class="resultTone(log.result)">{{ resultLabel(log.result) }}</Badge>
            </span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">模型</span><span
                class="font-mono">{{ log.model }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">渠道</span><span
                class="font-mono">{{ log.channel || '-' }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">状态码</span><span
                class="font-mono">{{ log.http_status ?? '-' }}</span></span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">流式</span>{{ log.stream ?
              '是' : '否' }}</span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">开始</span>{{
              formatDate(log.started_at) }}</span>
            <span class="inline-flex items-center gap-1.5"><span class="text-muted-foreground">耗时</span>{{
              formatDuration(log.duration_ms) }}</span>
          </div>
          <Separator />
          <div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">
            <p class="break-all font-mono text-xs text-muted-foreground">UUID：{{ log.id }}　｜　Request ID：{{
              log.request_id }}</p>
            <Button v-if="log.request_id" variant="link" size="sm" class="h-auto gap-1 p-0 text-xs" as-child>
              <router-link :to="`/route-logs?request_id=${log.request_id}`">
                跳转对应转发日志
                <RiArrowRightUpLine size="14" />
              </router-link>
            </Button>
          </div>
        </CardContent>
      </Card>

      <!-- 请求 / 响应报文 -->
      <Card class="rounded-md">
        <CardHeader>
          <CardTitle class="text-base">请求 / 响应报文</CardTitle>
        </CardHeader>
        <CardContent>
          <Accordion type="multiple" :default-value="['request']" class="w-full">
            <AccordionItem value="request">
              <AccordionTrigger>
                <span class="inline-flex items-center">
                  <RiArrowRightSLine
                    class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90" />
                  请求（request_json）
                </span>
                <template #icon><span class="hidden" /></template>
              </AccordionTrigger>
              <AccordionContent>
                <div class="mt-2 max-h-96 overflow-auto rounded border border-border bg-muted/30 p-3">
                  <AxJsonViewer :data="log.request_json" :expand-level="2" wrap-enabled />
                </div>
              </AccordionContent>
            </AccordionItem>
            <AccordionItem value="response">
              <AccordionTrigger>
                <span class="inline-flex items-center">
                  <RiArrowRightSLine
                    class="mr-2 size-4 shrink-0 text-muted-foreground transition-transform duration-200 group-aria-expanded/accordion-trigger:rotate-90" />
                  响应（response_json）
                  <template v-if="!log.response_json">
                    <span class="ml-2 text-xs font-normal text-muted-foreground">（尚无响应：被拦截或中断）</span>
                  </template>
                </span>
                <template #icon><span class="hidden" /></template>
              </AccordionTrigger>
              <AccordionContent>
                <div v-if="log.response_json"
                  class="mt-2 max-h-168 overflow-auto rounded border border-border bg-muted/30 p-3">
                  <AxJsonViewer :data="log.response_json" :expand-level="2" wrap-enabled />
                </div>
                <p v-else
                  class="mt-2 rounded border border-dashed border-border py-6 text-center text-sm text-muted-foreground">
                  尚无响应：请求被拦截或流式中断。
                </p>
              </AccordionContent>
            </AccordionItem>
          </Accordion>
        </CardContent>
      </Card>
    </template>
  </div>
</template>
