<script setup lang="ts">
import { computed, nextTick, onUnmounted, reactive, ref, watch } from 'vue'
import {
  RiAddLine,
  RiDeleteBinLine,
  RiEditLine,
  RiLoaderLine,
  RiRefreshLine,
  RiUploadLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useManagementApi } from '@/composables/useManagementApi'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
const api = useManagementApi()
const {
  data: skills,
  loading: skillsLoading,
  refreshing: skillsRefreshing,
  refresh: refreshSkills,
} = useListLoader(api.skills)
const {
  data: presets,
  loading: presetsLoading,
  refreshing: presetsRefreshing,
  refresh: refreshPresets,
} = useListLoader(api.presets)
const {
  data: skillStatus,
  refreshing: statusRefreshing,
  refresh: refreshStatus,
} = useListLoader(api.skillStatus)
const { pending, run } = useAsyncTask()
const { confirmDialog } = useConfirm()
const activeTab = ref('skills')
const skillDialog = ref(false)
const presetDialog = ref(false)
const skillFile = ref<File>()
const skillForm = reactive({ name: '', source: '', version: '' })
const presetForm = reactive({
  name: '',
  selectedSkills: [] as string[],
  targets: [] as string[], // 多选平台（'generic'=通用 .agents）
})
// 技能清单显示模式：tag=标签点选；detail=列表（名称+描述，一行一个）。
const skillDisplayMode = ref<'tag' | 'detail'>('tag')
function toggleSkill(name: string) {
  const i = presetForm.selectedSkills.indexOf(name)
  if (i >= 0) presetForm.selectedSkills.splice(i, 1)
  else presetForm.selectedSkills.push(name)
}
function toggleTarget(value: string) {
  const i = presetForm.targets.indexOf(value)
  if (i >= 0) presetForm.targets.splice(i, 1)
  else presetForm.targets.push(value)
}
const loading = computed(() => skillsLoading.value || presetsLoading.value)
const anyRefreshing = computed(
  () => skillsRefreshing.value || presetsRefreshing.value || statusRefreshing.value,
)
// 独立操作 loading（各自按钮显示旋转图标，互不干扰）。
const syncing = ref(false)
const npxCmd = ref('')

// ===== 更新日志（SSE 实时进度）=====
type UpdateStatus = 'idle' | 'running' | 'done' | 'error'
const logLines = ref<string[]>([])
const updateStatus = ref<UpdateStatus>('idle')
const logBox = ref<HTMLElement>()
let updateStream: EventSource | null = null

const updateStatusLabel = computed(() => {
  switch (updateStatus.value) {
    case 'running':
      return '更新中…'
    case 'done':
      return '已完成'
    case 'error':
      return '失败'
    default:
      return '未开始'
  }
})

function stopUpdateStream() {
  updateStream?.close()
  updateStream = null
}

function openUpdateStream() {
  stopUpdateStream()
  logLines.value = []
  updateStatus.value = 'running'
  activeTab.value = 'logs'
  const es = new EventSource('/api/skills/update-stream')
  updateStream = es
  // 统一用 onmessage + JSON.type 分桶，不依赖 SSE event: 行（更稳健，
  // 避免 vite 代理/网络层对自定义事件名的兼容差异）。
  es.onmessage = (e: MessageEvent) => {
    let ev: { type: string; line?: string; data?: string }
    try {
      ev = JSON.parse(e.data as string)
    } catch {
      ev = { type: 'log', line: e.data as string }
    }
    if (ev.type === 'log') {
      logLines.value.push(ev.line || '')
    } else if (ev.type === 'done') {
      updateStatus.value = 'done'
      stopUpdateStream()
      void refreshSkills()
      void refreshStatus()
    } else if (ev.type === 'error') {
      updateStatus.value = 'error'
      stopUpdateStream()
      void refreshSkills()
      void refreshStatus()
    }
  }
  es.onerror = () => {
    // 任务结束或连接异常：仅当仍在 running 时视为失败。
    if (updateStatus.value === 'running') {
      updateStatus.value = 'error'
    }
    stopUpdateStream()
  }
}

