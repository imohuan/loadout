<script setup lang="ts">
// McpInvocationsTab：MCP 工具调用日志（mcp_invocations 明细，查库）。
// 数据来自 admin-api：GET /api/mcp-invocations?page=&page_size=&kind=&tool=&server=&auth=
// 表格 + 展开行（输入/输出 JSON）+ 类型/认证/结果 Badge + 分页。
import { onMounted, reactive, ref } from 'vue'
import { RiArrowDownSLine, RiArrowRightSLine, RiRefreshLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import DataPagination from '@/components/DataPagination.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import { getMcpInvocations } from '@/lib/api'
import type { McpInvocation } from '@/lib/types'

const loading = ref(false)
const items = ref<McpInvocation[]>([])
const total = ref(0)
const expanded = ref<Set<number>>(new Set())

const filters = reactive<{
  kind: string
  tool: string
  auth: string
  page: number
  pageSize: number
}>({ kind: '', tool: '', auth: '', page: 1, pageSize: 20 })

async function load() {
  loading.value = true
  try {
    const page = await getMcpInvocations({
      page: filters.page,
      pageSize: filters.pageSize,
      kind: filters.kind || undefined,
      tool: filters.tool || undefined,
      auth: filters.auth || undefined,
    })
    items.value = page.items
    total.value = page.total
  } catch (e) {
    toast.error('加载工具调用日志失败', { description: (e as Error).message })
  } finally {
    loading.value = false
  }
}

function applyFilter() {
  filters.page = 1
  expanded.value.clear()
  load()
}
function toggleRow(id: number) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

// ---- 展示 helper ----
const KIND_LABEL: Record<string, string> = { single: '单 MCP', group: '分组', $smart: '聚合' }
const KIND_CLASS: Record<string, string> = {
  single: 'border-blue-500/20 bg-blue-500/15 text-blue-700 dark:text-blue-300',
  group: 'border-violet-500/20 bg-violet-500/15 text-violet-700 dark:text-violet-300',
  $smart: 'border-amber-500/20 bg-amber-500/15 text-amber-700 dark:text-amber-300',
}
const AUTH_LABEL: Record<string, string> = { session: 'session', 'mcp-key': 'mcp-key', public: 'public' }
const RESULT_LABEL: Record<string, string> = {
  success: '成功',
  error: '失败',
  not_found: '未找到',
  timeout: '超时',
  denied: '拒绝',
}
const RESULT_CLASS: Record<string, string> = {
  success: 'border-emerald-500/20 bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  error: 'border-red-500/20 bg-red-500/15 text-red-700 dark:text-red-300',
  not_found: 'border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300',
  timeout: 'border-orange-500/20 bg-orange-500/15 text-orange-700 dark:text-orange-300',
  denied: 'border-red-500/20 bg-red-500/15 text-red-700 dark:text-red-300',
}
function kindLabel(k: string): string {
  return KIND_LABEL[k] || k || '—'
}
function kindClass(k: string): string {
  return KIND_CLASS[k] || 'border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300'
}
function authLabel(a: string): string {
  return AUTH_LABEL[a] || a || '—'
}
function resultLabel(r: string): string {
  return RESULT_LABEL[r] || r || '—'
}
function resultClass(r: string): string {
  return RESULT_CLASS[r] || 'border-slate-500/20 bg-slate-500/15 text-slate-700 dark:text-slate-300'
}
function fmtTime(ts: string): string {
  if (!ts) return '—'
  const d = new Date(ts)
  return Number.isNaN(d.getTime()) ? ts : d.toLocaleString()
}
function fmtJSON(raw: string): string {
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
}

onMounted(load)
</script>

<template>
  <div class="space-y-4">
    <!-- 筛选工具栏 -->
    <div class="flex flex-wrap items-center gap-2">
      <!-- 注意：SelectItem value 不能为空串（shadcn-vue 保留空串作"清除选择"语义，会抛错），
           用 "all" 占位并在 update 时映射回空串表示不过滤。 -->
      <Select
        :model-value="filters.kind"
        @update:model-value="
          filters.kind = $event === 'all' ? '' : $event;
          applyFilter()
        "
      >
        <SelectTrigger class="h-9 w-[130px]">
          <SelectValue placeholder="类型" />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            <SelectItem value="all">全部</SelectItem>
            <SelectItem value="single">单 MCP</SelectItem>
            <SelectItem value="group">分组</SelectItem>
            <SelectItem value="$smart">聚合</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
      <Select
        :model-value="filters.auth"
        @update:model-value="
          filters.auth = $event === 'all' ? '' : $event;
          applyFilter()
        "
      >
        <SelectTrigger class="h-9 w-[130px]">
          <SelectValue placeholder="认证" />
        </SelectTrigger>
        <SelectContent>
          <SelectGroup>
            <SelectItem value="all">全部</SelectItem>
            <SelectItem value="session">session</SelectItem>
            <SelectItem value="mcp-key">mcp-key</SelectItem>
            <SelectItem value="public">public</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
      <Input
        v-model="filters.tool"
        class="h-9 w-[200px]"
        placeholder="工具名关键字…"
        @keydown.enter="applyFilter"
      />
      <Button variant="outline" class="h-9" :disabled="loading" @click="applyFilter">
        <RiRefreshLine size="16" :class="loading ? 'animate-spin' : ''" />刷新
      </Button>
    </div>

    <!-- 列表 -->
    <Card class="rounded-md">
      <CardContent class="p-0">
        <LoadingBlock v-if="loading && items.length === 0" />
        <div v-else-if="total === 0" class="py-16 text-center">
          <div class="mb-2 text-sm text-muted-foreground">暂无工具调用记录</div>
          <p class="text-xs text-muted-foreground/70">触发单 MCP / 分组 / 聚合调用后自动记录</p>
        </div>
        <Table v-else class="table-fixed w-full">
          <colgroup>
            <col class="w-8" />
            <col class="w-[150px]" />
            <col class="w-[90px]" />
            <col class="w-[110px]" />
            <col />
            <col class="w-[90px]" />
            <col class="w-[90px]" />
            <col class="w-[90px]" />
          </colgroup>
          <TableHeader>
            <TableRow>
              <TableHead></TableHead>
              <TableHead>时间</TableHead>
              <TableHead>类型</TableHead>
              <TableHead>名称</TableHead>
              <TableHead>工具</TableHead>
              <TableHead>认证</TableHead>
              <TableHead>耗时</TableHead>
              <TableHead>结果</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <template v-for="row in items" :key="row.id">
              <TableRow class="cursor-pointer" @click="toggleRow(row.id)">
                <TableCell class="py-2">
                  <RiArrowDownSLine
                    v-if="expanded.has(row.id)"
                    size="16"
                    class="text-muted-foreground"
                  />
                  <RiArrowRightSLine v-else size="16" class="text-muted-foreground" />
                </TableCell>
                <TableCell class="py-2">
                  <span class="whitespace-nowrap text-xs tabular-nums">{{ fmtTime(row.started_at) }}</span>
                </TableCell>
                <TableCell class="py-2">
                  <Badge variant="outline" :class="kindClass(row.aggregate_kind)">{{
                    kindLabel(row.aggregate_kind)
                  }}</Badge>
                </TableCell>
                <TableCell class="py-2">
                  <span class="text-xs text-muted-foreground">{{
                    row.aggregate_target || '—'
                  }}</span>
                </TableCell>
                <TableCell class="py-2">
                  <div class="min-w-0">
                    <div class="truncate text-sm font-medium">{{ row.tool_name }}</div>
                    <div v-if="row.server_name" class="truncate text-xs text-muted-foreground">
                      {{ row.server_name }}
                    </div>
                  </div>
                </TableCell>
                <TableCell class="py-2">
                  <span class="text-xs text-muted-foreground">{{ authLabel(row.auth_kind) }}</span>
                </TableCell>
                <TableCell class="py-2 text-right">
                  <span class="text-xs tabular-nums text-muted-foreground">{{ row.duration_ms }} ms</span>
                </TableCell>
                <TableCell class="py-2">
                  <Badge variant="outline" :class="resultClass(row.result)">{{
                    resultLabel(row.result)
                  }}</Badge>
                </TableCell>
              </TableRow>
              <!-- 展开行：输入/输出 JSON -->
              <TableRow v-if="expanded.has(row.id)" class="bg-muted/30">
                <TableCell class="py-2" :colspan="8">
                  <div class="grid gap-3 md:grid-cols-2">
                    <div class="min-w-0">
                      <div class="mb-1 text-xs font-medium text-muted-foreground">输入</div>
                      <pre
                        class="max-h-64 overflow-auto rounded-md border bg-background p-2 text-xs whitespace-pre-wrap break-all"
                        >{{ fmtJSON(row.input_json) || '—' }}</pre
                      >
                    </div>
                    <div class="min-w-0">
                      <div class="mb-1 text-xs font-medium text-muted-foreground">输出</div>
                      <pre
                        class="max-h-64 overflow-auto rounded-md border bg-background p-2 text-xs whitespace-pre-wrap break-all"
                        >{{ fmtJSON(row.output_json) || '—' }}</pre
                      >
                    </div>
                  </div>
                  <div v-if="row.error_message" class="mt-2">
                    <div class="mb-1 text-xs font-medium text-red-600 dark:text-red-400">错误</div>
                    <pre class="rounded-md border border-red-500/20 bg-red-500/5 p-2 text-xs whitespace-pre-wrap break-all text-red-700 dark:text-red-300">{{
                      row.error_message
                    }}</pre>
                  </div>
                </TableCell>
              </TableRow>
            </template>
          </TableBody>
        </Table>
      </CardContent>
    </Card>

    <!-- 分页 -->
    <DataPagination
      v-if="total > 0"
      v-model:page="filters.page"
      v-model:page-size="filters.pageSize"
      :total="total"
      :disabled="loading"
      @update:page="load"
      @update:page-size="load"
    />
  </div>
</template>
