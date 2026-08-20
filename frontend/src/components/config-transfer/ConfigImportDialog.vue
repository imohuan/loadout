<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import {
  Badge,
  Button,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from 'shadcn-vue-cdn'
import {
  RiCheckLine,
  RiCloseLine,
  RiErrorWarningLine,
  RiFileUploadLine,
  RiLoaderLine,
  RiRefreshLine,
  RiUploadCloud2Line,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import {
  importConfig,
  previewImport,
  type ImportMode,
  type ImportPreview,
  type ImportPreviewSection,
  type ImportResult,
  type ImportSectionResult,
} from '@/composables/useConfigTransfer'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

// 主配置 section key（可独立选择导入模式）；其余为附带文件，跟随所属主配置的模式。
const MAIN_KEYS = ['channels', 'aggregates', 'capability_routes', 'mcp', 'skills']
const MAIN_LABELS: Record<string, string> = {
  channels: '渠道配置',
  aggregates: '聚合模型配置',
  capability_routes: '能力路由配置',
  mcp: 'MCP 配置',
  skills: 'Skills 配置',
}

type Stage = 'idle' | 'previewing' | 'ready' | 'importing' | 'done'
const stage = ref<Stage>('idle')
const file = ref<File | null>(null)
const preview = ref<ImportPreview | null>(null)
const result = ref<ImportResult | null>(null)
const dragOver = ref(false)
const error = ref('')
// 每个主 section 的模式（默认追加，避免误覆盖）。
const modes = reactive<Record<string, ImportMode>>({})

const mainSections = computed<ImportPreviewSection[]>(() =>
  (preview.value?.sections || []).filter((section) => MAIN_KEYS.includes(section.key)),
)
const extraSections = computed<ImportPreviewSection[]>(() =>
  (preview.value?.sections || []).filter((section) => !MAIN_KEYS.includes(section.key)),
)

function reset() {
  stage.value = 'idle'
  file.value = null
  preview.value = null
  result.value = null
  error.value = ''
  dragOver.value = false
  for (const key of Object.keys(modes)) delete modes[key]
}

watch(
  () => props.open,
  (value) => {
    if (value) reset()
  },
)

function onFileChosen(chosen: File | undefined | null) {
  if (!chosen) return
  if (!/\.zip$/i.test(chosen.name) && chosen.type !== 'application/zip') {
    toast.error('请选择 zip 压缩包')
    return
  }
  file.value = chosen
  void runPreview(chosen)
}

function onDrop(event: DragEvent) {
  dragOver.value = false
  onFileChosen(event.dataTransfer?.files?.[0])
}

function onFileChange(event: Event) {
  onFileChosen((event.target as HTMLInputElement).files?.[0])
}

function onPickFile() {
  fileInputRef.value?.click()
}

const fileInputRef = ref<HTMLInputElement | null>(null)

async function runPreview(chosen: File) {
  stage.value = 'previewing'
  error.value = ''
  try {
    const result = await previewImport(chosen)
    preview.value = result
    // 为包内主配置初始化模式（默认追加），保留用户已选过的值。
    for (const section of result.sections) {
      if (MAIN_KEYS.includes(section.key) && modes[section.key] === undefined) {
        modes[section.key] = 'append'
      }
    }
    stage.value = 'ready'
  } catch (err) {
    error.value = err instanceof Error ? err.message : '解析 zip 失败'
    stage.value = 'idle'
  }
}

async function doImport() {
  if (!file.value || stage.value !== 'ready') return
  stage.value = 'importing'
  error.value = ''
  try {
    result.value = await importConfig(file.value, { ...modes })
    stage.value = 'done'
    const total = result.value.results.reduce((sum, item) => sum + item.imported, 0)
    const failed = result.value.results.filter((item) => item.errors?.length).length
    if (failed > 0) {
      toast.error('导入完成，但部分配置失败', {
        description: `${total} 条写入成功，${failed} 类配置有错误`,
      })
    } else {
      toast.success('配置导入完成', {
        description: `${total} 条配置已写入，可前往对应页面查看`,
      })
    }
  } catch (err) {
    error.value = err instanceof Error ? err.message : '导入失败'
    stage.value = 'ready'
  }
}

function modeOf(section: ImportPreviewSection): ImportMode {
  return modes[section.key] || 'append'
}

function setMode(section: ImportPreviewSection, mode: ImportMode) {
  modes[section.key] = mode
}

const doneSummary = computed(() => {
  const items = result.value?.results || []
  return {
    total: items.reduce((sum, item) => sum + item.count, 0),
    imported: items.reduce((sum, item) => sum + item.imported, 0),
    failed: items.filter((item) => item.errors?.length).length,
  }
})

function resultRowClass(item: ImportSectionResult) {
  if (item.errors?.length) return 'border-destructive/40 bg-destructive/5'
  if (item.imported > 0) return ''
  return 'opacity-70'
}

function onOpenChange(value: boolean) {
  emit('update:open', value)
}
</script>

<template>
  <Dialog :open="open" @update:open="onOpenChange">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl!">
      <DialogHeader>
        <DialogTitle>导入配置</DialogTitle>
        <DialogDescription>
          上传配置 zip，解析后逐项选择「覆盖」或「追加」，再写入当前实例。
        </DialogDescription>
      </DialogHeader>

      <!-- 拖拽 / 点击上传 -->
      <div
        v-if="stage === 'idle'"
        class="relative flex min-h-40 cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border-2 border-dashed px-6 py-8 text-center transition-colors"
        :class="
          dragOver
            ? 'border-primary bg-primary/5'
            : 'border-border hover:border-primary/50 hover:bg-muted/40'
        "
        @dragover.prevent="dragOver = true"
        @dragleave="dragOver = false"
        @drop.prevent="onDrop"
        @click="onPickFile"
      >
        <RiUploadCloud2Line size="36" class="text-muted-foreground" />
        <p class="text-sm font-medium">拖拽 zip 到此处，或点击选择文件</p>
        <p class="text-xs text-muted-foreground">
          支持从本页「导出配置」生成的压缩包（loadout-config）
        </p>
        <input
          ref="fileInputRef"
          type="file"
          accept=".zip,application/zip"
          class="hidden"
          @change="onFileChange"
        />
      </div>

      <!-- 解析中 -->
      <div
        v-else-if="stage === 'previewing'"
        class="flex min-h-40 flex-col items-center justify-center gap-3 text-muted-foreground"
      >
        <RiLoaderLine size="28" class="animate-spin" />
        <p class="text-sm">正在解析 zip…</p>
      </div>

      <!-- 解析完成：逐项选择模式 -->
      <div v-else-if="stage === 'ready' && preview" class="space-y-4">
        <div class="flex items-center gap-2 text-sm text-muted-foreground">
          <RiFileUploadLine size="16" />
          <span class="truncate font-mono text-xs">{{ file?.name }}</span>
          <span class="text-xs">共 {{ preview.sections.length }} 类配置</span>
        </div>

        <div class="space-y-2">
          <p class="text-xs font-medium text-muted-foreground">导入模式（每类独立选择）</p>
          <div
            v-for="section in mainSections"
            :key="section.key"
            class="rounded-md border border-border px-3 py-2.5"
          >
            <div class="flex items-center justify-between gap-3">
              <div class="min-w-0">
                <p class="text-sm font-medium">{{ MAIN_LABELS[section.key] || section.name }}</p>
                <p class="text-xs text-muted-foreground">
                  {{ section.file }} · {{ section.count }} 条
                </p>
              </div>
              <!-- Tab-style switch: 选中=白底阴影，未选=透明灰字，与左侧 Tab 视觉一致 -->
              <div
                class="inline-flex h-7 shrink-0 items-center justify-center gap-0.5 rounded-lg bg-muted p-0.5 text-muted-foreground"
                role="tablist"
              >
                <Button
                  type="button"
                  role="tab"
                  :aria-selected="modeOf(section) === 'overwrite'"
                  size="sm"
                  variant="ghost"
                  class="h-6 rounded-md px-2.5 text-xs font-medium transition-shadow"
                  :class="
                    modeOf(section) === 'overwrite'
                      ? 'bg-background text-foreground shadow-sm'
                      : 'hover:text-foreground'
                  "
                  @click="setMode(section, 'overwrite')"
                >
                  覆盖
                </Button>
                <Button
                  type="button"
                  role="tab"
                  :aria-selected="modeOf(section) === 'append'"
                  size="sm"
                  variant="ghost"
                  class="h-6 rounded-md px-2.5 text-xs font-medium transition-shadow"
                  :class="
                    modeOf(section) === 'append'
                      ? 'bg-background text-foreground shadow-sm'
                      : 'hover:text-foreground'
                  "
                  @click="setMode(section, 'append')"
                >
                  追加
                </Button>
              </div>
            </div>
            <p class="mt-1 text-xs text-muted-foreground">
              {{
                modeOf(section) === 'overwrite'
                  ? '用包内配置全量替换当前配置'
                  : '与现有配置合并，同名（id/name）以包内为准'
              }}
            </p>
          </div>

          <div v-if="extraSections.length" class="rounded-md border border-border px-3 py-2.5">
            <p class="text-sm font-medium">附带文件</p>
            <p class="mt-0.5 text-xs text-muted-foreground">
              跟随所属主配置的导入模式：
              <span
                v-for="(section, index) in extraSections"
                :key="section.key"
                class="mr-2 inline-block"
              >
                {{ section.name }}（{{ section.count }} 条）<template
                  v-if="index < extraSections.length - 1"
                  >·</template
                >
              </span>
            </p>
          </div>
        </div>
      </div>

      <!-- 导入中 -->
      <div
        v-else-if="stage === 'importing'"
        class="flex min-h-32 flex-col items-center justify-center gap-3 text-muted-foreground"
      >
        <RiLoaderLine size="28" class="animate-spin" />
        <p class="text-sm">正在写入配置…</p>
      </div>

      <!-- 导入结果 -->
      <div v-else-if="stage === 'done' && result" class="space-y-3">
        <div class="flex items-center gap-2 text-sm">
          <RiCheckLine v-if="!doneSummary.failed" size="16" class="text-green-600" />
          <RiErrorWarningLine v-else size="16" class="text-destructive" />
          <span>
            共 {{ doneSummary.total }} 条，写入 {{ doneSummary.imported }} 条
            <template v-if="doneSummary.failed">，{{ doneSummary.failed }} 类配置有错误</template>
          </span>
        </div>
        <div class="max-h-64 space-y-1.5 overflow-y-auto pr-1">
          <div
            v-for="item in result.results"
            :key="item.key"
            class="rounded-md border border-border px-3 py-2"
            :class="resultRowClass(item)"
          >
            <div class="flex items-center justify-between gap-3">
              <span class="text-sm font-medium">{{ item.name }}</span>
              <div class="flex items-center gap-2">
                <Badge v-if="item.errors?.length" variant="destructive">失败</Badge>
                <span class="text-xs text-muted-foreground">
                  {{ item.mode === 'overwrite' ? '覆盖' : '追加' }} · 导入 {{ item.imported }} /
                  {{ item.count }}
                </span>
              </div>
            </div>
            <div v-if="item.skipped?.length" class="mt-1 space-y-0.5">
              <p
                v-for="(skip, index) in item.skipped"
                :key="index"
                class="text-xs text-muted-foreground"
              >
                {{ skip }}
              </p>
            </div>
            <div v-if="item.errors?.length" class="mt-1 space-y-0.5">
              <p v-for="(err, index) in item.errors" :key="index" class="text-xs text-destructive">
                {{ err }}
              </p>
            </div>
          </div>
        </div>
      </div>

      <!-- 错误提示 -->
      <p
        v-if="error"
        class="rounded-md border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive"
      >
        {{ error }}
      </p>

      <DialogFooter>
        <template v-if="stage === 'ready'">
          <Button variant="outline" @click="reset"> <RiRefreshLine size="16" />重新选择 </Button>
          <Button :disabled="!mainSections.length" @click="doImport">
            <RiUploadCloud2Line size="16" />开始导入
          </Button>
        </template>
        <template v-else-if="stage === 'done'">
          <Button variant="outline" @click="reset()"> <RiRefreshLine size="16" />继续导入 </Button>
          <Button @click="emit('update:open', false)">完成</Button>
        </template>
        <Button v-else variant="outline" @click="emit('update:open', false)">
          <RiCloseLine size="16" />关闭
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
