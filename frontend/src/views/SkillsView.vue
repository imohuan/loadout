<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import {
  RiAddLine,
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiClipboardLine,
  RiDeleteBinLine,
  RiEditLine,
  RiGroup2Line,
  RiListUnordered,
  RiLoader4Line,
  RiLoaderLine,
  RiRefreshLine,
  RiTextWrap,
  RiUploadLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import { useManagementApi } from '@/composables/useManagementApi'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useTask, startTask } from '@/composables/useTask'
import { useConfirm } from '@/composables/useConfirm'
import BulkSelectButtons from '@/components/BulkSelectButtons.vue'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import TranslateText from '@/components/TranslateText.vue'
import { useTranslateStore } from '@/stores/translate'
const api = useManagementApi()
const translateStore = useTranslateStore()
const {
  data: skills,
  loading: skillsLoading,
  refreshing: skillsRefreshing,
  refresh: refreshSkills,
} = useListLoader(api.skills)
// 技能列表加载后，批量查询数据库已有译文并灌入 store，TranslateText 即时显示（只读，不触发翻译）
watch(
  () => skills.value,
  (list) => {
    if (!list || !list.length) return
    const texts: { text: string; textKey: string }[] = []
    for (const s of list) {
      if (s.description) texts.push({ text: s.description, textKey: 'skill:' + s.name })
    }
    if (texts.length) void translateStore.lookupBatch(texts)
  },
  { immediate: true },
)
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
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()
// 后台「检查并更新技能」任务：注册收尾动作，更新结束（done）时自动刷新技能列表与平台状态。
const skillUpdateRunning = useTask('skill-update', {
  kind: 'skill',
  onDone: () => {
    void refreshSkills()
    void refreshStatus()
  },
  onError: (err) => toast.error('技能更新失败', { description: String(err) }),
}).isRunning
const activeTab = ref('skills')
const skillDialog = ref(false)
const presetDialog = ref(false)
// 技能删除弹窗：单个删除按钮 + switch 控制是否连同删除本地技能
const deleteDialog = ref(false)
const deleteTarget = ref('')
const deleteLocal = ref(false)
function openDeleteDialog(name: string) {
  deleteTarget.value = name
  deleteLocal.value = false
  deleteDialog.value = true
}
// 复制技能目录绝对路径到剪贴板。优先用后端返回的 path 字段；缺 path 时回退本地拼接。
async function copySkillPath(name: string, path?: string) {
  const target = path && path.trim() ? path : name
  try {
    await navigator.clipboard.writeText(target)
    toast.success('路径已复制', { description: target })
  } catch (e) {
    toast.error('复制失败', { description: e instanceof Error ? e.message : String(e) })
  }
}
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
// 批量操作：全选当前技能列表全部、反选、清空已选。
function selectAllSkills() {
  if (!skills.value) return
  for (const s of skills.value) {
    if (!presetForm.selectedSkills.includes(s.name)) {
      presetForm.selectedSkills.push(s.name)
    }
  }
}
function invertSkills() {
  if (!skills.value) return
  const current = new Set(presetForm.selectedSkills)
  presetForm.selectedSkills.length = 0
  for (const s of skills.value) {
    if (!current.has(s.name)) presetForm.selectedSkills.push(s.name)
  }
}
function clearSkills() {
  presetForm.selectedSkills.length = 0
}
// 当前技能列表是否已全部选中（用于全选按钮的切换态）。
const allSkillsSelected = computed(() => {
  if (!skills.value || !skills.value.length) return false
  return skills.value.every((s) => presetForm.selectedSkills.includes(s.name))
})
const loading = computed(() => skillsLoading.value || presetsLoading.value)
const anyRefreshing = computed(
  () => skillsRefreshing.value || presetsRefreshing.value || statusRefreshing.value,
)
// 独立操作 loading（各自按钮显示旋转图标，互不干扰）。
const syncing = ref(false)
const npxCmd = ref('')

