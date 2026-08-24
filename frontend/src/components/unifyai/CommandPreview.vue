<script setup lang="ts">
import { computed, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiClipboardLine,
  RiCloudLine,
  RiCodeSSlashLine,
  RiFileList3Line,
  RiServerLine,
  RiBracesLine,
} from '@remixicon/vue'
import AxJsonViewer from '@/components/ui/AxJsonViewer.vue'
import type {
  ModelSourceKind,
  ModelSourceStatus,
  OpenCodexGroup,
  OpenCodexModelsResult,
} from '@/lib/unifyai'
import { groupOpenCodexModels } from '@/lib/unifyai'

const props = defineProps<{
  /** 实时拼装的 CLI 命令 */
  command: string
  /** 同步配置对象（sync.json 内容，用 AxJsonViewer 可折叠展示） */
  configData?: unknown
  /** 模型来源状态（文档 §5.2） */
  modelSource: ModelSourceStatus
  /** OpenCodex 代理模型列表（--list-models） */
  opencodexModels: OpenCodexModelsResult
  /** 强制视觉开关（--enable-vision），仅在显示时必传（默认显示，顶部已独立放置时传 false 隐藏） */
  enableVision: boolean
  /** 切换强制视觉后重新拉取模型列表 */
  onToggleVision: (v: boolean) => void
  /** 是否在本卡片内显示强制视觉开关（false = 移到外部独立位置） */
  showVision?: boolean
  /** MCP 配置文件路径 */
  mcpSourcePath: string
  /** MCP 服务器启用数 / 总数 */
  mcpEnabled: number
  mcpTotal: number
}>()

const expanded = ref(true)

async function copyCommand() {
  await navigator.clipboard.writeText(props.command)
  toast.success('命令已复制', { description: '粘贴到终端执行即可完成同步' })
}

const modelLabel: Record<
  ModelSourceKind,
  { text: string; variant: 'default' | 'outline' | 'destructive' }
> = {
  openrouter: { text: 'OpenRouter', variant: 'default' },
  none: { text: '模型源不可用', variant: 'destructive' },
}

/** 缓存时间 → 本地时间（无值返回空串） */
function formatCachedAt(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  return d.toLocaleString('zh-CN', { hour12: false })
}

const cachedAtText = computed(() => formatCachedAt(props.modelSource.cachedAt))

/** OpenCodex 模型按 provider 分组 */
const opencodexGroups = computed<OpenCodexGroup[]>(() =>
  groupOpenCodexModels(props.opencodexModels.models),
)
/** 模型卡片内联展开/折叠 */
const opencodexExpanded = ref(false)
</script>

