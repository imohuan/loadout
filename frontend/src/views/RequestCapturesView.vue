<script setup lang="ts">
import { computed, onMounted, onScopeDispose, ref, watch } from 'vue'
import { RiDeleteBinLine, RiLoader4Line, RiRefreshLine, RiScanLine } from '@remixicon/vue'
import { useRequestCaptures } from '@/composables/useRequestCaptures'
import type { CaptureFilters } from '@/composables/useRequestCaptures'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import { formatBytes, formatDate } from '@/lib/format'
import type { CaptureItem } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import DataPagination from '@/components/DataPagination.vue'
import CaptureFiltersForm from '@/components/request-captures/CaptureFilters.vue'
import CaptureDetailDialog from '@/components/request-captures/CaptureDetailDialog.vue'
import AxTable from '@/components/ui/AxTable.vue'

/**
 * 请求抓包捕获面板。
 *
 * 与「转发日志」页的关系：两个页面都是请求调试视图，结构一致（筛选 → 表格 → 分页），
 * 区别在数据来源——转发日志是路由决策记录（脱敏），这里是进中转那一刻的原始报文
 * （含 Authorization 原文与图片 base64），并且可以一键生成 Node 复现脚本。
 *
 * 自动刷新：抓包文件是「来一个请求多一个」，所以第 1 页给一个 3 秒定时刷新，
 * 翻页后停表，与转发日志页的行为保持一致。
 */
const service = useRequestCaptures()
const filters = ref<CaptureFilters>({})
const page = ref(1)
const pageSize = ref(20)
const {
  data: pageData,
  loading,
  refreshing,
  refresh,
} = useListLoader(() => service.list(filters.value, { page: page.value, pageSize: pageSize.value }))

const items = computed(() => pageData.value?.items ?? [])
const total = computed(() => pageData.value?.total ?? 0)
const dirSize = computed(() => pageData.value?.size ?? 0)
const retentionText = computed(() => {
  const data = pageData.value
  if (!data) return ''
  const parts: string[] = []
  if (data.max_age_days > 0) parts.push(`只留最近 ${data.max_age_days} 天`)
  if (data.max_size_mb > 0) parts.push(`最多 ${data.max_size_mb} MB`)
  return parts.length ? `保留策略：${parts.join('、')}` : '保留策略：未设置（抓包会一直保留）'
})

const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()

/** 详情弹窗：打开的是某一条捕获的原始报文。 */
const detailID = ref('')
const detailOpen = ref(false)
function openDetail(item: CaptureItem) {
  detailID.value = item.id
  detailOpen.value = true
}
function onDetailDeleted() {
  detailOpen.value = false
  void refresh()
}

async function apply(next: CaptureFilters) {
  await run('apply', async () => {
    filters.value = next
    page.value = 1
    await refresh()
  })
}

function onPageChange(next: number) {
  page.value = next
  void refresh()
}
function onPageSizeChange(next: number) {
  pageSize.value = next
  page.value = 1
  void refresh()
}

async function remove(item: CaptureItem) {
  const ok = await confirmDialog({
    title: '删除这条抓包？',
    description: `将删除 Request ID 为 ${item.request_id} 的捕获文件，删除后无法恢复。`,
    confirmText: '删除',
    destructive: true,
  })
  if (!ok) return
  await run('delete', async () => {
    await service.remove(item.id)
    await refresh()
  }, '已删除')
}

async function clearAll() {
  const ok = await confirmDialog({
    title: '清空全部抓包？',
    description: `将删除 ${total.value} 条捕获文件（占用 ${formatBytes(dirSize.value)}），删除后无法恢复。`,
    confirmText: '清空',
    destructive: true,
  })
  if (!ok) return
  await run('clear', async () => {
    await service.clear()
    page.value = 1
    await refresh()
  }, '抓包已清空')
}

// ===== 自动刷新（只在第 1 页跑）=====
const AUTO_REFRESH_INTERVAL = 3_000
const autoRefresh = ref(true)
let timer: ReturnType<typeof setInterval> | undefined

