<script setup lang="ts">
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import {
  RiAddLine,
  RiAttachment2,
  RiCloseLine,
  RiDeleteBinLine,
  RiImageAddLine,
  RiPlayLine,
  RiRefreshLine,
  RiStopLine,
} from '@remixicon/vue'
import { useChannels } from '@/composables/useChannels'
import { useListLoader } from '@/composables/useListLoader'
import { useModelTest } from '@/composables/useModelTest'
import { useRouteLogs } from '@/composables/useRouteLogs'
import type { RouteLog } from '@/lib/types'
import { loadTestRouteLogs, saveTestRouteLog, clearTestRouteLogs } from '@/lib/routeLogCache'
import PageHeader from '@/components/PageHeader.vue'
import RouteLogTable from '@/components/route-logs/RouteLogTable.vue'

type MessageRole = 'system' | 'user' | 'assistant'
type TestMessage = { id: number; role: MessageRole; content: string }
type Attachment = { id: number; name: string; kind: 'image' | 'file'; preview?: string }

const channelService = useChannels()
const modelTest = useModelTest()
const routeLogs = useRouteLogs()
const { data: channels, loading: channelsLoading } = useListLoader(channelService.list)
const config = reactive({ channelId: '', baseUrl: '', apiKey: '', model: '' })

// 模型后缀模式：决定上游请求路径的标志符（chat/completions、messages 等）。
// 不同厂商标志符不同，部分 base URL 带 /v1、部分不带，因此这里只配置 /v1 之后的部分。
// 目前仅作占位，后续构造上游请求路径时使用。
const suffixMode = ref<'gpt' | 'claude' | 'chat'>('chat')
function setSuffixMode(value: string) {
  if (value === 'gpt' || value === 'claude' || value === 'chat') suffixMode.value = value
}

// 只放真实消息与用户主动添加的可编辑行；空消息不会发送到接口。
// 输入区由右侧「用户输入」卡片承担（绑定 draft）。
const messages = ref<TestMessage[]>([])
const attachments = ref<Attachment[]>([])
const previewAttachment = ref<Attachment | null>(null)
const draft = ref('')
const models = ref<string[]>([])
const modelsLoading = ref(false)
const modelsError = ref('')
const fetchError = ref('')
const assistantReply = ref('')
const streaming = ref(false)
const abortController = ref<AbortController | null>(null)
const fileInput = ref<HTMLInputElement>()
const nextMessageId = ref(2)
const nextAttachmentId = ref(1)
// 只保存本页发起的「测试请求」日志，不加载全量转发日志：
// 启动时从 localStorage 恢复，发送结束后用 detail 接口拉后端真实记录覆盖。
// 页面刷新后依然只显示测试请求，不会混入其他请求的日志。
const logs = ref<RouteLog[]>(loadTestRouteLogs() || [])
const loadingDetail = ref('')
// 已展开请求的详情缓存（attempts / error_message），避免重复请求且折叠再展开瞬时显示。
const detailsMap = reactive(new Map<string, RouteLog>())

const displayLogs = computed(() =>
  (logs.value || []).map((log) => {
    const detail = detailsMap.get(log.request_id)
    if (!detail) return log
    return {
      ...log,
      attempts: detail.attempts,
      error_message: detail.error_message ?? log.error_message,
    }
  }),
)

const filteredModels = computed(() => {
  const keyword = config.model.trim().toLowerCase()
  return models.value.filter((model) => !keyword || model.toLowerCase().includes(keyword))
})

// 目标来源：选中渠道时始终带 channel_id（后端以渠道记录为准解析 base_url 与 key）；
// 手动输入的 key 优先于渠道存储的 key；suffix_mode 决定上游路径后缀。未选渠道时用临时配置。
function buildTarget() {
  const target: Record<string, string> = { suffix_mode: suffixMode.value }
  if (config.channelId) {
    target.channel_id = config.channelId
    if (config.apiKey.trim()) target.api_key = config.apiKey.trim()
  } else {
    target.base_url = config.baseUrl
    target.api_key = config.apiKey
  }
  return target
}