// 日志自动滚动到底部。
watch(
  () => logLines.value.length,
  async () => {
    await nextTick()
    if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
  },
)

// 组件卸载时关闭 SSE 连接。
onUnmounted(stopUpdateStream)

// ===== ANSI 颜色码 → HTML（支持 16 色 + 256 色 + 重置/加粗）=====
const FG16: Record<number, string> = {
  30: '#000',
  31: '#c00',
  32: '#0a0',
  33: '#aa0',
  34: '#00c',
  35: '#c0c',
  36: '#0aa',
  37: '#bbb',
  90: '#555',
  91: '#f55',
  92: '#5f5',
  93: '#ff5',
  94: '#55f',
  95: '#f5f',
  96: '#5ff',
  97: '#fff',
}
function ansi256(n: number): string {
  if (n < 16) {
    return FG16[n < 8 ? 30 + n : 90 + (n - 8)] || '#fff'
  }
  if (n >= 232) {
    const g = (n - 232) * 10 + 8
    return `rgb(${g},${g},${g})`
  }
  const c = n - 16
  const r = Math.floor(c / 36)
  const g = Math.floor((c % 36) / 6)
  const b = c % 6
  const v = (lv: number) => (lv === 0 ? 0 : 55 * lv + 40)
  return `rgb(${v(r)},${v(g)},${v(b)})`
}
function ansiToHtml(s: string): string {
  // 1) 去掉 OSC 序列（标题/颜色等，...\u0007 结尾）。
  let out = s.replace(/\u001b\][\s\S]*?(?:\u0007|\u001b\\)/g, '')
  // 2) 去掉行内 \r（终端用 CR 覆盖行，进度条等场景）。
  out = out.replace(/\r+/g, '')
  // 3) HTML escape。
  out = out.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
  // 4) 统一处理 CSI 序列：SGR (m) → span；其他（光标移动/擦除/私有模式等）→ 剥掉。
  out = out.replace(/\u001b\[([\d;?]*)([\x40-\x7e])/g, (_, params: string, final: string) => {
    if (final !== 'm') {
      return '' // 非 SGR 全部剥掉（光标/清除/私有模式）
    }
    const tokens = params
      .split(';')
      .filter((c) => c !== '')
      .map(Number)
    if (tokens.includes(0)) {
      return '</span>'
    }
    const styles: string[] = []
    for (let i = 0; i < tokens.length; i++) {
      const c = tokens[i]
      if (c === 1) styles.push('font-weight:bold')
      else if (c === 2) styles.push('font-weight:normal')
      else if (c === 3) styles.push('font-style:italic')
      else if (c === 4) styles.push('text-decoration:underline')
      else if (c === 38 && tokens[i + 1] === 5 && i + 2 < tokens.length) {
        styles.push(`color:${ansi256(tokens[i + 2])}`)
        i += 2
      } else if (c === 48 && tokens[i + 1] === 5 && i + 2 < tokens.length) {
        styles.push(`background-color:${ansi256(tokens[i + 2])}`)
        i += 2
      } else if (FG16[c]) styles.push(`color:${FG16[c]}`)
    }
    if (!styles.length) return ''
    return `<span style="${styles.join(';')}">`
  })
  return out
}

// 组件卸载时关闭 SSE 连接。
onUnmounted(stopUpdateStream)

// 目标平台选项与标签（generic 映射后端空串 = 通用 .agents）。
const platformOptions = [
  { value: 'generic', label: '通用 (.agents)' },
  { value: 'codex', label: 'Codex' },
  { value: 'claudecode', label: 'Claude Code' },
  { value: 'opencode', label: 'OpenCode' },
]
const platformLabel: Record<string, string> = {
  '': '通用 (.agents)',
  generic: '通用 (.agents)',
  codex: 'Codex',
  claudecode: 'Claude Code',
  opencode: 'OpenCode',
}
function platformName(name?: string) {
  if (!name) return '通用 (.agents)'
  return platformLabel[name] || name
}
function targetToApi(target: string) {
  return target === 'generic' ? '' : target
}