function stopTimer() {
  if (timer) {
    clearInterval(timer)
    timer = undefined
  }
}
function syncTimer() {
  stopTimer()
  if (!autoRefresh.value || page.value !== 1) return
  timer = setInterval(() => {
    void refresh({ silentError: true })
  }, AUTO_REFRESH_INTERVAL)
}
watch([page, autoRefresh], syncTimer, { flush: 'post' })
onMounted(syncTimer)
onScopeDispose(stopTimer)
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="请求抓包捕获"
      description="在请求进入中转的最前端，把完整原始报文（含 Authorization 与图片 base64）打包落盘，用于逐字节复现问题。"
    >
      <template #actions>
        <Button
          variant="outline"
          :disabled="loading || refreshing || isPending('refresh')"
          @click="refresh()"
        >
          <RiLoader4Line v-if="isPending('refresh')" class="animate-spin" size="16" />
          <RiRefreshLine v-else :class="{ 'animate-spin': refreshing }" size="16" />
          刷新
        </Button>
        <Button variant="destructive" :disabled="isPending('clear') || !total" @click="clearAll">
          <RiLoader4Line v-if="isPending('clear')" class="animate-spin" size="16" />
          <RiDeleteBinLine v-else size="16" />
          清空抓包
        </Button>
      </template>
    </PageHeader>

    <CaptureFiltersForm :is-pending="isPending" @apply="apply" @reset="apply({})" />

    <LoadingBlock v-if="loading" />
    <Card v-else class="rounded-md pb-0!">
      <!-- 卡片头与「转发日志」页保持同一结构：标题 + 一句话说明，
           右侧放统计与自动刷新开关。 -->
      <CardHeader class="flex flex-row items-start justify-between gap-3 space-y-0">
        <div>
          <CardTitle class="text-base">请求记录</CardTitle>
          <CardDescription>点开一条记录可查看完整原始报文，并生成复现脚本。</CardDescription>
        </div>
        <div class="flex shrink-0 flex-wrap items-center justify-end gap-3">
          <span class="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
            <RiScanLine size="14" />
            共 <span class="font-medium tabular-nums text-foreground">{{ total }}</span> 条，
            占用 <span class="font-medium tabular-nums text-foreground">{{ formatBytes(dirSize) }}</span>
            <span class="text-xs">· {{ retentionText }}</span>
          </span>
          <label class="flex cursor-pointer items-center gap-2 text-sm whitespace-nowrap text-muted-foreground">
            <span>自动刷新</span>
            <Switch v-model="autoRefresh" aria-label="自动刷新" />
            <span v-if="autoRefresh && page !== 1" class="text-xs">（第 1 页才自动刷新）</span>
          </label>
        </div>
      </CardHeader>
      <CardContent class="p-0">
        <div v-if="items.length" class="overflow-x-auto">
          <AxTable>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>抓包时间</TableHead>
                  <TableHead>请求模型</TableHead>
                  <TableHead>输出类型</TableHead>
                  <TableHead>请求 ID</TableHead>
                  <TableHead>路径</TableHead>
                  <TableHead>大小</TableHead>
                  <TableHead class="w-20">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <TableRow
                  v-for="item in items"
                  :key="item.id"
                  class="cursor-pointer"
                  @click="openDetail(item)"
                >
                  <TableCell class="whitespace-nowrap text-xs text-muted-foreground">
                    {{ formatDate(item.captured_at) }}
                  </TableCell>
                  <TableCell>
                    <p class="font-mono text-xs font-medium">{{ item.model || '-' }}</p>
                  </TableCell>
                  <!-- 输出类型：流 / 非流，独立一列（原来挤在请求模型单元格里上下排列） -->
                  <TableCell>
                    <span
                      class="inline-flex items-center rounded border px-1.5 py-0.5 text-[10px] font-medium"
                      :class="
                        item.stream
                          ? 'border-sky-500/30 bg-sky-500/10 text-sky-700 dark:text-sky-300'
                          : 'border-border bg-muted text-muted-foreground'
                      "
                    >
                      {{ item.stream ? '流式' : '非流式' }}
                    </span>
                  </TableCell>
                  <TableCell>
                    <p class="max-w-64 truncate font-mono text-[11px] text-muted-foreground">
                      {{ item.request_id }}
                    </p>
                  </TableCell>
                  <TableCell class="whitespace-nowrap font-mono text-xs text-muted-foreground">
                    {{ item.method }} {{ item.path }}
                  </TableCell>
                  <TableCell class="whitespace-nowrap tabular-nums text-xs">
                    {{ formatBytes(item.size) }}
                  </TableCell>
                  <TableCell>
                    <Button
                      variant="ghost"
                      size="sm"
                      class="h-7 px-2 text-xs text-destructive"
                      :disabled="isPending('delete')"
                      @click.stop="remove(item)"
                    >
                      删除
                    </Button>
                  </TableCell>
                </TableRow>
              </TableBody>
            </Table>
          </AxTable>
        </div>
        <EmptyState
          v-else-if="!total"
          title="还没有抓包记录"
          description="在「能力路由」里给目标模型添加 request_capture 能力（路由方式选附加代理），之后的请求就会在这里出现原始报文。"
        />
        <div v-if="total > 0" class="border-t border-border p-3">
          <DataPagination
            v-model:page="page"
            v-model:page-size="pageSize"
            :total="total"
            @update:page="onPageChange"
            @update:page-size="onPageSizeChange"
          />
        </div>
      </CardContent>
    </Card>

    <CaptureDetailDialog
      v-if="detailOpen"
      v-model:open="detailOpen"
      :capture-id="detailID"
      @deleted="onDetailDeleted"
    />
  </div>
</template>