function importChannel(id: string) {
  config.channelId = id
  const channel = channels.value?.find((item) => item.id === id)
  if (!channel) return
  config.baseUrl = channel.base_url
  config.apiKey = ''
  models.value = channel.models || []
  modelsError.value = channel.models_error || ''
}

async function fetchModels() {
  modelsError.value = ''
  if (!config.baseUrl.trim() && !config.channelId) {
    modelsError.value = '请先选择渠道或输入 Base URL'
    return
  }
  modelsLoading.value = true
  try {
    const result = await modelTest.listModels(buildTarget())
    if (result.error) {
      models.value = []
      modelsError.value = result.error
      return
    }
    models.value = result.models || []
    if (!models.value.length) modelsError.value = '接口没有返回可用模型'
  } catch (error) {
    modelsError.value = error instanceof Error ? error.message : '获取模型失败'
  } finally {
    modelsLoading.value = false
  }
}

function addMessage() {
  messages.value.push({ id: nextMessageId.value++, role: 'user', content: '' })
}
function removeMessage(index: number) {
  if (messages.value.length > 1) messages.value.splice(index, 1)
}
function chooseModel(model: string) {
  config.model = model
}
function openFilePicker() {
  fileInput.value?.click()
}
function revokePreview(attachment: Attachment) {
  if (attachment.preview) URL.revokeObjectURL(attachment.preview)
}

function removeAttachment(id: number) {
  const attachment = attachments.value.find((item) => item.id === id)
  if (previewAttachment.value?.id === id) previewAttachment.value = null
  if (attachment) revokePreview(attachment)
  attachments.value = attachments.value.filter((item) => item.id !== id)
}

function clearAttachments() {
  attachments.value.forEach(revokePreview)
  attachments.value = []
}

function clearDraft() {
  draft.value = ''
  fetchError.value = ''
  clearAttachments()
}

function openImagePreview(attachment: Attachment) {
  if (attachment.kind === 'image' && attachment.preview) previewAttachment.value = attachment
}

function attachmentLabel(name: string) {
  if (name.length <= 30) return name
  return `${name.slice(0, 14)}...${name.slice(-13)}`
}

function addFiles(files: FileList | File[]) {
  Array.from(files).forEach((file) => {
    const image = file.type.startsWith('image/')
    attachments.value.push({
      id: nextAttachmentId.value++,
      name: file.name,
      kind: image ? 'image' : 'file',
      preview: image ? URL.createObjectURL(file) : undefined,
    })
  })
}

function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement
  if (input.files) addFiles(input.files)
  input.value = ''
}

function onPaste(event: ClipboardEvent) {
  const images = Array.from(event.clipboardData?.files || []).filter((file) =>
    file.type.startsWith('image/'),
  )
  if (!images.length) return
  event.preventDefault()
  addFiles(images)
}

function onDrop(event: DragEvent) {
  event.preventDefault()
  if (event.dataTransfer?.files) addFiles(event.dataTransfer.files)
}

function blobUrlToDataUrl(url: string): Promise<string> {
  return fetch(url)
    .then((response) => response.blob())
    .then(
      (blob) =>
        new Promise<string>((resolve, reject) => {
          const reader = new FileReader()
          reader.onload = () => resolve(reader.result as string)
          reader.onerror = () => reject(reader.error)
          reader.readAsDataURL(blob)
        }),
    )
}

