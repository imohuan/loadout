<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  RiCheckLine,
  RiClipboardLine,
  RiDeleteBinLine,
  RiDownloadLine,
  RiTerminalBoxLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useRequestCaptures } from '@/composables/useRequestCaptures'
import { useConfirm } from '@/composables/useConfirm'
import { formatBytes, formatDate } from '@/lib/format'
import type { CaptureDetail } from '@/lib/types'
import AxJsonViewer from '@/components/ui/AxJsonViewer.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'

/**
 * 抓包详情弹窗：展示进入中转那一刻的原始报文（未脱敏）。
 *
 * 三个按钮的用途：
 *   - 复制完整 JSON：把 envelope 原文（含 headers、body）放剪贴板，方便对比；
 *   - 填入测试：后端生成 Node.js 复现脚本并返回路径，直接运行该脚本即可重放；
 *   - 下载 .cap.json.gz：把压缩文件本身取下来，可跨机器保留证据。
 */
const props = defineProps<{ captureId: string; open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean]; deleted: [] }>()

const service = useRequestCaptures()
const { confirmDialog } = useConfirm()

const detail = ref<CaptureDetail | null>(null)
const loading = ref(true)
const error = ref('')
const copied = ref(false)
const replayLoading = ref(false)
const replayPath = ref('')
const deleting = ref(false)

onMounted(async () => {
  try {
    detail.value = await service.detail(props.captureId)
  } catch (e) {
    error.value = e instanceof Error ? e.message : '加载捕获失败'
  } finally {
    loading.value = false
  }
})

/** body 是 JSON 时给出缩进版，方便直接读；否则按原文展示。 */
const prettyBody = computed(() => {
  const raw = detail.value?.body ?? ''
  if (!raw) return ''
  try {
    return JSON.stringify(JSON.parse(raw), null, 2)
  } catch {
    return raw
  }
})

const bodyLabel = computed(() => {
  const raw = detail.value?.body ?? ''
  if (!raw) return '（空请求体）'
  if (raw.startsWith('hex:')) return '（二进制请求体，下面按 hex 展示）'
  return raw.length.toLocaleString() + ' 字符'
})

/** 单个 header 拼成 "k: v1, v2"，多值头全部保留。 */
const headerRows = computed(() => {
  const h = detail.value?.headers ?? {}
  return Object.keys(h)
    .sort((a, b) => a.localeCompare(b))
    .map((key) => ({ key, value: h[key].join(', ') }))
})

async function copyJson() {
  if (!detail.value) return
  try {
    await navigator.clipboard.writeText(JSON.stringify(detail.value, null, 2))
    copied.value = true
    toast.success('已复制完整 JSON（含未脱敏的 headers 与 body）')
    setTimeout(() => (copied.value = false), 2000)
  } catch {
    toast.error('复制失败，请检查浏览器剪贴板权限')
  }
}

async function makeReplayScript() {
  if (!detail.value) return
  replayLoading.value = true
  try {
    const res = await service.replayScript(detail.value.id)
    replayPath.value = res.path
    toast.success('复现脚本已生成')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '生成复现脚本失败')
  } finally {
    replayLoading.value = false
  }
}

async function copyReplayPath() {
  if (!replayPath.value) return
  try {
    await navigator.clipboard.writeText(replayPath.value)
    toast.success('已复制脚本路径')
  } catch {
    toast.error('复制失败，请检查浏览器剪贴板权限')
  }
}

/** 打开原始捕获文件（浏览器直接下载 gzip，不经过任何改写）。 */
function download() {
  if (!detail.value) return
  window.open(
    '/api/request-captures/' + encodeURIComponent(detail.value.id) + '/download',
    '_blank',
  )
}

async function remove() {
  if (!detail.value) return
  const ok = await confirmDialog({
    title: '删除这条抓包？',
    description:
      '将删除 Request ID 为 ' + detail.value.request_id + ' 的捕获文件，删除后无法恢复。',
    confirmText: '删除',
    destructive: true,
  })
  if (!ok) return
  deleting.value = true
  try {
    await service.remove(detail.value.id)
    emit('update:open', false)
    emit('deleted')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '删除失败')
  } finally {
    deleting.value = false
  }
}

