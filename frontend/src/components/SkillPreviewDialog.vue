<script setup lang="ts">
// 技能文件预览弹窗：左侧目录树、右侧文件内容只读预览。
//
// 数据来源于后端两个只读接口：
//   GET /api/skills/{name}/tree           → 扁平条目清单（本组件还原成树）
//   GET /api/skills/{name}/file?path=...  → 单文件文本内容
// 渲染分流：Markdown 走 marked + dompurify，代码走 highlight.js（懒加载，不拖慢首屏），
// 其余纯文本走等宽字体 + 行号；二进制/超限文件只给提示不回内容。

import { computed, ref, watch } from 'vue'
import { RiClipboardLine, RiCloseLine, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useManagementApi } from '@/composables/useManagementApi'
import { buildSkillTree, defaultExpandedPaths, type SkillTreeNode } from '@/lib/skillTree'
import type { SkillFileContent } from '@/lib/types'
import SplitPane from '@/components/SplitPane.vue'
import SkillTreeNodeItem from './skill-preview/SkillTreeNode.vue'

const props = defineProps<{
  open: boolean
  skill: { name: string; path?: string; description?: string } | null
}>()

const emit = defineEmits<{ 'update:open': [boolean] }>()

const api = useManagementApi()

const loadingTree = ref(false)
const loadingFile = ref(false)
const entries = ref<SkillTreeNode[]>([])
const expanded = ref<string[]>([])
const selectedPath = ref('')
const file = ref<SkillFileContent | null>(null)
const treeError = ref('')
const rootPath = ref('')
// 技能目录里是否有 SKILL.md（决定「查看 SKILL.md」按钮是否可用）。
const hasSkillMd = ref(false)

const skillName = computed(() => props.skill?.name || '')
const selectedName = computed(() => {
  if (!selectedPath.value) return ''
  const parts = selectedPath.value.split('/')
  return parts[parts.length - 1] || selectedPath.value
})

// 预览分流用：优先按完整文件名识别（Dockerfile 之类），再回退扩展名。
const HIGHLIGHT_LANGS: Record<string, string> = {
  bash: 'bash', c: 'c', cpp: 'cpp', cs: 'csharp', css: 'css', dockerfile: 'dockerfile',
  go: 'go', html: 'xml', java: 'java', js: 'javascript', json: 'json', jsx: 'javascript',
  kt: 'kotlin', lua: 'lua', md: 'markdown', markdown: 'markdown', php: 'php', ps1: 'powershell',
  py: 'python', rb: 'ruby', rs: 'rust', scss: 'scss', sh: 'bash', sql: 'sql', swift: 'swift',
  toml: 'ini', ts: 'typescript', tsx: 'typescript', vue: 'xml', xml: 'xml', yaml: 'yaml', yml: 'yaml',
  zsh: 'bash',
}

const ext = computed(() => {
  const name = selectedName.value.toLowerCase()
  if (!name) return ''
  const dot = name.lastIndexOf('.')
  return dot > 0 ? name.slice(dot + 1) : name
})
const isMarkdown = computed(() => ext.value === 'md' || ext.value === 'markdown')
const highlightLang = computed(() => HIGHLIGHT_LANGS[ext.value] || '')
const canHighlight = computed(
  () => !!file.value && !file.value.binary && !isMarkdown.value && !!highlightLang.value,
)

const markdownHtml = ref('')
const highlightedHtml = ref('')
// 按行渲染代码：行号列与代码列共用同一 line-height，横向滚动时行号固定在左侧。
const codeLines = computed(() => {
  if (!file.value || file.value.binary) return [] as string[]
  const html = highlightedHtml.value
  if (!html) return (file.value.content || '').split('\n')
  return html.replace(/\n$/, '').split('\n')
})
const lineCount = computed(() => (file.value?.content ? codeLines.value.length : 0))