async function send() {
  fetchError.value = ''
  if (!config.baseUrl.trim() && !config.channelId) {
    fetchError.value = '请先选择渠道或输入 Base URL'
    return
  }
  if (!config.model.trim()) {
    fetchError.value = '请先填写模型名'
    return
  }
  if (!draft.value.trim() && !attachments.value.length) {
    fetchError.value = '请输入消息或添加资源'
    return
  }

  // 图片附件转 base64 data URL（后台代理不支持前端 blob URL）。
  const imageParts: string[] = []
  for (const attachment of attachments.value) {
    if (attachment.kind !== 'image' || !attachment.preview) continue
    try {
      imageParts.push(await blobUrlToDataUrl(attachment.preview))
    } catch {
      // 忽略转换失败的附件
    }
  }

  const text = draft.value.trim() || (attachments.value.length ? '[附带资源]' : '')

  // 用局部变量构造本次请求的 messages，不写入左侧 Messages 卡片列表，
  // 避免发送一次左侧就多出一条记录。
  // 先取左侧已编辑的消息行（过滤空行），再把本次输入作为最后一条 user 消息。
  const requestMessages: Array<{ role: string; content: unknown }> = messages.value
    .filter((m) => m.content.trim())
    .map((m) => ({ role: m.role, content: m.content }))
  const currentMessage: { role: string; content: unknown } = {
    role: 'user',
    content: text || '[附带资源]',
  }
  if (imageParts.length) {
    const parts: Array<{ type: string; text?: string; image_url?: { url: string } }> = [
      { type: 'text', text },
    ]
    for (const url of imageParts) parts.push({ type: 'image_url', image_url: { url } })
    currentMessage.content = parts
  }
  requestMessages.push(currentMessage)

  // 发送后保留草稿与附件，便于连续调试；需要清空时点「清空」按钮。

  const startedAt = new Date()
  assistantReply.value = ''
  streaming.value = true
  const controller = new AbortController()
  abortController.value = controller

  const entry: RouteLog = {
    request_id: `test-${Date.now()}`,
    requested_model: config.model,
    final_model: config.model,
    final_channel_id: config.channelId || undefined,
    started_at: startedAt.toISOString(),
    result: 'running',
    attempts: [
      {
        step_no: 1,
        action: '首次尝试',
        result: 'running',
        model: config.model,
        channel_id: config.channelId || undefined,
        started_at: startedAt.toISOString(),
      },
    ],
  }
  logs.value.unshift(entry)

  const channelId = config.channelId || undefined
  try {
    const result = await modelTest.chat(
      buildTarget(),
      config.model,
      requestMessages,
      (delta) => {
        assistantReply.value += delta
      },
      controller.signal,
    )
    entry.request_id = result.request_id || entry.request_id
    entry.result = 'success'
    entry.duration_ms = Date.now() - startedAt.getTime()
    entry.attempts = [
      {
        step_no: 1,
        action: 'test',
        result: 'success',
        model: config.model,
        channel_id: channelId,
        started_at: startedAt.toISOString(),
        duration_ms: entry.duration_ms,
      },
    ]
  } catch (error) {
    const aborted = error instanceof DOMException && error.name === 'AbortError'
    entry.result = 'failed'
    entry.duration_ms = Date.now() - startedAt.getTime()
    entry.error_message = aborted ? '已手动停止' : error instanceof Error ? error.message : '请求失败'
    entry.attempts = [
      {
        step_no: 1,
        action: 'test',
        result: 'failed',
        model: config.model,
        channel_id: channelId,
        started_at: startedAt.toISOString(),
        duration_ms: entry.duration_ms,
        error_message: entry.error_message,
      },
    ]
    if (!aborted) fetchError.value = entry.error_message
  } finally {
    streaming.value = false
    abortController.value = null
    // 请求结束（成功/失败/停止）后用 detail 接口拉后端真实记录覆盖本地占位，
    // 拿到完整 attempts / error_message 并写回 localStorage；拉取失败保留本地 entry。
    await syncTestLog(entry)
  }
}

// 用后端 detail 记录替换本地占位，并持久化到浏览器本地缓存。
async function syncTestLog(entry: RouteLog) {
  try {
    const detail = await routeLogs.detail(entry.request_id)
    const index = logs.value.findIndex((log) => log.request_id === entry.request_id)
    if (index >= 0) logs.value[index] = detail
    else logs.value.unshift(detail)
    saveTestRouteLog(detail)
  } catch {
    // 后端日志可能尚未落库或不存在，保留本地 entry
    saveTestRouteLog(entry)
  }
}