// 相对时间：刚刚 / N 分钟前 / N 小时前 / N 天前更新。
function timeAgo(iso?: string) {
  if (!iso) return '-'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '-'
  const diff = Date.now() - t
  if (diff < 60_000) return '刚刚更新'
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前更新`
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前更新`
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前更新`
  return new Date(iso).toLocaleDateString()
}
// 近期更新：7 天内。
function isRecent(iso?: string) {
  if (!iso) return false
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return false
  return Date.now() - t < 7 * 86_400_000
}

function parseNpxSkillsCmd(raw: string) {
  const tokens = raw.trim().split(/\s+/).filter(Boolean)
  const skillsIdx = tokens.indexOf('skills')
  if (skillsIdx < 0) return null
  const addIdx = tokens.indexOf('add', skillsIdx + 1)
  if (addIdx < 0) return null
  const sourceRaw = tokens[addIdx + 1]
  if (!sourceRaw) return null

  let source = sourceRaw
  let version = ''
  let name = ''
  for (let i = addIdx + 2; i < tokens.length; i++) {
    const t = tokens[i]
    const next = tokens[i + 1]
    if ((t === '--skill' || t === '-s') && next) {
      name = next
      i++
    } else if ((t === '--version' || t === '-v') && next) {
      version = next
      i++
    }
  }

  source = source
    .replace(/\.git$/i, '')
    .replace(/^https?:\/\/(?:www\.)?github\.com\//i, '')
    .replace(/^git@github\.com:/i, '')
  const hashIdx = source.indexOf('#')
  if (hashIdx > 0) {
    version = source.slice(hashIdx + 1) || 'main'
    source = source.slice(0, hashIdx)
  }
  const atIdx = source.indexOf('@')
  if (atIdx > 0 && !version) {
    version = source.slice(atIdx + 1) || 'main'
    source = source.slice(0, atIdx)
  }
  if (!name) {
    const parts = source.split('/').filter(Boolean)
    name = parts[parts.length - 1] || ''
  }
  if (!source || !name) return null
  return { name, source, version: version || 'main' }
}

function onNpxCmdInput() {
  const parsed = parseNpxSkillsCmd(npxCmd.value)
  if (!parsed) return
  skillForm.name = parsed.name
  skillForm.source = parsed.source
  skillForm.version = parsed.version
}
async function refresh() {
  await Promise.all([refreshSkills(), refreshPresets(), refreshStatus()])
}
async function installSkill() {
  await run(async () => {
    await api.installSkill({ ...skillForm })
    Object.assign(skillForm, { name: '', source: '', version: '' })
    npxCmd.value = ''
    skillDialog.value = false
    await refreshSkills()
    await refreshStatus()
  }, '技能已安装')
}
async function importSkillZip() {
  const file = skillFile.value
  if (!file) return
  await run(async () => {
    await api.importSkillZip(file, skillForm.name || file.name.replace(/\.zip$/i, ''))
    Object.assign(skillForm, { name: '', source: '', version: '' })
    npxCmd.value = ''
    skillFile.value = undefined
    skillDialog.value = false
    await refreshSkills()
    await refreshStatus()
  }, '技能压缩包已导入')
}
function onSkillFile(event: Event) {
  skillFile.value = (event.target as HTMLInputElement).files?.[0]
}
// 当前正在编辑的预设（null=新建模式）。编辑时 name 字段禁用，避免改名歧义。
const editingPreset = ref<{ name: string } | null>(null)
function openEditPreset(p: {
  name: string
  skills: string[]
  target?: string
  targets?: string[]
}) {
  editingPreset.value = p
  presetForm.name = p.name
  presetForm.selectedSkills = [...p.skills]
  // targets 后端值（"" 表示通用）转回前端 'generic'，便于按钮组回显。
  const rawTargets = p.targets?.length ? p.targets : p.target ? [p.target] : ['']
  presetForm.targets = rawTargets.map((t) => (t === '' ? 'generic' : t))
  presetDialog.value = true
}
function closePresetDialog() {
  presetDialog.value = false
  editingPreset.value = null
  Object.assign(presetForm, { name: '', selectedSkills: [], targets: [] })
}
async function savePreset() {
  if (!presetForm.selectedSkills.length) {
    toast.error('请至少选择一个技能')
    return
  }
  const isEdit = !!editingPreset.value
  await run(
    async () => {
      // 后端 CreatePreset 同名覆盖：编辑时 name 不变即可原地更新；新建时 name 唯一。
      await api.createPreset({
        name: presetForm.name,
        skills: presetForm.selectedSkills,
        targets: presetForm.targets.map((t) => targetToApi(t)),
      })
      closePresetDialog()
      await refreshPresets()
    },
    isEdit ? '预设已更新' : '预设已创建',
  )
}
// 预设目标展示：优先多平台列表（targets），回退旧单值字段（target）。
function presetTargetsLabel(preset: { target?: string; targets?: string[] }): string {
  const list = preset.targets?.length ? preset.targets : preset.target ? [preset.target] : ['']
  return list.map((t) => platformName(t)).join('、') || '通用 (.agents)'
}
async function removeSkill(name: string) {
  if (!(await confirmDialog('移除技能「' + name + '」？'))) return
  await run(async () => {
    await api.deleteSkill(name)
    await refreshSkills()
    await refreshStatus()
  }, '技能已移除')
}
async function applyPreset(name: string) {
  const confirmed = await confirmDialog({
    title: '切换到预设「' + name + '」？',
    description: '当前目录将被备份为 xxx-backup，可在下方「平台技能状态」恢复。',
    confirmText: '切换',
  })
  if (!confirmed) return
  await run(async () => {
    await api.applyPreset(name)
    await refreshStatus()
  }, '预设已切换')
}
async function removePreset(name: string) {
  if (!(await confirmDialog('删除预设「' + name + '」？'))) return
  await run(async () => {
    await api.deletePreset(name)
    await refreshPresets()
  }, '预设已删除')
}
async function syncSkills() {
  if (syncing.value) return
  syncing.value = true
  try {
    const r = await api.syncSkills()
    await refreshSkills()
    await refreshStatus()
    toast.success(`同步完成，共 ${r.synced} 个技能`)
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '同步失败')
  } finally {
    syncing.value = false
  }
}
async function checkUpdates() {
  if (updateStatus.value === 'running') return
  // SSE 连接即触发后端启动更新任务，日志实时推送到「更新日志」Tab。
  openUpdateStream()
}
async function restoreBackup(status: { name: string; dir: string }) {
  const label = platformName(status.name)
  const confirmed = await confirmDialog({
    title: `恢复「${label}」？`,
    description: `将删除当前技能目录，并把 ${status.dir}-backup 还原回去。`,
    confirmText: '恢复',
  })
  if (!confirmed) return
  await run(async () => {
    await api.restoreBackup(status.name)
    await refreshStatus()
    toast.success(`已恢复「${label}」`)
  })
}
async function restoreAllBackups() {
  const confirmed = await confirmDialog({
    title: '恢复所有平台的备份？',
    description: '将删除各平台当前技能目录并还原备份。',
    confirmText: '恢复所有',
  })
  if (!confirmed) return
  await run(async () => {
    const r = await api.restoreAllBackups()
    await refreshStatus()
    toast.success(`已恢复 ${r.restored.length} 个平台`)
  })
}
</script>
<template>
  <div class="h-full flex flex-col gap-6">
    <PageHeader title="Skills" description="安装、移除技能，并保存可快速切换的技能预设。">
      <template #actions
        ><Button variant="outline" :disabled="loading || pending" @click="refresh">
          <RiRefreshLine :class="anyRefreshing ? 'animate-spin' : ''" size="16" />刷新
        </Button>
        <Button variant="outline" :disabled="pending || syncing" @click="syncSkills">
          <RiUploadLine :class="syncing ? 'animate-spin' : ''" size="16" />{{
            syncing ? '同步中…' : '主动同步'
          }}
        </Button>
        <Button
          variant="outline"
          :disabled="pending || updateStatus === 'running'"
          @click="checkUpdates"
        >
          <RiLoaderLine :class="updateStatus === 'running' ? 'animate-spin' : ''" size="16" />{{
            updateStatus === 'running' ? '更新中…' : '检查并更新'
          }}
        </Button>
        <Button variant="outline" @click="presetDialog = true">
          <RiAddLine size="16" />创建预设
        </Button>
        <Button @click="skillDialog = true"> <RiAddLine size="16" />安装技能 </Button>
      </template>
    </PageHeader>
    <LoadingBlock v-if="loading" />
    <TooltipProvider v-else>
      <Tabs v-model="activeTab" class="flex-1 space-y-4">
        <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
          <TabsTrigger value="skills">技能列表</TabsTrigger>
          <TabsTrigger value="presets">预设列表</TabsTrigger>
          <TabsTrigger value="platforms">平台状态</TabsTrigger>
          <TabsTrigger value="logs">更新日志</TabsTrigger>
        </TabsList>
        <TabsContent value="skills" class="space-y-4">
          <Card class="rounded-md">
            <CardHeader>
              <CardTitle class="text-base">技能列表</CardTitle>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="skills?.length" class="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead class="w-32">名称</TableHead>
                      <TableHead>描述</TableHead>
                      <TableHead>来源</TableHead>
                      <TableHead class="w-24">版本</TableHead>
                      <TableHead class="w-28">更新时间</TableHead>
                      <TableHead class="text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="skill in skills" :key="skill.name">
                      <TableCell class="w-32 font-medium">{{ skill.name }}</TableCell>
                      <TableCell class="max-w-xs">
                        <Tooltip v-if="skill.description" :delay-duration="150">
                          <TooltipTrigger as-child>
                            <p class="line-clamp-2 text-sm text-muted-foreground">
                              {{ skill.description }}
                            </p>
                          </TooltipTrigger>
                          <TooltipContent
                            side="bottom"
                            align="start"
                            class="max-w-md whitespace-normal break-words"
                          >
                            {{ skill.description }}
                          </TooltipContent>
                        </Tooltip>
                        <span v-else class="text-sm text-muted-foreground">—</span>
                      </TableCell>
                      <TableCell class="font-mono text-xs">{{ skill.source || '-' }}</TableCell>
                      <TableCell>{{ skill.version || '-' }}</TableCell>
                      <TableCell>
                        <span
                          :class="
                            skill.updated_at
                              ? isRecent(skill.updated_at)
                                ? 'text-green-600'
                                : 'text-muted-foreground'
                              : 'text-muted-foreground'
                          "
                          :title="skill.updated_at || undefined"
                        >
                          {{ timeAgo(skill.updated_at) }}
                        </span>
                        <Badge v-if="isRecent(skill.updated_at)" variant="default" class="ml-1"
                          >近期</Badge
                        >
                      </TableCell>
                      <TableCell class="text-right">
                        <Tooltip>
                          <TooltipTrigger as-child
                            ><Button
                              variant="ghost"
                              size="icon"
                              aria-label="移除技能"
                              @click="removeSkill(skill.name)"
                            >
                              <RiDeleteBinLine size="16" /> </Button
                          ></TooltipTrigger>
                          <TooltipContent>移除技能</TooltipContent>
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                  </TableBody>
                </Table>
              </div>
              <EmptyState v-else title="还没有技能" description="通过来源安装技能。" />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="presets" class="space-y-4">
          <Card class="rounded-md">
            <CardHeader>
              <CardTitle class="text-base">预设列表</CardTitle>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="presets?.length" class="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>名称</TableHead>
                      <TableHead>目标</TableHead>
                      <TableHead>技能</TableHead>
                      <TableHead class="text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="preset in presets" :key="preset.name">
                      <TableCell class="font-medium">{{ preset.name }}</TableCell>
                      <TableCell>
                        <Badge variant="outline">{{ presetTargetsLabel(preset) }}</Badge>
                      </TableCell>
                      <TableCell class="max-w-md text-sm text-muted-foreground">
                        <div class="flex items-start gap-2">
                          <span
                            class="min-w-0 flex-1 whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                          >
                            {{ preset.skills.join(', ') || '-' }}
                          </span>
                          <span
                            v-if="preset.skills.length"
                            class="shrink-0 text-xs tabular-nums text-muted-foreground"
                            :title="`共 ${preset.skills.length} 个技能`"
                          >
                            ({{ preset.skills.length }} 个)
                          </span>
                        </div>
                      </TableCell>
                      <TableCell class="text-right">
                        <div class="flex justify-end gap-2">
                          <Button variant="outline" size="sm" @click="applyPreset(preset.name)"
                            >切换</Button
                          >
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="编辑预设"
                                @click="openEditPreset(preset)"
                              >
                                <RiEditLine size="16" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>编辑预设</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="删除预设"
                                @click="removePreset(preset.name)"
                              >
                                <RiDeleteBinLine size="16" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>删除预设</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                    </TableRow>
                  </TableBody>
                </Table>
              </div>
              <EmptyState
                v-else
                title="还没有预设"
                description="预设用于保存一组可以快速切换的技能。"
              />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="platforms" class="space-y-4">
          <Card class="rounded-md">
            <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle class="text-base">平台技能状态</CardTitle>
              <Button
                variant="destructive"
                size="sm"
                :disabled="pending"
                v-if="skillStatus?.some((s) => s.has_backup)"
                @click="restoreAllBackups"
                >恢复所有</Button
              >
            </CardHeader>
            <CardContent class="p-0">
              <p class="px-4 py-2 text-xs text-muted-foreground">
                切换预设时旧目录会被备份为
                <code class="font-mono">skills-backup</code>；检测到备份后可用「恢复」还原。
              </p>
              <div v-if="skillStatus?.length" class="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>平台</TableHead>
                      <TableHead>技能数</TableHead>
                      <TableHead>备份</TableHead>
                      <TableHead class="text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="st in skillStatus" :key="st.name || 'generic'">
                      <TableCell>
                        <div class="font-medium">{{ platformName(st.name) }}</div>
                        <div class="font-mono text-xs text-muted-foreground">{{ st.dir }}</div>
                      </TableCell>
                      <TableCell>{{ st.count }}</TableCell>
                      <TableCell>
                        <Badge :variant="st.has_backup ? 'default' : 'outline'">{{
                          st.has_backup ? '有备份' : '无'
                        }}</Badge>
                      </TableCell>
                      <TableCell class="text-right">
                        <Button
                          variant="destructive"
                          size="sm"
                          :disabled="pending"
                          v-if="st.has_backup"
                          @click="restoreBackup(st)"
                          >恢复</Button
                        >
                        <span v-else class="text-sm text-muted-foreground">—</span>
                      </TableCell>
                    </TableRow>
                  </TableBody>
                </Table>
              </div>
              <EmptyState
                v-else
                title="暂无平台信息"
                description="安装技能后这里会显示各平台的技能数量与备份状态。"
              />
            </CardContent>
          </Card>
        </TabsContent>
        <TabsContent value="logs" class="space-y-4">
          <Card class="h-full rounded-md self-start">
            <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
              <CardTitle class="text-base">更新日志</CardTitle>
              <Badge
                :variant="
                  updateStatus === 'done'
                    ? 'default'
                    : updateStatus === 'error'
                      ? 'destructive'
                      : updateStatus === 'running'
                        ? 'secondary'
                        : 'outline'
                "
                >{{ updateStatusLabel }}</Badge
              >
            </CardHeader>
            <CardContent class="p-0">
              <div
                ref="logBox"
                class="min-h-[320px] space-y-1 overflow-y-auto bg-muted/50 p-3 font-mono text-xs"
              >
                <div
                  v-for="(line, i) in logLines"
                  :key="i"
                  class="whitespace-pre-wrap break-all text-foreground"
                  v-html="ansiToHtml(line)"
                />
                <div v-if="!logLines.length" class="text-muted-foreground">
                  暂无日志。点击右上角「检查并更新」开始，命令输出将实时显示在这里。
                </div>
              </div>
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </TooltipProvider>
    <Dialog v-model:open="skillDialog">
      <DialogContent class="sm:max-w-xl!">
        <DialogHeader>
          <DialogTitle>安装技能</DialogTitle>
        </DialogHeader>
        <form class="space-y-3" @submit.prevent="installSkill">
          <div class="space-y-1">
            <Label for="skill-cmd">npx 安装命令（粘贴后自动解析）</Label>
            <Input
              id="skill-cmd"
              v-model="npxCmd"
              placeholder="npx skills add owner/repo --skill my-skill"
              @input="onNpxCmdInput"
            />
          </div>
          <div class="space-y-1">
            <Label for="skill-name">技能名</Label
            ><Input id="skill-name" v-model="skillForm.name" required placeholder="git-tools" />
          </div>
          <div class="space-y-1">
            <Label for="skill-source">来源</Label
            ><Input
              id="skill-source"
              v-model="skillForm.source"
              required
              placeholder="owner/repo"
            />
          </div>
          <div class="space-y-1">
            <Label for="skill-version">版本或分支</Label
            ><Input id="skill-version" v-model="skillForm.version" placeholder="main" />
          </div>
          <div class="space-y-1 border-t border-border pt-3">
            <Label for="skill-zip">或导入 ZIP 包</Label
            ><Input
              id="skill-zip"
              type="file"
              accept=".zip,application/zip"
              @change="onSkillFile"
            />
          </div>
          <DialogFooter>
            <Button type="submit" :disabled="pending">安装</Button>
            <Button
              type="button"
              variant="secondary"
              :disabled="pending || !skillFile"
              @click="importSkillZip"
              >导入 ZIP</Button
            >
            <Button type="button" variant="outline" @click="skillDialog = false">取消</Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="presetDialog" @update:open="(o: boolean) => !o && closePresetDialog()">
      <DialogContent class="sm:max-w-8/10! lg:max-w-6/10! lg:max-h-8/10 min-w-0 overflow-hidden">
        <DialogHeader>
          <DialogTitle>{{ editingPreset ? '编辑预设' : '创建预设' }}</DialogTitle>
        </DialogHeader>
        <form class="w-full min-w-0 space-y-4" @submit.prevent="savePreset">
          <div class="space-y-1">
            <Label for="preset-name">预设名</Label
            ><Input
              id="preset-name"
              v-model="presetForm.name"
              required
              placeholder="编程向"
              :disabled="!!editingPreset"
            />
            <p v-if="editingPreset" class="text-xs text-muted-foreground">
              预设名不可修改（重命名请先删除再新建）
            </p>
          </div>
          <div class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>技能清单</Label>
              <div class="flex rounded-md border border-border p-0.5">
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  :class="skillDisplayMode === 'tag' ? 'bg-muted' : ''"
                  @click="skillDisplayMode = 'tag'"
                >
                  标签
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="ghost"
                  :class="skillDisplayMode === 'detail' ? 'bg-muted' : ''"
                  @click="skillDisplayMode = 'detail'"
                >
                  详情
                </Button>
              </div>
            </div>
            <template v-if="skills?.length">
              <!-- 标签模式：点击切换选中；悬停 3s 显示描述 -->
              <div
                v-if="skillDisplayMode === 'tag'"
                class="flex max-h-56 flex-wrap gap-2 overflow-y-auto rounded-md border border-border p-3"
              >
                <template v-for="skill in skills" :key="skill.name">
                  <Tooltip v-if="skill.description" :delay-duration="3000">
                    <TooltipTrigger as-child>
                      <Button
                        type="button"
                        size="sm"
                        :variant="
                          presetForm.selectedSkills.includes(skill.name) ? 'default' : 'outline'
                        "
                        @click="toggleSkill(skill.name)"
                      >
                        {{ skill.name }}
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent side="bottom" class="max-w-md whitespace-normal break-words">
                      {{ skill.description }}
                    </TooltipContent>
                  </Tooltip>
                  <Button
                    v-else
                    type="button"
                    size="sm"
                    :variant="
                      presetForm.selectedSkills.includes(skill.name) ? 'default' : 'outline'
                    "
                    @click="toggleSkill(skill.name)"
                  >
                    {{ skill.name }}
                  </Button>
                </template>
              </div>
              <!-- 详情模式：名称+描述，一个技能一行 -->
              <div
                v-else
                class="max-h-72 space-y-1 overflow-y-auto rounded-md border border-border p-2"
              >
                <button
                  v-for="skill in skills"
                  :key="skill.name"
                  type="button"
                  class="flex w-full min-w-0 items-start gap-3 rounded-md px-3 py-2 text-left text-sm transition-colors"
                  :class="
                    presetForm.selectedSkills.includes(skill.name)
                      ? 'bg-primary/10 text-primary ring-1 ring-inset ring-primary/30'
                      : 'hover:bg-muted'
                  "
                  @click="toggleSkill(skill.name)"
                >
                  <span
                    class="mt-0.5 flex h-4 w-4 shrink-0 items-center justify-center rounded-sm border"
                    :class="
                      presetForm.selectedSkills.includes(skill.name)
                        ? 'border-primary bg-primary text-primary-foreground'
                        : 'border-border'
                    "
                  >
                    <svg
                      v-if="presetForm.selectedSkills.includes(skill.name)"
                      viewBox="0 0 12 12"
                      class="h-3 w-3"
                      fill="none"
                      stroke="currentColor"
                      stroke-width="2"
                      stroke-linecap="round"
                      stroke-linejoin="round"
                    >
                      <path d="M2.5 6.5l2.5 2.5 4.5-5.5" />
                    </svg>
                  </span>
                  <span class="min-w-0">
                    <span class="block font-medium">{{ skill.name }}</span>
                    <Tooltip v-if="skill.description" :delay-duration="150">
                      <TooltipTrigger as-child>
                        <span class="block truncate text-xs text-muted-foreground">
                          {{ skill.description }}
                        </span>
                      </TooltipTrigger>
                      <TooltipContent
                        side="bottom"
                        align="start"
                        class="max-w-md whitespace-normal break-words"
                      >
                        {{ skill.description }}
                      </TooltipContent>
                    </Tooltip>
                  </span>
                </button>
              </div>
            </template>
            <p v-else class="text-sm text-muted-foreground">
              暂无可选技能，请先在「技能列表」中安装技能。
            </p>
            <p class="text-xs text-muted-foreground">
              点击切换选中，已选 {{ presetForm.selectedSkills.length }} 个
            </p>
          </div>
          <div class="space-y-2">
            <Label>目标平台（可多选）</Label>
            <div class="flex flex-wrap gap-2">
              <Button
                v-for="opt in platformOptions"
                :key="opt.value"
                type="button"
                size="sm"
                :variant="presetForm.targets.includes(opt.value) ? 'default' : 'outline'"
                @click="toggleTarget(opt.value)"
              >
                {{ opt.label }}
              </Button>
            </div>
            <p class="text-xs text-muted-foreground">未选择时默认通用（.agents）</p>
          </div>
          <DialogFooter
            ><Button type="submit" :disabled="pending">{{
              editingPreset ? '保存' : '创建预设'
            }}</Button
            ><Button type="button" variant="outline" @click="closePresetDialog()"
              >取消</Button
            ></DialogFooter
          >
        </form>
      </DialogContent>
    </Dialog>
  </div>
</template>