function formatSize(size: number) {
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`
  return `${(size / 1024 / 1024).toFixed(1)} MB`
}

// 打开新技能时重置全部状态并加载目录树。
watch(
  () => [props.open, skillName.value] as const,
  ([open, name]) => {
    if (!open || !name) return
    expanded.value = []
    selectedPath.value = ''
    file.value = null
    entries.value = []
    treeError.value = ''
    rootPath.value = ''
    markdownHtml.value = ''
    highlightedHtml.value = ''
    void loadTree(name)
  },
  { immediate: true },
)

async function loadTree(name = skillName.value) {
  if (!name) return
  loadingTree.value = true
  treeError.value = ''
  try {
    const tree = await api.skillTree(name)
    const roots = buildSkillTree(tree.entries || [])
    entries.value = roots
    rootPath.value = tree.root || ''
    expanded.value = defaultExpandedPaths(roots)
    // 默认预览 SKILL.md，方便一眼看到技能说明。
    const skillMd = (tree.entries || []).find((e) => e.path === 'SKILL.md' && !e.dir)
    hasSkillMd.value = !!skillMd
    if (skillMd) void openFile('SKILL.md')
  } catch (e) {
    // 目录加载失败（如后端版本过旧没有该接口）时明确提示，不要静默成「空目录」。
    treeError.value = e instanceof Error ? e.message : String(e)
    toast.error('技能目录加载失败', { description: treeError.value })
  } finally {
    loadingTree.value = false
  }
}

function toggleDir(path: string) {
  const i = expanded.value.indexOf(path)
  if (i >= 0) expanded.value.splice(i, 1)
  else expanded.value.push(path)
}

async function openFile(path: string) {
  if (!skillName.value || !path) return
  selectedPath.value = path
  loadingFile.value = true
  file.value = null
  markdownHtml.value = ''
  highlightedHtml.value = ''
  try {
    const res = await api.skillFile(skillName.value, path)
    file.value = res
    if (res.binary || !res.content) return
    if (isMarkdown.value) {
      const { marked } = await import('marked')
      const DOMPurify = (await import('dompurify')).default
      const raw = await marked.parse(res.content, { async: true })
      markdownHtml.value = DOMPurify.sanitize(raw)
      return
    }
    if (canHighlight.value) {
      const hljs = (await import('highlight.js/lib/common')).default
      highlightedHtml.value = hljs.highlight(res.content, { language: highlightLang.value }).value
    }
  } catch (e) {
    toast.error('文件读取失败', { description: e instanceof Error ? e.message : String(e) })
    selectedPath.value = ''
  } finally {
    loadingFile.value = false
  }
}

async function copyPath() {
  const target = rootPath.value || props.skill?.path || skillName.value
  try {
    await navigator.clipboard.writeText(target)
    toast.success('路径已复制', { description: target })
  } catch (e) {
    toast.error('复制失败', { description: e instanceof Error ? e.message : String(e) })
  }
}

function close() {
  emit('update:open', false)
}
</script>

<template>
  <Dialog :open="open" @update:open="(o: boolean) => emit('update:open', o)">
    <DialogContent
      class="flex h-[86vh] w-[94vw] max-w-[94vw]! flex-col gap-0 overflow-hidden p-0 sm:max-w-[94vw]!"
      :show-close-button="false"
    >
      <DialogHeader class="flex shrink-0 flex-row items-center gap-1 border-b border-border px-4 py-2.5">
        <div class="min-w-0 flex-1">
          <DialogTitle class="truncate text-base">{{ skillName }} · 文件</DialogTitle>
          <DialogDescription class="truncate text-xs text-muted-foreground">
            {{ rootPath || skill?.path || '技能目录' }}
          </DialogDescription>
        </div>
        <Button variant="ghost" size="icon" aria-label="复制技能路径" @click="copyPath">
          <RiClipboardLine class="size-4" />
        </Button>
        <Button variant="ghost" size="icon" aria-label="重新加载" :disabled="loadingTree" @click="loadTree()">
          <RiLoader4Line v-if="loadingTree" class="size-4 animate-spin" />
          <RiRefreshLine v-else class="size-4" />
        </Button>
        <Button variant="ghost" size="icon" aria-label="关闭" @click="close">
          <RiCloseLine class="size-4" />
        </Button>
      </DialogHeader>

      <SplitPane :min-left="200" :min-right="280" :initial="0.28" height-class="flex-1 min-h-0">
        <template #left>
          <div class="flex h-full flex-col">
            <div class="border-b border-border px-3 py-2 text-xs text-muted-foreground">
              目录结构（{{ entries.length ? '点击文件预览' : '空' }}）
            </div>
            <div class="min-h-0 flex-1 overflow-auto py-1">
              <div v-if="loadingTree" class="flex items-center gap-2 px-3 py-2 text-sm text-muted-foreground">
                <RiLoader4Line class="size-4 animate-spin" />加载中…
              </div>
              <p v-else-if="treeError" class="px-3 py-2 text-sm text-destructive">{{ treeError }}</p>
              <p v-else-if="!entries.length" class="px-3 py-4 text-sm text-muted-foreground">
                这个技能目录是空的。
              </p>
              <template v-else>
                <SkillTreeNodeItem
                  v-for="node in entries"
                  :key="node.path"
                  :node="node"
                  :depth="0"
                  :expanded="expanded"
                  :selected="selectedPath"
                  @toggle="toggleDir"
                  @select="openFile"
                />
              </template>
            </div>
          </div>
        </template>

        <template #right>
          <div class="flex h-full min-w-0 flex-col bg-muted/20">
            <div
              class="flex items-center gap-2 border-b border-border px-3 py-2 text-xs text-muted-foreground"
            >
              <span class="min-w-0 flex-1 truncate font-mono">{{ selectedPath || '未选择文件' }}</span>
              <span v-if="file" class="shrink-0 tabular-nums">
                {{ formatSize(file.size) }}
                <template v-if="lineCount"> · {{ lineCount }} 行</template>
              </span>
            </div>

            <div v-if="loadingFile" class="flex shrink-0 items-center gap-2 px-5 py-3 text-sm text-muted-foreground">
              <RiLoader4Line class="size-4 animate-spin" />读取中…
            </div>
            <div v-else-if="!file" class="flex flex-1 items-center justify-center p-4 text-center">
              <p class="text-sm text-muted-foreground">点击左侧文件即可在这里预览内容。</p>
            </div>
            <div v-else-if="file.binary" class="flex flex-1 items-center justify-center p-4">
              <p class="text-sm text-muted-foreground">二进制文件（{{ formatSize(file.size) }}），不支持预览。</p>
            </div>
            <template v-else>
              <!-- Markdown：渲染后的富文本 -->
              <div v-if="isMarkdown" class="min-h-0 flex-1 overflow-auto p-5">
                <div class="prose-sm max-w-none space-y-3 text-sm [&_code]:rounded [&_code]:bg-muted [&_code]:px-1 [&_h1]:text-xl [&_h1]:font-semibold [&_h2]:text-lg [&_h2]:font-semibold [&_h3]:font-semibold [&_li]:ml-5 [&_li]:list-disc [&_pre]:overflow-x-auto [&_pre]:rounded-md [&_pre]:bg-muted [&_pre]:p-3 [&_table]:border-collapse [&_td]:border [&_td]:border-border [&_td]:px-2 [&_th]:border [&_th]:border-border [&_th]:px-2" v-html="markdownHtml" />
              </div>
              <!-- 代码/文本：行号 + 等宽内容，整体横向滚动 -->
              <div v-else class="min-h-0 flex-1 overflow-auto">
                <div class="min-w-full font-mono text-[12.5px] leading-5">
                  <div
                    v-for="(line, i) in codeLines"
                    :key="i"
                    class="flex min-w-full whitespace-pre"
                  >
                    <span
                      class="sticky left-0 z-10 shrink-0 select-none border-r border-border bg-muted/60 px-2 text-right text-muted-foreground tabular-nums"
                      :style="{ width: `${String(lineCount).length + 2}ch` }"
                      >{{ i + 1 }}</span
                    >
                    <span
                      v-if="canHighlight"
                      class="px-3"
                      v-html="line || '&nbsp;'"
                    />
                    <span v-else class="px-3">{{ line }}</span>
                  </div>
                </div>
              </div>
              <p
                v-if="file.truncated"
                class="border-t border-border bg-amber-500/10 px-3 py-1.5 text-xs text-amber-700 dark:text-amber-400"
              >
                文件较大，仅显示前 512 KiB 内容。
              </p>
            </template>
          </div>
        </template>
      </SplitPane>

      <DialogFooter
        class="mx-0! mb-0! shrink-0 rounded-none! border-t border-border bg-transparent! px-4 py-2.5 sm:justify-end"
      >
        <Button
          variant="outline"
          :disabled="!hasSkillMd"
          @click="openFile('SKILL.md')"
          >查看 SKILL.md</Button
        >
        <Button variant="outline" @click="close">关闭</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