<template>
  <Card class="rounded-md">
    <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
      <div class="space-y-0.5">
        <CardTitle class="text-base">数据预览</CardTitle>
        <CardDescription>将要同步的数据来源与最终执行命令。</CardDescription>
      </div>
      <Button variant="ghost" size="icon" class="size-8" @click="expanded = !expanded">
        <RiArrowDownSLine v-if="expanded" size="16" />
        <RiArrowRightSLine v-else size="16" />
      </Button>
    </CardHeader>
    <CardContent v-show="expanded" class="space-y-3">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="flex items-center gap-3 rounded-md border p-3">
          <span class="grid size-8 shrink-0 place-items-center rounded-md bg-muted">
            <RiCloudLine size="16" class="text-muted-foreground" />
          </span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-1.5 text-sm">
              <span class="font-medium">模型来源</span>
              <Badge :variant="modelLabel[modelSource.kind].variant" class="font-normal">
                {{ modelLabel[modelSource.kind].text }}
              </Badge>
              <template v-if="modelSource.kind === 'openrouter'">
                <Badge variant="secondary" class="font-normal">{{ modelSource.modelCount }} 个模型</Badge>
                <Badge variant="outline" class="font-normal">👁 {{ modelSource.visionCount }} 视觉</Badge>
                <Badge variant="outline" class="font-normal">🧠 {{ modelSource.reasoningCount }} 思考</Badge>
              </template>
            </div>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ modelSource.baseUrl || 'https://openrouter.ai/api/v1' }}
              <template v-if="modelSource.apiKeyMasked"> · API Key {{ modelSource.apiKeyMasked }}</template>
            </p>
            <p v-if="modelSource.kind === 'none' && modelSource.degraded" class="mt-0.5 text-xs text-amber-600">
              {{ modelSource.degraded }}
            </p>
            <p v-if="modelSource.kind === 'openrouter'" class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs">
              <span v-if="cachedAtText" class="text-emerald-600">元数据缓存：{{ cachedAtText }} 已刷新</span>
              <span v-else class="text-amber-600">元数据缓存：尚未刷新</span>
              <span v-if="modelSource.apiKeyMasked" class="text-muted-foreground">公开端点，无需密钥</span>
            </p>
          </div>
        </div>
        <div class="flex items-center gap-3 rounded-md border p-3">
          <span class="grid size-8 shrink-0 place-items-center rounded-md bg-muted">
            <RiFileList3Line size="16" class="text-muted-foreground" />
          </span>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-1.5 text-sm">
              <span class="font-medium">MCP 来源</span>
              <Badge variant="secondary" class="font-normal"
                >{{ mcpEnabled }}/{{ mcpTotal }} 启用</Badge
              >
            </div>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ mcpSourcePath }}
            </p>
          </div>
        </div>
        <div class="flex items-start gap-3 rounded-md border p-3 sm:col-span-2">
          <span class="grid size-8 shrink-0 place-items-center rounded-md bg-muted">
            <RiBracesLine size="16" class="text-muted-foreground" />
          </span>
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-1.5 text-sm">
              <span class="font-medium">OpenCodex 模型</span>
              <Badge variant="secondary" class="font-normal">{{ opencodexModels.count }} 个模型</Badge>
              <Badge variant="outline" class="font-normal"
                >{{ opencodexModels.enabledProviderCount }} 个 provider</Badge
              >
              <Badge
                v-if="opencodexModels.orMatchedCount != null"
                variant="outline"
                class="border-emerald-500/30 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300"
              >
                OpenRouter 匹配 {{ opencodexModels.orMatchedCount }}/{{ opencodexModels.orTotal }}
              </Badge>
              <Badge v-if="opencodexModels.hasApiKey" variant="outline" class="font-normal">
                Key {{ opencodexModels.apiKeyPreview }}
              </Badge>
              <label v-if="showVision !== false" class="ml-auto flex cursor-pointer items-center gap-1.5 text-xs text-muted-foreground">
                <span>强制视觉</span>
                <Switch :model-value="enableVision" @update:model-value="onToggleVision" />
              </label>
            </div>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ opencodexModels.proxyUrl }}
            </p>
            <p v-if="opencodexModels.degraded" class="mt-0.5 text-xs text-amber-600">
              {{ opencodexModels.degradedReason }}
            </p>
            <template v-else-if="opencodexModels.count > 0">
              <button
                type="button"
                class="mt-1 flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground"
                @click="opencodexExpanded = !opencodexExpanded"
              >
                <RiArrowDownSLine v-if="opencodexExpanded" size="14" />
                <RiArrowRightSLine v-else size="14" />
                {{ opencodexExpanded ? '收起' : '展开' }}（按 provider 分组）
              </button>
              <div v-show="opencodexExpanded" class="mt-1.5 space-y-2">
                <div v-for="g in opencodexGroups" :key="g.provider" class="rounded-md border bg-muted/30 p-2">
                  <p class="text-xs font-medium text-muted-foreground">
                    {{ g.provider }}（{{ g.models.length }}）
                  </p>
                  <ul class="mt-1 grid gap-x-4 gap-y-0.5 sm:grid-cols-2">
                    <li
                      v-for="m in g.models"
                      :key="m.displayName"
                      class="truncate font-mono text-[11px] text-muted-foreground"
                      :title="m.displayName"
                    >
                      <span v-if="m.supportsThinking" class="mr-0.5">🧠</span>
                      <span v-else class="mr-0.5 opacity-40">🧠</span>
                      <span v-if="m.supportsVision" class="mr-0.5">👁️</span>
                      <span v-else class="mr-0.5 opacity-40">👁️</span>
                      {{ m.modelId }}
                    </li>
                  </ul>
                </div>
              </div>
            </template>
          </div>
        </div>
      </div>
      <div class="rounded-md border bg-muted/40 p-3">
        <div class="mb-2 flex items-center justify-between gap-2">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <RiCodeSSlashLine size="14" />
            命令预览（实时生成，等宽字体）
          </div>
          <Button variant="outline" size="sm" class="h-7 px-2 text-xs" @click="copyCommand">
            <RiClipboardLine size="14" />复制命令
          </Button>
        </div>
        <code
          class="block overflow-x-auto whitespace-pre rounded-md bg-background p-3 font-mono text-xs leading-6"
        >
          {{ command }}
        </code>
        <template v-if="configData !== undefined">
          <p class="mt-2 flex items-center gap-1 text-xs font-medium text-muted-foreground">
            <RiBracesLine size="13" />同步配置（sync.json 内容，执行前写入，实时更新）
          </p>
          <div class="max-h-64 overflow-y-auto rounded-md border bg-background p-3">
            <AxJsonViewer :data="configData" :expand-level="2" :wrap-enabled="false" />
          </div>
        </template>
        <p class="mt-2 flex items-center gap-1 text-xs text-muted-foreground">
          <RiServerLine size="13" />
          复制到终端执行即可，等价于下方所有 UI 选项。
        </p>
      </div>
    </CardContent>
  </Card>
</template>