// 展开请求：优先用已缓存详情；没有则从后端 detail 接口拉取并缓存。
async function expand(log: RouteLog) {
  if (detailsMap.has(log.request_id)) return
  loadingDetail.value = log.request_id
  try {
    const detail = await routeLogs.detail(log.request_id)
    detailsMap.set(log.request_id, detail)
    const index = logs.value.findIndex((item) => item.request_id === log.request_id)
    if (index >= 0) {
      logs.value[index] = detail
      saveTestRouteLog(detail)
    }
  } catch {
    // 详情拉取失败（如日志已被清理）：保持原状，表格显示「暂无步骤详情」
  } finally {
    loadingDetail.value = ''
  }
}

// 清空本页测试日志：清掉页面列表、详情缓存与 localStorage 持久化。
function clearTestLogs() {
  logs.value = []
  detailsMap.clear()
  clearTestRouteLogs()
}

function stop() {
  abortController.value?.abort()
}

onBeforeUnmount(() => {
  abortController.value?.abort()
  clearAttachments()
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="模型测试"
      description="后台代理上游 /models 与 /chat/completions，规避跨域；测试请求会写入转发日志。"
    />

    <div class="grid gap-6 md:grid-cols-[minmax(22rem,5fr)_minmax(0,7fr)]">
      <section class="min-w-0 space-y-4">
        <Card class="rounded-md">
          <CardHeader class="pb-4">
            <CardTitle class="text-base">连接配置</CardTitle>
            <CardDescription>从已有渠道快速导入，或直接填入临时配置。</CardDescription>
          </CardHeader>
          <CardContent class="space-y-4">
            <div class="grid gap-3 md:grid-cols-2">
              <div class="space-y-2">
                <Label for="test-channel">预设</Label>
                <Select
                  :model-value="config.channelId"
                  :disabled="channelsLoading"
                  @update:model-value="importChannel(String($event))"
                >
                  <SelectTrigger id="test-channel">
                    <SelectValue
                      :placeholder="
                        channelsLoading
                          ? '正在加载渠道'
                          : channels?.length
                            ? '选择渠道并快速导入'
                            : '暂无可用渠道'
                      "
                    />
                  </SelectTrigger>
                  <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                    <SelectGroup>
                      <SelectItem
                        v-for="channel in channels || []"
                        :key="channel.id"
                        :value="channel.id"
                        >{{ channel.name }}</SelectItem
                      >
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
              <div class="space-y-2">
                <Label for="test-suffix">模型后缀</Label>
                <Select :model-value="suffixMode" @update:model-value="setSuffixMode">
                  <SelectTrigger id="test-suffix">
                    <SelectValue placeholder="选择模型后缀模式" />
                  </SelectTrigger>
                  <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                    <SelectGroup>
                      <SelectItem value="gpt">/responses</SelectItem>
                      <SelectItem value="claude">/messages</SelectItem>
                      <SelectItem value="chat">/chat/completions</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div class="space-y-2">
              <Label for="test-base-url">Base URL</Label
              ><Input
                id="test-base-url"
                v-model="config.baseUrl"
                placeholder="https://api.example.com/v1"
                autocomplete="url"
              />
            </div>
            <div class="space-y-2">
              <Label for="test-api-key">API Key</Label
              ><Input
                id="test-api-key"
                v-model="config.apiKey"
                type="password"
                placeholder="sk-..."
                autocomplete="off"
              />
            </div>
            <div class="space-y-2">
              <div class="flex items-center justify-between gap-3">
                <Label for="test-model">模型</Label
                ><Button
                  size="sm"
                  type="button"
                  variant="outline"
                  :disabled="modelsLoading"
                  @click="fetchModels"
                >
                  <RiRefreshLine size="16" />{{ modelsLoading ? '获取中' : '获取所有模型' }}
                </Button>
              </div>
              <Input
                id="test-model"
                v-model="config.model"
                placeholder="输入或选择模型"
                autocomplete="off"
              />
              <p v-if="modelsError" class="text-xs text-destructive">{{ modelsError }}</p>
              <div
                v-else-if="models.length"
                class="flex max-h-64 flex-wrap gap-2 overflow-y-auto rounded-md border border-border p-2"
              >
                <Button
                  v-for="model in filteredModels"
                  :key="model"
                  size="sm"
                  type="button"
                  variant="secondary"
                  class="h-7 font-mono text-xs"
                  @click="chooseModel(model)"
                  >{{ model }}</Button
                >
                <p v-if="!filteredModels.length" class="w-full py-1 text-sm text-muted-foreground">
                  没有匹配的模型
                </p>
              </div>
            </div>
          </CardContent>
        </Card>

        <Card class="rounded-md">
          <CardHeader class="flex flex-row items-start justify-between gap-3 space-y-0 pb-3">
            <div>
              <CardTitle class="text-base">Messages</CardTitle>
              <CardDescription>按顺序编辑测试请求中的消息。</CardDescription>
            </div>
            <TooltipProvider>
              <Tooltip>
                <TooltipTrigger as-child
                  ><Button size="icon" variant="outline" aria-label="添加消息" @click="addMessage">
                    <RiAddLine size="16" /> </Button
                ></TooltipTrigger>
                <TooltipContent>添加消息</TooltipContent>
              </Tooltip>
            </TooltipProvider>
          </CardHeader>
          <CardContent class="space-y-3 pt-0">
            <p v-if="!messages.length" class="rounded-md border border-dashed border-border py-6 text-center text-sm text-muted-foreground">
              暂无消息。在右侧输入发送，或点 + 添加消息组成多轮对话。
            </p>
            <div
              v-for="(message, index) in messages"
              :key="message.id"
              class="grid grid-cols-[110px_minmax(0,1fr)_36px] items-start gap-2 rounded-md border border-border p-3"
            >
              <Select v-model="message.role">
                <SelectTrigger class="w-[110px] shrink-0">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" :side-offset="2">
                  <SelectGroup>
                    <SelectItem value="system">system</SelectItem>
                    <SelectItem value="user">user</SelectItem>
                    <SelectItem value="assistant">assistant</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <Textarea v-model="message.content" class="min-w-0" placeholder="输入消息内容" />
              <Button
                size="icon"
                variant="ghost"
                aria-label="删除消息"
                :disabled="messages.length === 1"
                @click="removeMessage(index)"
              >
                <RiCloseLine size="16" />
              </Button>
            </div>
          </CardContent>
        </Card>
      </section>

      <section class="min-w-0 space-y-4">
        <Card class="rounded-md">
          <CardHeader class="pb-4">
            <CardTitle class="text-base">用户输入</CardTitle>
            <CardDescription>可粘贴图片、拖入图片或文件，也可手动添加资源。</CardDescription>
          </CardHeader>
          <CardContent class="space-y-3">
            <div
              class="space-y-3 rounded-md border border-dashed border-border bg-muted/20 p-3"
              @dragover.prevent
              @drop="onDrop"
            >
              <Textarea
                v-model="draft"
                rows="8"
                placeholder="输入本次测试消息；粘贴或拖入图片会作为附件加入。"
                @paste="onPaste"
              />
              <input ref="fileInput" class="hidden" type="file" multiple @change="onFileChange" />
              <div v-if="attachments.length" class="flex flex-wrap gap-2">
                <div
                  v-for="attachment in attachments"
                  :key="attachment.id"
                  class="flex h-9 max-w-full items-center gap-1 rounded-md border border-border bg-background pr-1 text-xs sm:max-w-56"
                  :class="
                    attachment.preview
                      ? 'cursor-pointer transition-colors hover:bg-muted/60 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring'
                      : ''
                  "
                  :role="attachment.preview ? 'button' : undefined"
                  :tabindex="attachment.preview ? 0 : undefined"
                  :aria-label="attachment.preview ? `预览图片：${attachment.name}` : undefined"
                  @click="attachment.preview && openImagePreview(attachment)"
                  @keydown.enter.prevent="attachment.preview && openImagePreview(attachment)"
                  @keydown.space.prevent="attachment.preview && openImagePreview(attachment)"
                >
                  <span
                    v-if="attachment.preview"
                    class="flex size-8 shrink-0 items-center justify-center"
                  >
                    <img
                      :src="attachment.preview"
                      :alt="attachment.name"
                      class="size-6 rounded-sm object-cover"
                    />
                  </span>
                  <span v-else class="flex size-8 shrink-0 items-center justify-center">
                    <RiAttachment2 size="15" class="text-muted-foreground" />
                  </span>
                  <span class="min-w-0 truncate">{{ attachmentLabel(attachment.name) }}</span>
                  <TooltipProvider>
                    <Tooltip>
                      <TooltipTrigger as-child>
                        <Button
                          type="button"
                          size="icon"
                          variant="ghost"
                          class="size-7 shrink-0"
                          aria-label="删除附件"
                          @click.stop="removeAttachment(attachment.id)"
                        >
                          <RiCloseLine size="14" />
                        </Button>
                      </TooltipTrigger>
                      <TooltipContent>删除附件</TooltipContent>
                    </Tooltip>
                  </TooltipProvider>
                </div>
              </div>
              <div class="flex flex-wrap items-center justify-between gap-3">
                <Button type="button" variant="outline" size="sm" @click="openFilePicker">
                  <RiImageAddLine size="16" />添加图片或资源
                </Button>
                <div class="flex items-center gap-2">
                  <Button
                    v-if="streaming"
                    type="button"
                    variant="outline"
                    @click="stop"
                  >
                    <RiStopLine size="16" />停止
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    :disabled="!draft.trim() && !attachments.length"
                    @click="clearDraft"
                  >
                    <RiDeleteBinLine size="16" />清空
                  </Button>
                  <Button
                    type="button"
                    :disabled="streaming || (!config.model.trim() && !config.channelId)"
                    @click="send"
                  >
                    <RiPlayLine size="16" />发送
                  </Button>
                </div>
              </div>
            </div>
            <p v-if="fetchError" class="text-sm text-destructive">{{ fetchError }}</p>
          </CardContent>
        </Card>

        <Card class="rounded-md">
          <CardHeader class="pb-3">
            <CardTitle class="text-base">响应</CardTitle>
            <CardDescription>后台代理上游返回，流式逐字输出。</CardDescription>
          </CardHeader>
          <CardContent class="space-y-2">
            <div v-if="streaming" class="flex items-center gap-2 text-sm text-muted-foreground">
              <span class="size-2 animate-pulse rounded-full bg-primary"></span>正在生成...
            </div>
            <pre
              v-if="assistantReply"
              class="max-h-96 overflow-y-auto whitespace-pre-wrap break-words rounded-md border border-border bg-muted/20 p-3 font-sans text-sm text-foreground"
              >{{ assistantReply }}</pre
            >
            <p v-if="!streaming && !assistantReply" class="text-sm text-muted-foreground">
              发送请求后，这里会显示模型回复。
            </p>
          </CardContent>
        </Card>

        <RouteLogTable
          :logs="displayLogs"
          :channels="channels || []"
          :loading-detail="loadingDetail"
          @expand="expand"
        >
          <template #actions>
            <Button
              type="button"
              variant="outline"
              size="sm"
              :disabled="!displayLogs.length"
              aria-label="清空测试日志"
              @click="clearTestLogs"
            >
              <RiDeleteBinLine size="16" />清空
            </Button>
          </template>
        </RouteLogTable>
      </section>
    </div>

    <Dialog :open="Boolean(previewAttachment)" @update:open="!$event && (previewAttachment = null)">
      <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-hidden p-3 sm:max-w-5xl!">
        <DialogHeader>
          <DialogTitle class="truncate pr-8 text-base">{{
            previewAttachment?.name || '图片预览'
          }}</DialogTitle>
        </DialogHeader>
        <img
          v-if="previewAttachment?.preview"
          :src="previewAttachment.preview"
          :alt="previewAttachment.name"
          class="max-h-[calc(100dvh-6rem)] w-full object-contain"
        />
      </DialogContent>
    </Dialog>
  </div>
</template>