// 表格描述自动换行开关（默认开启；关闭后描述只显示一行）
const wrapDescription = ref(true)
// ===== 按来源聚合视图（折叠表格）=====
const groupBySource = ref(false)
const expandedSources = ref<string[]>([])
// 空 source 统一归到一个稳定 key（避免空字符串与字面 "-" 串撞到不同组）。
const UNSPECIFIED_SOURCE = '__unspecified__'
type SkillGroup = {
  key: string
  displaySource: string
  firstDescription: string
  skills: NonNullable<typeof skills.value>
}
const groupedSkills = computed<SkillGroup[]>(() => {
  if (!skills.value) return []
  const map = new Map<string, SkillGroup>()
  for (const s of skills.value) {
    const raw = s.source || ''
    const isEmpty = raw === '' || raw === '-'
    const key = isEmpty ? UNSPECIFIED_SOURCE : raw
    const display = isEmpty ? '未指定来源' : raw
    if (!map.has(key)) {
      map.set(key, { key, displaySource: display, firstDescription: s.description || '', skills: [] })
    }
    map.get(key)!.skills.push(s)
  }
  return Array.from(map.values())
})
function toggleSourceGroup(key: string) {
  const i = expandedSources.value.indexOf(key)
  if (i >= 0) expandedSources.value.splice(i, 1)
  else expandedSources.value.push(key)
}
function isSourceExpanded(key: string) {
  return expandedSources.value.includes(key)
}
function toggleGroupBySource() {
  groupBySource.value = !groupBySource.value
  // 切换模式时清空展开状态，避免下次进入聚合态还残留旧展开项。
  expandedSources.value = []
}

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
  await run(
    'install-skill',
    async () => {
      await api.installSkill({ ...skillForm })
      Object.assign(skillForm, { name: '', source: '', version: '' })
      npxCmd.value = ''
      skillDialog.value = false
      await refreshSkills()
      await refreshStatus()
    },
    '技能已安装',
  )
}
async function importSkillZip() {
  const file = skillFile.value
  if (!file) return
  await run(
    'import-skill',
    async () => {
      await api.importSkillZip(file, skillForm.name || file.name.replace(/\.zip$/i, ''))
      Object.assign(skillForm, { name: '', source: '', version: '' })
      npxCmd.value = ''
      skillFile.value = undefined
      skillDialog.value = false
      await refreshSkills()
      await refreshStatus()
    },
    '技能压缩包已导入',
  )
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
    'save-preset',
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
async function confirmDeleteSkill() {
  const name = deleteTarget.value
  if (!name) return
  deleteDialog.value = false
  const deleteLocalSkill = deleteLocal.value
  if (deleteLocalSkill) {
    // 同时删除技能源与本地技能
    await run(
      `skill:${name}:remove`,
      async () => {
        await api.unregisterSkill(name)
        await api.deleteSkill(name)
        await refreshSkills()
        await refreshStatus()
      },
      '技能已删除',
    )
  } else {
    // 只删除技能源（~/.loadout/skills 文件夹）
    await run(
      `skill:${name}:unregister`,
      async () => {
        await api.unregisterSkill(name)
        await refreshSkills()
        await refreshStatus()
      },
      '技能源已删除',
    )
  }
}
async function applyPreset(name: string) {
  const confirmed = await confirmDialog({
    title: '切换到预设「' + name + '」？',
    description: '当前目录将被备份为 xxx-backup，可在下方「平台技能状态」恢复。',
    confirmText: '切换',
  })
  if (!confirmed) return
  await run(
    `preset:${name}:apply`,
    async () => {
      await api.applyPreset(name)
      await refreshStatus()
    },
    '预设已切换',
  )
}
async function removePreset(name: string) {
  if (!(await confirmDialog('删除预设「' + name + '」？'))) return
  await run(
    `preset:${name}:remove`,
    async () => {
      await api.deletePreset(name)
      await refreshPresets()
    },
    '预设已删除',
  )
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
  // 触发后端检查并更新技能（后台 procreg 任务，日志显示在底部进程面板）。
  // 加载态与结束收尾由 useTask('skill-update') 统一管理。
  await startTask({
    id: 'skill-update',
    kind: 'skill',
    run: () => api.checkSkillUpdates('skill-update'),
  })
}

async function restoreBackup(status: { name: string; dir: string }) {
  const label = platformName(status.name)
  const confirmed = await confirmDialog({
    title: `恢复「${label}」？`,
    description: `将删除当前技能目录，并把 ${status.dir}-backup 还原回去。`,
    confirmText: '恢复',
  })
  if (!confirmed) return
  await run(`platform:${status.name}:restore`, async () => {
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
  await run('restore-all', async () => {
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
        ><Button variant="outline" :disabled="loading || anyRefreshing" @click="refresh">
          <RiRefreshLine :class="anyRefreshing ? 'size-4 animate-spin' : 'size-4'" />刷新
        </Button>
        <Button variant="outline" :disabled="syncing" @click="syncSkills">
          <RiLoaderLine v-if="syncing" class="size-4 animate-spin" /><RiUploadLine v-else class="size-4" />{{
            syncing ? '同步中…' : '主动同步'
          }}
        </Button>
        <Button variant="outline" :disabled="skillUpdateRunning" @click="checkUpdates">
          <RiLoaderLine :class="skillUpdateRunning ? 'size-4 animate-spin' : 'size-4'" />{{
            skillUpdateRunning ? '更新中…' : '检查并更新'
          }}
        </Button>
        <Button variant="outline" @click="presetDialog = true">
          <RiAddLine class="size-4" />创建预设
        </Button>
        <Button @click="skillDialog = true"> <RiAddLine class="size-4" />安装技能 </Button>
      </template>
    </PageHeader>
    <LoadingBlock v-if="loading" />
    <TooltipProvider v-else>
      <Tabs v-model="activeTab" class="flex-1 space-y-4">
        <div class="flex items-center justify-between gap-2">
          <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
            <TabsTrigger value="skills">技能列表</TabsTrigger>
            <TabsTrigger value="presets">预设列表</TabsTrigger>
            <TabsTrigger value="platforms">平台状态</TabsTrigger>
          </TabsList>
          <Tooltip :delay-duration="150">
            <TooltipTrigger as-child>
              <Button
                variant="ghost"
                size="icon"
                :aria-label="wrapDescription ? '关闭表格描述自动换行' : '开启表格描述自动换行'"
                :aria-pressed="wrapDescription"
                @click="wrapDescription = !wrapDescription"
              >
                <RiTextWrap v-if="wrapDescription" class="size-4" />
                <RiTextWrap v-else class="size-4 text-muted-foreground opacity-60" />
              </Button>
            </TooltipTrigger>
            <TooltipContent side="bottom">
              {{ wrapDescription ? '关闭自动换行（描述只显示一行）' : '开启自动换行' }}
            </TooltipContent>
          </Tooltip>
        </div>
        <TabsContent value="skills" class="space-y-4">
          <Card class="rounded-md">
            <CardHeader>
              <CardTitle class="text-base">技能列表</CardTitle>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="skills?.length" class="overflow-x-auto">
                <Table class="table-fixed w-full min-w-[60rem]">
                  <TableHeader v-if="!groupBySource">
                    <TableRow>
                      <TableHead class="w-48">名称</TableHead>
                      <TableHead class="min-w-72">描述</TableHead>
                      <TableHead class="w-48">
                        <div class="flex items-center gap-1">
                          <span>来源</span>
                          <Tooltip>
                            <TooltipTrigger as-child>
                              <Button
                                variant="ghost"
                                size="icon"
                                class="size-6"
                                aria-label="按来源聚合"
                                @click="toggleGroupBySource"
                              >
                                <RiGroup2Line class="size-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>按来源聚合</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableHead>
                      <TableHead class="w-28">版本</TableHead>
                      <TableHead class="w-42">更新时间</TableHead>
                      <TableHead class="w-12 text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableHeader v-else>
                    <TableRow>
                      <TableHead class="w-12"></TableHead>
                      <TableHead class="w-32">
                        <div class="flex items-center gap-1">
                          <span>来源</span>
                          <Tooltip>
                            <TooltipTrigger as-child>
                              <Button
                                variant="ghost"
                                size="icon"
                                class="size-6"
                                aria-label="取消按来源聚合"
                                @click="toggleGroupBySource"
                              >
                                <RiListUnordered class="size-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>取消按来源聚合</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableHead>
                      <TableHead class="min-w-72">描述</TableHead>
                      <TableHead class="w-24">技能数</TableHead>
                      <TableHead class="w-12 text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody v-if="!groupBySource">
                    <TableRow v-for="skill in skills" :key="skill.name">
                      <TableCell class="w-48 font-medium truncate">{{ skill.name }}</TableCell>
                      <TableCell class="min-w-72" :class="wrapDescription ? 'break-words whitespace-normal' : 'truncate'">
                        <TranslateText
                          v-if="skill.description"
                          :source="skill.description"
                          source-type="skill"
                          :source-id="skill.name"
                          class="line-clamp-2 text-sm text-muted-foreground" :single-line="!wrapDescription"
                        />
                        <span v-else class="text-sm text-muted-foreground">—</span>
                      </TableCell>
                      <TableCell class="w-48 font-mono text-xs truncate">{{ skill.source || '-' }}</TableCell>
                      <TableCell class="w-28">{{ skill.version || '-' }}</TableCell>
                      <TableCell class="w-32">
                        <div class="inline-flex items-center gap-1 whitespace-nowrap overflow-hidden">
                          <span
                            :class="
                              skill.updated_at
                                ? isRecent(skill.updated_at)
                                  ? 'text-green-600'
                                  : 'text-muted-foreground'
                                : 'text-muted-foreground'
                            "
                          >
                            {{ timeAgo(skill.updated_at) }}
                          </span>
                          <Badge v-if="isRecent(skill.updated_at)" variant="default"
                            >近期</Badge
                          >
                        </div>
                      </TableCell>
                      <TableCell class="w-12 text-right">
                        <div class="flex justify-end">
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="复制技能路径"
                                @click="copySkillPath(skill.name, skill.path)"
                              >
                                <RiClipboardLine class="size-4" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>复制技能路径</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                class="text-destructive hover:text-destructive"
                                aria-label="删除技能"
                                :disabled="
                                  isPending(`skill:${skill.name}:unregister`) ||
                                  isPending(`skill:${skill.name}:remove`)
                                "
                                @click="openDeleteDialog(skill.name)"
                              >
                                <RiLoader4Line
                                  v-if="
                                    isPending(`skill:${skill.name}:unregister`) ||
                                    isPending(`skill:${skill.name}:remove`)
                                  "
                                  class="animate-spin"
                                  size="16"
                                /><RiDeleteBinLine v-else class="size-4" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>删除技能</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                    </TableRow>
                  </TableBody>
                  <TableBody v-else>
                    <template v-for="group in groupedSkills" :key="group.key">
                      <TableRow>
                        <TableCell class="w-12 px-0">
                          <Button
                            variant="ghost"
                            size="icon"
                            class="size-8"
                            :aria-label="
                              isSourceExpanded(group.key) ? '收起技能列表' : '展开技能列表'
                            "
                            :aria-expanded="isSourceExpanded(group.key)"
                            @click="toggleSourceGroup(group.key)"
                          >
                            <RiArrowDownSLine
                              v-if="isSourceExpanded(group.key)"
                              size="16"
                            />
                            <RiArrowRightSLine v-else class="size-4" />
                          </Button>
                        </TableCell>
                        <TableCell class="w-32">
                          <div class="min-w-0">
                            <div class="truncate font-mono text-sm">
                              {{ group.displaySource }}
                            </div>
                            <div class="text-xs text-muted-foreground">
                              {{ group.skills.length }} 个技能
                            </div>
                          </div>
                        </TableCell>
                        <TableCell class="min-w-72">
                          <div class="min-w-0">
                          <Tooltip
                            v-if="group.firstDescription"
                            :delay-duration="150"
                          >
                            <TooltipTrigger as-child>
                              <p class="truncate text-sm text-muted-foreground">
                                <TranslateText
                                  :source="group.firstDescription"
                                  source-type="skill"
                                  :source-id="group.skills[0]?.name || ''"
                                  single-line
                                />
                              </p>
                            </TooltipTrigger>
                            <TooltipContent
                              side="bottom"
                              align="start"
                              class="max-w-md whitespace-normal break-words "
                            >
                              <TranslateText
                                :source="group.firstDescription"
                                source-type="skill"
                                :source-id="group.skills[0]?.name || ''"
                                />
                            </TooltipContent>
                          </Tooltip>
                          <span v-else class="text-sm text-muted-foreground">—</span>
                          </div>
                        </TableCell>
                        <TableCell class="w-24">
                          <Badge variant="secondary">{{ group.skills.length }} 个</Badge>
                        </TableCell>
                        <TableCell class="w-12 text-right text-sm text-muted-foreground">—</TableCell>
                      </TableRow>
                      <TableRow
                        v-if="isSourceExpanded(group.key)"
                        class="bg-muted/30 hover:bg-muted/30"
                      >
                        <TableCell :colspan="5" class="whitespace-normal p-0 w-full overflow-hidden">
                          <Table class="table-fixed w-full min-w-[48rem]">
                            <TableBody>
                              <TableRow
                                v-for="skill in group.skills"
                                :key="skill.name"
                                class="hover:bg-transparent"
                              >
                                <TableCell class="w-48 font-medium truncate">{{ skill.name }}</TableCell>
                                <TableCell class="min-w-72">
                                  <div class="min-w-0">
                                  <Tooltip v-if="skill.description" :delay-duration="150">
                                    <TooltipTrigger as-child>
                                      <p class="truncate text-sm text-muted-foreground">
                                        <TranslateText
                                          :source="skill.description"
                                          source-type="skill"
                                          :source-id="skill.name"
                                          single-line
                                        />
                                      </p>
                                    </TooltipTrigger>
                                    <TooltipContent
                                      side="bottom"
                                      align="start"
                                      class="max-w-md whitespace-normal break-words"
                                    >
                                      <TranslateText
                                        :source="skill.description"
                                        source-type="skill"
                                        :source-id="skill.name"
                                        />
                                    </TooltipContent>
                                  </Tooltip>
                                  <span v-else class="text-sm text-muted-foreground">—</span>
                                  </div>
                                </TableCell>
                                <TableCell class="w-32">{{ skill.version || '-' }}</TableCell>
                                <TableCell class="w-32">
                                  <div class="inline-flex items-center gap-1 whitespace-nowrap overflow-hidden">
                                    <span
                                      :class="
                                        skill.updated_at
                                          ? isRecent(skill.updated_at)
                                            ? 'text-green-600'
                                            : 'text-muted-foreground'
                                          : 'text-muted-foreground'
                                      "
                                    >
                                      {{ timeAgo(skill.updated_at) }}
                                    </span>
                                    <Badge
                                      v-if="isRecent(skill.updated_at)"
                                      variant="default"
                                      >近期</Badge
                                    >
                                  </div>
                                </TableCell>
                                <TableCell class="w-12 text-right">
                                  <div class="flex justify-end">
                                    <Tooltip>
                                      <TooltipTrigger as-child
                                        ><Button
                                          variant="ghost"
                                          size="icon"
                                          aria-label="复制技能路径"
                                          @click="copySkillPath(skill.name, skill.path)"
                                        >
                                          <RiClipboardLine class="size-4" /> </Button
                                      ></TooltipTrigger>
                                      <TooltipContent>复制技能路径</TooltipContent>
                                    </Tooltip>
                                    <Tooltip>
                                      <TooltipTrigger as-child
                                        ><Button
                                          variant="ghost"
                                          size="icon"
                                          class="text-destructive hover:text-destructive"
                                          aria-label="删除技能"
                                          :disabled="
                                            isPending(`skill:${skill.name}:unregister`) ||
                                            isPending(`skill:${skill.name}:remove`)
                                          "
                                          @click="openDeleteDialog(skill.name)"
                                        >
                                          <RiLoader4Line
                                            v-if="
                                              isPending(`skill:${skill.name}:unregister`) ||
                                              isPending(`skill:${skill.name}:remove`)
                                            "
                                            class="animate-spin"
                                            size="16"
                                          /><RiDeleteBinLine v-else class="size-4" /> </Button
                                      ></TooltipTrigger>
                                      <TooltipContent>删除技能</TooltipContent>
                                    </Tooltip>
                                  </div>
                                </TableCell>
                              </TableRow>
                            </TableBody>
                          </Table>
                        </TableCell>
                      </TableRow>
                    </template>
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
                <Table class="table-fixed w-full">
                  <TableHeader>
                    <TableRow>
                      <TableHead class="w-32">名称</TableHead>
                      <TableHead class="w-40">目标</TableHead>
                      <TableHead>技能</TableHead>
                      <TableHead class="w-48 text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="preset in presets" :key="preset.name">
                      <TableCell class="w-32 truncate font-medium">{{ preset.name }}</TableCell>
                      <TableCell class="w-40">
                        <Badge variant="outline">{{ presetTargetsLabel(preset) }}</Badge>
                      </TableCell>
                      <TableCell class="w-fit text-sm text-muted-foreground">
                        <div class="flex items-start gap-2">
                          <span
                            class="min-w-0 flex-1 whitespace-pre-wrap break-words [display:-webkit-box] [-webkit-line-clamp:2] [-webkit-box-orient:vertical] overflow-hidden"
                          >
                            {{ preset.skills.join(', ') || '-' }}
                          </span>
                          <Tooltip v-if="preset.skills.length">
                            <TooltipTrigger as-child>
                              <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
                                ({{ preset.skills.length }} 个)
                              </span>
                            </TooltipTrigger>
                            <TooltipContent>共 {{ preset.skills.length }} 个技能</TooltipContent>
                          </Tooltip>
                        </div>
                      </TableCell>
                      <TableCell class="w-48 text-right">
                        <div class="flex justify-end gap-2">
                          <Button
                            variant="outline"
                            size="sm"
                            :disabled="isPending(`preset:${preset.name}:apply`)"
                            @click="applyPreset(preset.name)"
                            ><RiLoader4Line
                              v-if="isPending(`preset:${preset.name}:apply`)"
                              class="animate-spin"
                              size="16"
                            />切换</Button
                          >
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="编辑预设"
                                @click="openEditPreset(preset)"
                              >
                                <RiEditLine class="size-4" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>编辑预设</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="删除预设"
                                :disabled="isPending(`preset:${preset.name}:remove`)"
                                @click="removePreset(preset.name)"
                              >
                                <RiLoader4Line
                                  v-if="isPending(`preset:${preset.name}:remove`)"
                                  class="animate-spin"
                                  size="16"
                                /><RiDeleteBinLine v-else class="size-4" /> </Button
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
                :disabled="isPending('restore-all')"
                v-if="skillStatus?.some((s) => s.has_backup)"
                @click="restoreAllBackups"
                ><RiLoader4Line
                  v-if="isPending('restore-all')"
                  class="animate-spin"
                  size="16"
                />恢复所有</Button
              >
            </CardHeader>
            <CardContent class="p-0">
              <p class="px-4 py-2 text-xs text-muted-foreground">
                切换预设时旧目录会被备份为
                <code class="font-mono">skills-backup</code>；检测到备份后可用「恢复」还原。
              </p>
              <div v-if="skillStatus?.length" class="overflow-x-auto">
                <Table class="table-fixed w-full">
                  <TableHeader>
                    <TableRow>
                      <TableHead class="w-64">平台</TableHead>
                      <TableHead class="w-24">技能数</TableHead>
                      <TableHead class="w-24">备份</TableHead>
                      <TableHead class="w-24 text-right">操作</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    <TableRow v-for="st in skillStatus" :key="st.name || 'generic'">
                      <TableCell class="w-64">
                        <div class="truncate font-medium">{{ platformName(st.name) }}</div>
                        <div class="truncate font-mono text-xs text-muted-foreground">{{ st.dir }}</div>
                      </TableCell>
                      <TableCell class="w-24">{{ st.count }}</TableCell>
                      <TableCell class="w-24">
                        <Badge :variant="st.has_backup ? 'default' : 'outline'">{{
                          st.has_backup ? '有备份' : '无'
                        }}</Badge>
                      </TableCell>
                      <TableCell class="w-24 text-right">
                        <Button
                          variant="destructive"
                          size="sm"
                          :disabled="isPending(`platform:${st.name || 'generic'}:restore`)"
                          v-if="st.has_backup"
                          @click="restoreBackup(st)"
                          ><RiLoader4Line
                            v-if="isPending(`platform:${st.name || 'generic'}:restore`)"
                            class="animate-spin"
                            size="16"
                          />恢复</Button
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
            <Button type="submit" :disabled="isPending('install-skill')">
              <RiLoader4Line v-if="isPending('install-skill')" class="size-4 animate-spin" />安装
            </Button>
            <Button
              type="button"
              variant="secondary"
              :disabled="isPending('import-skill') || !skillFile"
              @click="importSkillZip"
              ><RiLoader4Line v-if="isPending('import-skill')" class="size-4 animate-spin" />导入
              ZIP</Button
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
              <div class="flex items-center gap-3">
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
            </div>
            <template v-if="skills?.length">
              <!-- 标签模式：点击切换选中；悬停 3s 显示描述 -->
              <div
                v-if="skillDisplayMode === 'tag'"
                class="flex max-h-56 flex-wrap gap-2 overflow-y-auto rounded-md border border-border p-3"
              >
                <template v-for="skill in skills" :key="skill.name">
                  <Tooltip v-if="skill.description" :delay-duration="1000">
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
                      <TranslateText
                        :source="skill.description"
                        source-type="skill"
                        :source-id="skill.name"
                        />
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
                          <TranslateText
                            :source="skill.description"
                            source-type="skill"
                            :source-id="skill.name"
                            single-line
                          />
                        </span>
                      </TooltipTrigger>
                      <TooltipContent
                        side="bottom"
                        align="start"
                        class="max-w-md whitespace-normal break-words "
                      >
                        <TranslateText
                          :source="skill.description"
                          source-type="skill"
                          :source-id="skill.name"
                          />
                      </TooltipContent>
                    </Tooltip>
                  </span>
                </button>
              </div>
            </template>
            <p v-else class="text-sm text-muted-foreground">
              暂无可选技能，请先在「技能列表」中安装技能。
            </p>
            <!-- 已选数量 + 批量操作\uff08全选/反选/清空\uff09\uff0c在列表容器与提示文字之间\uff0c按需求可隐藏当前梢式的全部\uff0c不依赖显示模式 -->
            <div class="flex items-center justify-between">
              <p class="text-xs text-muted-foreground">
                点击切换选中，已选 {{ presetForm.selectedSkills.length }} 个
              </p>
              <BulkSelectButtons
                :all-selected="allSkillsSelected"
                :can-operate="(skills?.length ?? 0) > 0"
                :has-selection="presetForm.selectedSkills.length > 0"
                @select-all="selectAllSkills"
                @invert="invertSkills"
                @clear="clearSkills"
              />
            </div>
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
            ><Button type="submit" :disabled="isPending('save-preset')"
              ><RiLoader4Line v-if="isPending('save-preset')" class="size-4 animate-spin" />{{
                editingPreset ? '保存' : '创建预设'
              }}</Button
            ><Button type="button" variant="outline" @click="closePresetDialog()"
              >取消</Button
            ></DialogFooter
          >
        </form>
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="deleteDialog">
      <DialogContent class="sm:max-w-md!">
        <DialogHeader>
          <DialogTitle>删除技能「{{ deleteTarget }}」</DialogTitle>
          <DialogDescription class="text-sm text-muted-foreground">
            删除技能源会移除 ~/.loadout/skills 里对应的文件夹。
          </DialogDescription>
        </DialogHeader>
        <div class="flex items-center justify-between rounded-md border border-border p-3">
          <div>
            <Label>删除本地资源</Label>
            <p class="text-xs text-muted-foreground">
              开启后连同删除本地技能（~/.agents/skills 文件夹）
            </p>
          </div>
          <Switch v-model="deleteLocal" />
        </div>
        <DialogFooter>
          <Button variant="destructive" :disabled="isPending(`skill:${deleteTarget}:unregister`) || isPending(`skill:${deleteTarget}:remove`)" @click="confirmDeleteSkill">
            <RiLoader4Line
              v-if="isPending(`skill:${deleteTarget}:unregister`) || isPending(`skill:${deleteTarget}:remove`)"
              class="size-4 animate-spin"
            />删除
          </Button>
          <Button type="button" variant="outline" @click="deleteDialog = false">取消</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