function close() {
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="open" @update:open="(o: boolean) => (o ? null : close())">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-4xl!">
      <DialogHeader>
        <DialogTitle class="text-base">抓包详情</DialogTitle>
        <DialogDescription class="text-xs">
          请求进入中转前的最原始报文，未做任何脱敏或改写。
        </DialogDescription>
      </DialogHeader>

      <LoadingBlock v-if="loading" />
      <Alert v-else-if="error" variant="destructive">
        <AlertTitle>加载失败</AlertTitle>
        <AlertDescription>{{ error }}</AlertDescription>
      </Alert>

      <template v-else-if="detail">
        <div class="flex flex-wrap items-center gap-x-5 gap-y-2 text-sm">
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">模型</span>
            <Badge class="font-mono">{{ detail.model || '-' }}</Badge>
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">方法</span>
            <Badge class="font-mono">{{ detail.method || '-' }}</Badge>
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">路径</span>
            <Badge class="font-mono">{{ detail.path || '-' }}</Badge>
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">流式</span>
            <Badge>{{ detail.stream ? '是' : '否' }}</Badge>
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">大小</span>
            <Badge class="tabular-nums">{{ formatBytes(detail.size) }}</Badge>
          </span>
          <span class="inline-flex items-center gap-1.5">
            <span class="text-muted-foreground">抓包时间</span>
            <Badge class="font-mono">{{ formatDate(detail.captured_at) }}</Badge>
          </span>
        </div>

        <p class="break-all font-mono text-xs text-muted-foreground">
          Request ID：{{ detail.request_id }}　｜　UUID：{{ detail.id }}
        </p>

        <div class="flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" class="gap-1.5" @click="copyJson">
            <component :is="copied ? RiCheckLine : RiClipboardLine" size="14" />
            {{ copied ? '已复制' : '复制完整 JSON' }}
          </Button>
          <Button
            variant="outline"
            size="sm"
            class="gap-1.5"
            :disabled="replayLoading"
            @click="makeReplayScript"
          >
            <RiTerminalBoxLine size="14" />
            {{ replayLoading ? '生成中…' : '填入测试' }}
          </Button>
          <Button variant="outline" size="sm" class="gap-1.5" @click="download">
            <RiDownloadLine size="14" />
            下载 .cap.json.gz
          </Button>
          <Button
            variant="destructive"
            size="sm"
            class="ml-auto gap-1.5"
            :disabled="deleting"
            @click="remove"
          >
            <RiDeleteBinLine size="14" />
            删除
          </Button>
        </div>

        <div v-if="replayPath" class="space-y-1 rounded border border-border bg-muted/30 p-3">
          <p class="text-xs text-muted-foreground">复现脚本已生成，运行方式：</p>
          <p class="break-all font-mono text-xs">{{ replayPath }}</p>
          <div class="flex flex-wrap items-center gap-2">
            <Button variant="outline" size="sm" class="h-7 text-xs" @click="copyReplayPath">
              复制路径
            </Button>
            <code class="break-all font-mono text-[11px] text-muted-foreground">
              LOADOUT_API_KEY=sk-xxx node "{{ replayPath }}"
            </code>
          </div>
        </div>

        <Accordion type="multiple" :default-value="['headers', 'body']" class="w-full">
          <AccordionItem value="headers">
            <AccordionTrigger>请求头（{{ headerRows.length }} 项，未脱敏）</AccordionTrigger>
            <AccordionContent>
              <div class="mt-2 max-h-64 overflow-auto rounded border border-border bg-muted/30 p-3">
                <table class="w-full font-mono text-xs">
                  <tbody>
                    <tr v-for="row in headerRows" :key="row.key" class="align-top">
                      <td class="w-52 pr-3 text-muted-foreground">{{ row.key }}</td>
                      <td class="break-all">{{ row.value }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </AccordionContent>
          </AccordionItem>
          <AccordionItem value="body">
            <AccordionTrigger>请求体（{{ bodyLabel }}）</AccordionTrigger>
            <AccordionContent>
              <div class="mt-2 max-h-96 overflow-auto rounded border border-border bg-muted/30 p-3">
                <AxJsonViewer :data="prettyBody" :expand-level="2" wrap-enabled />
              </div>
            </AccordionContent>
          </AccordionItem>
        </Accordion>
      </template>
    </DialogContent>
  </Dialog>
</template>

