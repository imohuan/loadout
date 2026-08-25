<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiCloseLine,
  RiDeleteBinLine,
  RiEditLine,
  RiImportLine,
  RiLoader4Line,
  RiPlayLine,
  RiQuestionLine,
  RiRefreshLine,
  RiSettings3Line,
} from '@remixicon/vue'
import PageHeader from '@/components/PageHeader.vue'
import { useConfirm } from '@/composables/useConfirm'
import PlatformCard from '@/components/unifyai/PlatformCard.vue'
import ExcludeMatrix from '@/components/unifyai/ExcludeMatrix.vue'
import CommandPreview from '@/components/unifyai/CommandPreview.vue'
import StreamLogPanel, { type StreamStatus } from '@/components/StreamLogPanel.vue'
import {
  DEFAULT_SOURCE,
  IMPORT_MCP_ARGS,
  INITIAL_MCP_SERVERS,
  INITIAL_MODEL_SOURCE,
  INITIAL_OPENCODEX_MODELS,
  PLATFORMS,
  SYNC_CONFIG_PATH,
  type BackendPlatform,
  buildArgs,
  buildCommand,
  buildConfigObject,
  buildMatrixConfig,
  fetchAllConfig,
  fetchManagedMcpServers,
  fetchModelSource,
  fetchOpenCodexModels,
  importKindBadgeClass,
  saveMcpServers,
  saveSyncConfig,
  type McpImportSource,
  type McpMatrixCell,
  type McpMatrixResult,
  type McpServerInfo,
  type ModelSourceStatus,
  type OpenCodexModelsResult,
  type Platform,
  type PlatformId,
  type SyncMode,
} from '@/lib/unifyai'

// ---------- 同步内容三态 ----------
const modeTab = ref<SyncMode>('all')
const mode = computed<SyncMode>(() => modeTab.value)
const { confirmDialog } = useConfirm()

// ---------- 目标平台（数据来自后端 --list-platforms --json，失败回落内置） ----------
const platforms = ref<Platform[]>(PLATFORMS)
const allPlatforms = ref(false)
const selectedPlatforms = ref<PlatformId[]>(PLATFORMS.map((p) => p.id))

function platformSupportsMode(platform: Platform, m: SyncMode) {
  if (m === 'models') return platform.modelSync
  if (m === 'mcp') return platform.mcpSync === true
  return true
}

function togglePlatform(platform: Platform) {
  if (allPlatforms.value) return
  if (selectedPlatforms.value.includes(platform.id))
    selectedPlatforms.value = selectedPlatforms.value.filter((id) => id !== platform.id)
  else selectedPlatforms.value = [...selectedPlatforms.value, platform.id]
}

function platformDisabled(platform: Platform) {
  return allPlatforms.value || !platformSupportsMode(platform, mode.value)
}

function disableReason(platform: Platform) {
  if (mode.value === 'models' && !platform.modelSync) return '该平台不支持模型同步'
  if (mode.value === 'mcp' && platform.mcpSync !== true) return '该平台 MCP 同步未实现'
  return ''
}

// ---------- MCP 矩阵（行=源全集，列=平台，勾选=开启） ----------
const allServers = ref<McpServerInfo[]>(INITIAL_MCP_SERVERS)
/** 已禁用的服务器名集合（整行半透明、同步跳过；写回 mcp.json 的 enabled） */
const disabledServers = ref<Set<string>>(
  new Set(INITIAL_MCP_SERVERS.filter((s) => !s.enabled).map((s) => s.name)),
)

/** 把服务器列表写回后端 mcp.json（添加/删除/编辑/导入共用） */
const savingMcp = ref(false)
async function persistMcpServers() {
  savingMcp.value = true
  try {
    await saveMcpServers(allServers.value)
    toast.success('MCP 配置已保存到 mcp.json')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
    throw e
  } finally {
    savingMcp.value = false
  }
}

/** 启用/禁用切换：更新本地状态 + 自动保存（写回 mcp.json 的 enabled 字段） */
async function onDisabledChange(next: Set<string>) {
  disabledServers.value = next
  // 同步 allServers 的 enabled 字段
  allServers.value = allServers.value.map((s) => ({
    ...s,
    enabled: !next.has(s.name),
  }))
  try {
    await persistMcpServers()
  } catch {
    /* toast 已提示 */
  }
}

/** 删除服务器：确认后移除 + 保存 */
async function removeServer(name: string) {
  const confirmed = await confirmDialog(`确定删除 MCP 服务器「${name}」？将从 mcp.json 中移除。`)
  if (!confirmed) return
  allServers.value = allServers.value.filter((s) => s.name !== name)
  const next = new Set(disabledServers.value)
  next.delete(name)
  disabledServers.value = next
  const nextMatrix = { ...matrix.value }
  delete nextMatrix[name]
  matrix.value = nextMatrix
  try {
    await persistMcpServers()
  } catch {
    /* toast 已提示 */
  }
}

// ---------- 编辑 MCP 工具（行内编辑按钮触发，表单 / JSON 双模式） ----------
const editDialogOpen = ref(false)
const editMode = ref<'form' | 'json'>('json')
const editJsonDraft = ref('')
const editOriginalName = ref('')
const editForm = reactive({
  name: '',
  type: 'local' as 'local' | 'remote',
  enabled: true,
  command: '',
  url: '',
  headers: '',
  env: [] as Array<{ key: string; value: string }>,
})

/** ExcludeMatrix 行内编辑按钮：直接打开编辑弹窗（默认 JSON 模式直编辑） */
function openEditServer(server: McpServerInfo) {
  fillEditForm(server)
  editMode.value = 'json'
  editDialogOpen.value = true
}

function fillEditForm(server: McpServerInfo) {
  editOriginalName.value = server.name
  editJsonDraft.value = JSON.stringify(server, null, 2)
  Object.assign(editForm, {
    name: server.name,
    type: server.type,
    enabled: server.enabled,
    command: (server.command || []).join(' '),
    url: server.url || '',
    headers: Object.entries(server.headers || {})
      .map(([k, v]) => `${k}: ${v}`)
      .join('\n'),
    env: Object.entries(server.env || {}).map(([key, value]) => ({ key, value })),
  })
}

/** 表单 → JSON：把当前表单值生成为 JSON 文本（切换不丢改动） */
function syncEditFormToJson() {
  editJsonDraft.value = JSON.stringify(buildEditPayload(editForm), null, 2)
}

/** 编辑弹窗模式切换：切换前把另一模式当前值同步过来，JSON 解析失败则阻止切换 */
function changeEditMode(next: string) {
  if (next === editMode.value) return
  if (next === 'json') {
    syncEditFormToJson()
    editMode.value = 'json'
  } else {
    const error = syncEditJsonToForm()
    if (error) {
      toast.error('无法切换到表单模式', { description: error })
      return
    }
    editMode.value = 'form'
  }
}

/** JSON → 表单：解析 JSON 回填表单，失败返回错误信息 */
function syncEditJsonToForm(): string | null {
  let parsed: Record<string, any>
  try {
    parsed = JSON.parse(editJsonDraft.value)
  } catch (error) {
    return 'JSON 解析失败：' + (error instanceof Error ? error.message : String(error))
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed))
    return 'JSON 必须是单个 MCP 服务器对象'
  Object.assign(editForm, {
    name: String(parsed.name ?? ''),
    type: parsed.type === 'remote' ? 'remote' : 'local',
    enabled: parsed.enabled !== false,
    command: Array.isArray(parsed.command)
      ? parsed.command.join(' ')
      : String(parsed.command ?? ''),
    url: String(parsed.url ?? ''),
    headers: Object.entries((parsed.headers || {}) as Record<string, string>)
      .map(([k, v]) => `${k}: ${v}`)
      .join('\n'),
    env: Object.entries((parsed.env || {}) as Record<string, string>).map(([key, value]) => ({
      key,
      value,
    })),
  })
  return null
}

/** 从表单构造 McpServerInfo（编辑/同步共用） */
function buildEditPayload(form: typeof editForm): McpServerInfo {
  const srv: McpServerInfo = {
    name: form.name.trim(),
    type: form.type,
    enabled: form.enabled,
  }
  if (form.type === 'local') {
    srv.command = form.command.trim().split(/\s+/).filter(Boolean)
    const env: Record<string, string> = {}
    for (const row of form.env) {
      const k = row.key.trim()
      if (!k) continue
      env[k] = row.value
    }
    if (Object.keys(env).length) srv.env = env
  } else {
    srv.url = form.url.trim()
    const headers: Record<string, string> = {}
    for (const line of form.headers.split('\n')) {
      const idx = line.indexOf(':')
      if (idx > 0) headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
    }
    if (Object.keys(headers).length) {
      srv.headers = headers
      srv.hasAuth = true
    }
  }
  return srv
}

/** 校验编辑结果（表单/JSON 共用） */
function validateEditPayload(srv: McpServerInfo): string | null {
  if (!srv.name) return '名称必填'
  if (srv.type === 'local' && !(srv.command || []).length) return '请输入启动命令'
  if (srv.type === 'remote' && !srv.url) return '请输入远程地址 URL'
  return null
}

/** 替换列表中的服务器（处理改名时的 matrix / disabled 键迁移），再持久化 */
async function applyEdit(srv: McpServerInfo) {
  const oldName = editOriginalName.value
  const index = allServers.value.findIndex((s) => s.name === oldName)
  if (index < 0) throw new Error(`服务器「${oldName}」不存在`)
  if (srv.name !== oldName && allServers.value.some((s) => s.name === srv.name))
    throw new Error(`「${srv.name}」已存在`)
  const next = [...allServers.value]
  next[index] = srv
  allServers.value = next
  if (srv.name !== oldName) {
    const nextDisabled = new Set(disabledServers.value)
    if (nextDisabled.has(oldName)) {
      nextDisabled.delete(oldName)
      if (!srv.enabled) nextDisabled.add(srv.name)
    }
    disabledServers.value = nextDisabled
    const nextMatrix = { ...matrix.value }
    if (nextMatrix[oldName]) {
      nextMatrix[srv.name] = nextMatrix[oldName]
      delete nextMatrix[oldName]
    }
    matrix.value = nextMatrix
  }
  await persistMcpServers()
}

/** 保存编辑（按当前模式取值） */
async function saveEditServer() {
  if (savingMcp.value) return
  let srv: McpServerInfo
  if (editMode.value === 'json') {
    let parsed: Record<string, any>
    try {
      parsed = JSON.parse(editJsonDraft.value)
    } catch (error) {
      toast.error('JSON 解析失败：' + (error instanceof Error ? error.message : String(error)))
      return
    }
    if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
      toast.error('JSON 必须是单个 MCP 服务器对象')
      return
    }
    srv = buildEditPayload({
      name: String(parsed.name ?? ''),
      type: parsed.type === 'remote' ? 'remote' : 'local',
      enabled: parsed.enabled !== false,
      command: Array.isArray(parsed.command)
        ? parsed.command.join(' ')
        : String(parsed.command ?? ''),
      url: String(parsed.url ?? ''),
      headers: Object.entries((parsed.headers || {}) as Record<string, string>)
        .map(([k, v]) => `${k}: ${v}`)
        .join('\n'),
      env: Object.entries((parsed.env || {}) as Record<string, string>).map(([key, value]) => ({
        key,
        value,
      })),
    })
  } else {
    srv = buildEditPayload(editForm)
  }
  const error = validateEditPayload(srv)
  if (error) {
    toast.error(error)
    return
  }
  try {
    await applyEdit(srv)
    editDialogOpen.value = false
    toast.success('MCP 配置已保存到 mcp.json')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
  }
}

// ---------- 添加 / 导入 MCP 工具 ----------
const addDialogOpen = ref(false)
const importSource = ref<McpImportSource[]>([])
const importSelected = ref<string[]>([])
const importing = ref(false)
const addForm = reactive({
  name: '',
  type: 'local' as 'local' | 'remote',
  enabled: true,
  command: '',
  url: '',
  headers: '',
  env: [] as Array<{ key: string; value: string }>,
})

function openAddDialog() {
  addDialogOpen.value = true
  importSelected.value = []
  importSource.value = []
  addForm.name = ''
  addForm.type = 'local'
  addForm.enabled = true
  addForm.command = ''
  addForm.url = ''
  addForm.headers = ''
  addForm.env = []
  // 打开即自动拉一次，避免用户再点「加载列表」
  loadImportSource()
}

function addEnvRow(rows: Array<{ key: string; value: string }>) {
  rows.push({ key: '', value: '' })
}

function removeEnvRow(rows: Array<{ key: string; value: string }>, index: number) {
  rows.splice(index, 1)
}

/** 从 MCP 管理加载可导入的服务器 */
async function loadImportSource() {
  importing.value = true
  try {
    importSource.value = await fetchManagedMcpServers()
    if (!importSource.value.length) {
      toast.info('MCP 管理中没有可导入的服务器')
    }
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '读取 MCP 管理失败')
  } finally {
    importing.value = false
  }
}

function toggleImport(name: string) {
  const i = importSelected.value.indexOf(name)
  if (i >= 0) importSelected.value.splice(i, 1)
  else importSelected.value.push(name)
}

/** 把选中的 MCP 管理项（单 MCP / 分组 / 聚合）导入到本地 mcp.json */
async function importSelectedServers() {
  const picked = importSource.value.filter((s) => importSelected.value.includes(s.name))
  if (!picked.length) return
  const existing = new Set(allServers.value.map((s) => s.name))
  const added: string[] = []
  for (const item of picked) {
    // 分组 / 聚合端点会复用上游 name，可能与已有条目冲突。这里在保存前去重一次，
    // 避免落 mcp.json 后下游工具拿到同名双 entry。existing 随循环更新，
    // 防止 picked 内同名项（如「单 MCP 叫 mcp-smart」+「聚合 mcp-smart」）重复 append。
    if (existing.has(item.server.name)) {
      toast.warning(`「${item.server.name}」已存在，跳过`)
      continue
    }
    allServers.value = [...allServers.value, { ...item.server }]
    existing.add(item.server.name)
    added.push(item.server.name)
  }
  if (!added.length) return
  const next = { ...matrix.value }
  for (const name of added) {
    next[name] = Object.fromEntries(
      platforms.value.map((p) => [p.id, undefined])
    ) as Record<PlatformId, McpMatrixCell>
  }
  matrix.value = next
  try {
    await persistMcpServers()
    addDialogOpen.value = false
    toast.success(`已导入 ${added.length} 个 MCP 工具`)
  } catch {
    /* toast 已提示 */
  }
}

/** 手动添加 MCP 工具并保存 */
async function addManualServer() {
  const name = addForm.name.trim()
  if (!name) {
    toast.error('请输入工具名称')
    return
  }
  if (allServers.value.some((s) => s.name === name)) {
    toast.error(`「${name}」已存在`)
    return
  }
  const srv: McpServerInfo = {
    name,
    type: addForm.type,
    enabled: addForm.enabled,
  }
  if (addForm.type === 'local') {
    const parts = addForm.command.trim().split(/\s+/).filter(Boolean)
    if (!parts.length) {
      toast.error('请输入启动命令（如 npx -y @modelcontextprotocol/server-filesystem /path）')
      return
    }
    srv.command = parts
    // 环境变量：跳过空 key，保留空 value（用户可能故意置空）
    const env: Record<string, string> = {}
    for (const row of addForm.env) {
      const k = row.key.trim()
      if (!k) continue
      env[k] = row.value
    }
    if (Object.keys(env).length) srv.env = env
  } else {
    if (!addForm.url.trim()) {
      toast.error('请输入远程地址 URL')
      return
    }
    srv.url = addForm.url.trim()
    if (addForm.headers.trim()) {
      const headers: Record<string, string> = {}
      for (const line of addForm.headers.split('\n')) {
        const idx = line.indexOf(':')
        if (idx > 0) headers[line.slice(0, idx).trim()] = line.slice(idx + 1).trim()
      }
      if (Object.keys(headers).length) {
        srv.headers = headers
        srv.hasAuth = true
      }
    }
  }
  allServers.value = [...allServers.value, srv]
  matrix.value = {
    ...matrix.value,
    [name]: Object.fromEntries(
      platforms.value.map((p) => [p.id, undefined])
    ) as Record<PlatformId, McpMatrixCell>,
  }
  try {
    await persistMcpServers()
    addDialogOpen.value = false
  } catch {
    /* toast 已提示 */
  }
}

// ---------- 全局编辑 mcp.json（整个服务器列表，顶部按钮触发） ----------
const jsonFileDialogOpen = ref(false)
const jsonFileDraft = ref('')
const savingJsonFile = ref(false)

/** 打开全局编辑：把整个 mcp.json 服务器列表载入 JSON 编辑器 */
function openJsonFileEdit() {
  jsonFileDraft.value = JSON.stringify(allServers.value, null, 2)
  jsonFileDialogOpen.value = true
}

/** 全局编辑弹窗里的服务器计数（坏 JSON 时返回 0，不抛错） */
function serverCountInDraft() {
  try {
    const parsed = JSON.parse(jsonFileDraft.value)
    return Array.isArray(parsed) ? parsed.length : 0
  } catch {
    return 0
  }
}

/** 保存全局编辑：校验整个 JSON 列表后全量替换 + 重建 matrix/disabled + 写回 mcp.json */
async function saveJsonFile() {
  if (savingJsonFile.value) return
  let parsed: unknown
  try {
    parsed = JSON.parse(jsonFileDraft.value)
  } catch (error) {
    toast.error('JSON 解析失败：' + (error instanceof Error ? error.message : String(error)))
    return
  }
  if (!Array.isArray(parsed)) {
    toast.error('JSON 必须是服务器数组（[...]）')
    return
  }
  const servers: McpServerInfo[] = []
  for (const item of parsed) {
    if (typeof item !== 'object' || item === null) {
      toast.error('列表项必须是对象')
      return
    }
    const raw = item as Record<string, any>
    const name = String(raw.name ?? '').trim()
    if (!name) {
      toast.error('存在名称为空的服务器条目')
      return
    }
    const type = raw.type === 'remote' ? 'remote' : 'local'
    const srv: McpServerInfo = { name, type, enabled: raw.enabled !== false }
    if (type === 'local') {
      const command = Array.isArray(raw.command)
        ? raw.command.map(String)
        : String(raw.command ?? '')
            .split(/\s+/)
            .filter(Boolean)
      if (!command.length) {
        toast.error(`「${name}」缺少启动命令（command）`)
        return
      }
      srv.command = command
      if (raw.env && typeof raw.env === 'object') srv.env = raw.env
    } else {
      const url = String(raw.url ?? '').trim()
      if (!url) {
        toast.error(`「${name}」缺少远程地址（url）`)
        return
      }
      srv.url = url
      if (raw.headers && typeof raw.headers === 'object') {
        srv.headers = raw.headers
        srv.hasAuth = true
      }
    }
    servers.push(srv)
  }
  if (!servers.length) {
    toast.error('服务器列表不能为空')
    return
  }
  savingJsonFile.value = true
  try {
    allServers.value = servers
    // 重建 matrix：保留仍在列表中的服务器原有状态，新条目补 undefined 占位
    const nextMatrix: Record<string, Record<PlatformId, McpMatrixCell>> = {}
    for (const srv of servers) {
      nextMatrix[srv.name] = matrix.value[srv.name] || Object.fromEntries(
        platforms.value.map((p) => [p.id, undefined])
      ) as Record<PlatformId, McpMatrixCell>
    }
    matrix.value = nextMatrix
    // 重建 disabled：只保留仍在列表中的，再按 enabled 字段对齐
    const names = new Set(servers.map((s) => s.name))
    const nextDisabled = new Set(
      [...disabledServers.value].filter(
        (n) => names.has(n) && servers.find((s) => s.name === n)?.enabled === false,
      ),
    )
    for (const srv of servers) if (!srv.enabled) nextDisabled.add(srv.name)
    disabledServers.value = nextDisabled
    await persistMcpServers()
    jsonFileDialogOpen.value = false
    toast.success('mcp.json 已保存')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '保存失败')
  } finally {
    savingJsonFile.value = false
  }
}


const matrix = ref<Record<string, Record<PlatformId, McpMatrixCell>>>({})

/** 把 --list-mcp --json 结果应用到界面：源全集（行）+ 各平台 enabled（初始勾选） */
function applyMatrix(res: McpMatrixResult) {
  const srcServers: McpServerInfo[] = (res.source?.servers || []).map((item) => {
    const c = (item.config || {}) as Record<string, unknown>
    const type = c.type === 'remote' ? 'remote' : 'local'
    const srv: McpServerInfo = { name: item.name, type, enabled: item.enabled }
    if (type === 'local') {
      srv.command = Array.isArray(c.command)
        ? (c.command as string[]).map(String)
        : String(c.command || '')
            .split(/\s+/)
            .filter(Boolean)
      if (c.env && typeof c.env === 'object') srv.env = c.env as Record<string, string>
    } else {
      srv.url = String(c.url || '')
      if (c.headers && typeof c.headers === 'object') {
        srv.headers = c.headers as Record<string, string>
        srv.hasAuth = true
      }
    }
    return srv
  })
  allServers.value = srcServers.length ? srcServers : INITIAL_MCP_SERVERS

  // 行=源全集；单元格=平台 enabled 状态（平台不可读 / 未配置 = undefined）
  const next: Record<string, Record<PlatformId, McpMatrixCell>> = {}
  for (const srv of allServers.value) {
    const row: Record<PlatformId, McpMatrixCell> = Object.fromEntries(
      platforms.value.map((p) => [p.id, undefined])
    ) as Record<PlatformId, McpMatrixCell>
    for (const p of res.platforms) {
      if (!p.readable) continue
      const pid = p.platform as PlatformId
      const found = p.servers.find((x) => x.name === srv.name)
      row[pid] = found ? found.enabled : undefined
    }
    next[srv.name] = row
  }
  matrix.value = next
}

async function reloadMatrix() {
  const all = await fetchAllConfig()
  applyMatrix(all.mcp)
}

/** 后端平台列表（--list all → platforms）→ 前端 Platform（保留内置 color） */
function applyPlatforms(list: BackendPlatform[]) {
  if (!list.length) return
  const colorOf = Object.fromEntries(PLATFORMS.map((p) => [p.id, p.color]))
  const known = new Set<string>(PLATFORMS.map((p) => p.id))
  platforms.value = list
    .filter((p) => known.has(String(p.id)))
    .map((p) => ({
      id: String(p.id) as PlatformId,
      name: String(p.name),
      color: colorOf[String(p.id)] || '#888888',
      modelSync: p.modelStatus === 'supported' || p.supportsModels === true,
      mcpSync:
        p.mcpStatus === 'supported'
          ? true
          : p.mcpStatus === 'not_implemented'
            ? 'unimplemented'
            : false,
      configPath: String(p.configPath || ''),
      format: String(p.configFormat || '').toUpperCase(),
    }))
}

// ---------- 数据源状态 ----------
const modelSource = ref<ModelSourceStatus>({ ...INITIAL_MODEL_SOURCE })
const opencodexModels = ref<OpenCodexModelsResult>({ ...INITIAL_OPENCODEX_MODELS })
/** 强制视觉（--enable-vision）：切换后重新拉取 OpenCodex 模型列表 */
const enableVision = ref(false)
async function reloadOpenCodexModels() {
  const res = await fetchOpenCodexModels(enableVision.value)
  opencodexModels.value = res
}
/** 「强制视觉」开关回调：写 ref 后再重拉模型列表（ref 赋值必须在 script 内做，
 *  不能依赖模板内联函数的解包赋值，避免 HMR/编译器下状态不同步）。 */
function handleToggleVision(v: boolean) {
  enableVision.value = v
  reloadOpenCodexModels()
}
const mcpSourcePath = ref('./mcp.json（cwd 优先，回退 ~/.unifyai/mcp.json）')

// ---------- 高级选项 ----------
const advancedOpen = ref(false)
const sourcePath = ref(DEFAULT_SOURCE)
const verbose = ref(false)

// ---------- 命令拼装 ----------
// UI 已从「排除矩阵」切换到「同步矩阵」，不再传 --mcp-exclude / --mcp-exclude-for（CLI 命令保留，供脚本使用）。
const commandOpts = computed(() => ({
  mode: mode.value,
  all: allPlatforms.value,
  platforms: selectedPlatforms.value,
  mcpPlatforms: null,
  globalExcludes: [],
  perPlatformExcludes: Object.fromEntries(
    platforms.value.map((p) => [p.id, []])
  ),
  dryRun: false,
  source: sourcePath.value,
  verbose: verbose.value,
  // 「强制视觉」开关同时影响命令预览（command）和实际执行（streamUrl）——两路都走同一 buildArgs。
  enableVision: enableVision.value,
}))

// 命令预览：主命令 = 全量同步（--config sync.json，CLI 根据配置自动分发矩阵/全量）
const command = computed(() => buildCommand(commandOpts.value))

/** sync.json 配置内容预览：始终显示完整配置（全量 + 矩阵合并），实时反映所有 UI 改动 */
const configPreview = computed(() => buildSyncConfig())

// ---------- 执行（真实 unifyai CLI，经后端 SSE 流式日志） ----------
const runTrigger = ref(0)
const runStatus = ref<StreamStatus>('idle')
const runExitCode = ref<number | null>(null)
const dryRunMode = ref(false)
/** 自定义 CLI 参数（矩阵同步 / 导入用）；null = 用 buildArgs 默认命令 */
const customArgs = ref<string[] | null>(null)

/** SSE 端点：args 数组 JSON 编码进查询参数，连接即触发任务 */
const streamUrl = computed(() => {
  const args = customArgs.value ?? buildArgs({ ...commandOpts.value, dryRun: dryRunMode.value })
  return `/api/unifyai/stream?args=${encodeURIComponent(JSON.stringify(args))}`
})

/** 用指定参数启动任务（全量同步 / 矩阵同步 / 导入共用） */
/**
 * 用指定参数启动任务（全量同步 / 矩阵同步 / 导入共用）。
 * 传 config 时先保存到 sync.json（--config 引用的唯一来源）再执行。
 */
async function executeWithArgs(args: string[], dryRun = false, config?: unknown) {
  if (runStatus.value === 'running') return
  if (config !== undefined) {
    try {
      await saveSyncConfig(config)
    } catch (e) {
      toast.error(e instanceof Error ? e.message : '保存同步配置失败')
      return
    }
  }
  customArgs.value = args
  dryRunMode.value = dryRun
  runExitCode.value = null
  runTrigger.value++ // 触发 StreamLogPanel 连接 SSE（后端自动启动任务）
}

/** 构造完整 sync.json 配置（仅 CLI 执行所需字段：mode/platforms/mcp/enableVision/source） */
function buildFullConfig() {
  return buildConfigObject({ ...commandOpts.value, dryRun: false })
}

/**
 * 完整 sync.json（预览与执行共用）：全量配置 + 当前矩阵（有勾选时合并进 mcp.matrix）。
 * computed 依赖 commandOpts / matrix，任何 UI 改动（模式/平台/矩阵/视觉）都会实时反映到预览。
 */
function buildSyncConfig() {
  const cfg = buildFullConfig() as { mcp?: Record<string, unknown> }
  const matrixObj = (buildMatrixConfig(matrix.value) as { mcp: { matrix?: Record<string, unknown> } }).mcp.matrix
  // 矩阵无条件写入（空对象也写）：确保 CLI 走矩阵模式而不是全量同步——
  // 用户全部删除时矩阵为空，此时仍要触发 forceMcp 清空目标平台
  cfg.mcp = { ...(cfg.mcp || {}), matrix: matrixObj || {} }
  return cfg
}

/** 全量同步（统一入口）：先落盘完整 sync.json（含矩阵勾选，CLI 自动分发矩阵/全量）再执行 */
function execute(dryRun: boolean) {
  executeWithArgs(buildArgs({ ...commandOpts.value, dryRun }), dryRun, buildSyncConfig())
}

function onRunDone(exitCode: string) {
  runExitCode.value = Number(exitCode) || 0
  if (runExitCode.value === 0) {
    toast.success(dryRunMode.value ? '预览完成（dry-run，未写入文件）' : '同步完成')
    // 导入完成 → 刷新矩阵（源 mcp.json 已更新）
    if (customArgs.value?.[0] === '--import-mcp') reloadMatrix()
  } else {
    toast.error(`命令退出码 ${runExitCode.value}，请查看日志`)
  }
}

function onRunError(message: string) {
  toast.error(message || '命令执行失败')
}

// ---------- 执行前确认弹窗（文档 §10.3） ----------
const confirmOpen = ref(false)
const confirmWarnings = ref<string[]>([])
/** 待执行的任务（确认后运行；config 存在时先落盘 sync.json） */
const pendingRun = ref<{ args: string[]; dryRun: boolean; config?: unknown } | null>(null)

function startSync() {
  const warnings: string[] = []
  // UI 恒开 forceMcp（sync.json 写死 forceMcp: true）：目标平台未在矩阵中的 MCP 将被删除（重置语义）。
  // 仅模型模式（mode=models）不动 MCP，无需该警告。
  if (mode.value !== 'models') {
    warnings.push(
      'MCP 将按矩阵重置（forceMcp 开启）：目标平台存在但矩阵未勾选/未配置的服务器会被删除，勾选=开启、右键=关闭、未勾选=删除。',
    )
  }
  const config = buildSyncConfig()
  pendingRun.value = {
    args: ['--config', SYNC_CONFIG_PATH],
    dryRun: false,
    config,
  }
  if (warnings.length) {
    confirmWarnings.value = warnings
    confirmOpen.value = true
  } else {
    const { args, dryRun, config } = pendingRun.value
    pendingRun.value = null
    executeWithArgs(args, dryRun, config)
  }
}

/** 导入各平台 MCP 配置到源 mcp.json（--import-mcp），完成后自动刷新矩阵 */
function startImportMcp() {
  if (runStatus.value === 'running') return
  executeWithArgs(IMPORT_MCP_ARGS, false)
}

/** 确认弹窗「确认，开始同步」：关弹窗并执行待执行任务 */
function confirmAndExecute() {
  confirmOpen.value = false
  if (!pendingRun.value) return
  const { args, dryRun, config } = pendingRun.value
  pendingRun.value = null
  executeWithArgs(args, dryRun, config)
}

// ---------- 帮助 ----------
const helpOpen = ref(false)

// ---------- 元数据缓存刷新（真实调用 unifyai CLI：--update-metadata） ----------
const metadataUpdating = ref(false)
async function handleUpdateMetadata() {
  if (metadataUpdating.value) return
  metadataUpdating.value = true
  try {
    // 通过后端桥接启动 unifyai --update-metadata（SSE 日志走 /api/unifyai/stream）。
    const res = await fetch('/api/unifyai/run', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ args: ['--update-metadata'] }),
    })
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const body = await res.json()
    if (body.error) throw new Error(body.error)
    // CLI 拉取 OpenRouter 数据通常 2~5s，轮询后端缓存状态直到 CachedAt 更新或超时。
    const deadline = Date.now() + 20_000
    let updated: ModelSourceStatus | null = null
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 800))
      const src = await fetchModelSource()
      if (src.kind === 'openrouter' && src.modelCount > 0) {
        updated = src
        break
      }
    }
    if (updated) {
      modelSource.value = updated
      toast.success('模型元数据缓存已刷新', {
        description: `OpenRouter ${updated.modelCount} 个模型已更新（含 ${updated.visionCount} 视觉 / ${updated.reasoningCount} 思考）`,
      })
    } else {
      toast.error('元数据刷新超时', { description: 'CLI 任务可能已结束但缓存未就绪，请查看日志' })
    }
  } catch (err) {
    toast.error('元数据刷新失败', { description: String(err) })
  } finally {
    metadataUpdating.value = false
  }
}

// ---------- 初始化 ----------
onMounted(async () => {
  // 一次拉全（--list all → platforms + models + mcp 矩阵 + metadata 缓存状态）
  const all = await fetchAllConfig()
  applyPlatforms(all.platforms)
  applyMatrix(all.mcp)
  if (all.models?.models?.length) opencodexModels.value = all.models
  if (all.metadata?.modelCount > 0) {
    modelSource.value = {
      ...modelSource.value,
      kind: 'openrouter',
      baseUrl: 'https://openrouter.ai/api/v1',
      modelCount: all.metadata.modelCount,
      cachedAt: all.metadata.cachedAt || '',
    }
  }
  // 兜底：--list all 无数据（CLI 不可用）时用独立接口再试，保证页面有内容
  if (!all.mcp?.platforms?.length) {
    fetchModelSource().then((source) => (modelSource.value = source))
    reloadOpenCodexModels()
    fetch('/api/unifyai/platforms')
      .then((res) => (res.ok ? res.json() : null))
      .then((data: { platforms?: BackendPlatform[] } | null) => {
        if (data?.platforms?.length) applyPlatforms(data.platforms)
      })
      .catch(() => {})
  }
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="UnifyAI 配置同步"
      description="把模型与 MCP 配置从 OpenCodex 源同步到 6 个 AI 开发平台的本地配置文件"
    >
      <template #actions>
        <Button variant="outline" :disabled="metadataUpdating" @click="handleUpdateMetadata">
          <RiLoader4Line v-if="metadataUpdating" size="16" class="animate-spin" />
          <RiRefreshLine v-else size="16" />
          {{ metadataUpdating ? '刷新中...' : '更新元数据' }}
        </Button>
        <Button variant="ghost" @click="helpOpen = true"> <RiQuestionLine size="16" />帮助 </Button>
      </template>
    </PageHeader>

    <!-- ① 同步内容与目标平台（头部含同步内容 + 全部平台） -->
    <Card class="rounded-md">
      <CardHeader
        class="flex flex-col gap-3 space-y-0 lg:flex-row lg:items-center lg:justify-between"
      >
        <div class="space-y-0.5">
          <CardTitle class="text-base">① 同步内容与目标平台</CardTitle>
          <CardDescription
            >勾选要同步的平台；不支持所选能力的平台将置灰（执行时跳过并提示）。</CardDescription
          >
        </div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
          <Tabs v-model="modeTab">
            <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
              <TabsTrigger value="all">全部同步</TabsTrigger>
              <TabsTrigger value="models">仅模型</TabsTrigger>
              <TabsTrigger value="mcp">仅 MCP</TabsTrigger>
            </TabsList>
          </Tabs>
          <div class="flex items-center gap-2">
            <Switch id="all-platforms" v-model="allPlatforms" />
            <Label for="all-platforms" class="cursor-pointer whitespace-nowrap text-sm font-medium"
              >全部平台（--all）</Label
            >
          </div>
          <div class="flex items-center gap-2">
            <Switch id="enable-vision" v-model="enableVision" />
            <Label for="enable-vision" class="cursor-pointer whitespace-nowrap text-sm font-medium"
              >强制视觉（--enable-vision）</Label
            >
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <div
          class="grid grid-cols-2 gap-2 md:grid-cols-3"
          style="grid-template-columns: repeat(5, minmax(0, 1fr))"
        >
          <PlatformCard
            v-for="platform in platforms"
            :key="platform.id"
            :platform="platform"
            :selected="allPlatforms || selectedPlatforms.includes(platform.id)"
            :disabled="platformDisabled(platform)"
            :disable-reason="disableReason(platform)"
            @toggle="togglePlatform"
          />
        </div>
      </CardContent>
    </Card>

    <!-- ② MCP 同步矩阵（勾选=平台开启 + 白名单） -->
    <Card v-if="mode !== 'models'" class="rounded-md">
      <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
        <div class="space-y-0.5">
          <CardTitle class="text-base">② MCP 同步矩阵</CardTitle>
          <CardDescription
            >行 = 服务器（跨平台去重），列 = 平台，勾选 = 该平台开启。改动后到「③ 执行」点「按矩阵同步」落地到各平台。</CardDescription
          >
        </div>
        <div class="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            :disabled="runStatus === 'running'"
            @click="startImportMcp"
          >
            <RiImportLine size="16" />导入各平台配置到源
          </Button>
          <Button variant="outline" size="sm" :disabled="savingMcp" @click="openAddDialog">
            <RiAddLine size="16" />添加 MCP 工具
          </Button>
          <Button variant="outline" size="sm" :disabled="savingMcp" @click="openJsonFileEdit">
            <RiEditLine size="16" />编辑
          </Button>
        </div>
      </CardHeader>
      <CardContent class="space-y-6">
        <section>
          <ExcludeMatrix
            :servers="allServers"
            :platforms="platforms"
            :disabled="disabledServers"
            v-model:matrix="matrix"
            @update:disabled="onDisabledChange"
            @remove="removeServer"
            @edit="openEditServer"
          />
        </section>
      </CardContent>
    </Card>

    <!-- 数据预览（非操作步骤，纯展示） -->
    <CommandPreview
      :command="command"
      :config-data="configPreview"
      :model-source="modelSource"
      :opencodex-models="opencodexModels"
      :enable-vision="enableVision"
      :on-toggle-vision="handleToggleVision"
      :show-vision="false"
      :mcp-source-path="mcpSourcePath"
      :mcp-enabled="allServers.length"
      :mcp-total="allServers.length"
    />

    <!-- ③ 执行（高级选项 + 操作区） -->
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">③ 执行</CardTitle>
        <CardDescription
          >预览（dry-run）只展示不写文件；开始同步前会弹确认，备份后写入各平台配置。</CardDescription
        >
      </CardHeader>
      <CardContent class="space-y-4">
        <div class="rounded-md border">
          <button
            type="button"
            class="flex w-full items-center justify-between gap-2 px-3 py-2.5 text-left text-sm font-medium"
            @click="advancedOpen = !advancedOpen"
          >
            <span class="flex items-center gap-2"><RiSettings3Line size="15" />高级选项</span>
            <RiArrowDownSLine v-if="advancedOpen" size="16" class="text-muted-foreground" />
            <RiArrowRightSLine v-else size="16" class="text-muted-foreground" />
          </button>
          <div v-show="advancedOpen" class="grid gap-3 border-t px-3 py-3 sm:grid-cols-2">
            <div class="space-y-1">
              <Label>模型源配置路径（--source）</Label>
              <Input v-model="sourcePath" placeholder="~/.opencodex/config.json" />
            </div>
            <!-- <div class="flex items-center gap-2 pt-5">
              <Switch id="verbose" v-model="verbose" />
              <Label for="verbose" class="cursor-pointer text-sm">显示详细堆栈信息（--verbose）</Label>
            </div> -->
          </div>
        </div>
        <div class="flex flex-wrap justify-end gap-2">
          <Button variant="outline" :disabled="runStatus === 'running'" @click="execute(true)">
            <RiPlayLine size="16" />预览（dry-run）
          </Button>
          <Button :disabled="runStatus === 'running'" @click="startSync">
            <RiLoader4Line v-if="runStatus === 'running'" size="16" class="animate-spin" />
            <RiPlayLine v-else size="16" />
            {{ runStatus === 'running' ? '执行中...' : '开始同步' }}
          </Button>
        </div>
      </CardContent>
    </Card>

    <!-- ⑥ 执行日志（真实 unifyai 输出，SSE 流式）。
         ⚠ 不要加 v-if="runStatus !== 'idle'"：该组件必须先实例化才能 watch trigger、
         才能 emit('update:status','running') 把 runStatus 拉离 idle（v-model 闭环要求）。
         空态文案由组件内部 <div v-if="!logLines.length"> 承担，与 SkillsView 用法一致。
         执行指标（结果/退出码/模式）通过 #header-extra 嵌到卡片头部右侧，不再用单独卡片。 -->
    <StreamLogPanel
      :stream-url="streamUrl"
      :trigger="runTrigger"
      v-model:status="runStatus"
      :empty-text="'正在连接执行任务…'"
      @done="onRunDone"
      @error="onRunError"
    >
      <template #header-extra>
        <div
          v-if="runStatus === 'done' || runStatus === 'error'"
          class="flex flex-wrap items-center gap-x-3 gap-y-0.5"
        >
          <span class="flex items-baseline gap-1">
            <span class="text-muted-foreground">执行结果</span>
            <span
              class="font-semibold"
              :class="runStatus === 'done' ? 'text-emerald-600' : 'text-destructive'"
              >{{ runStatus === 'done' ? '成功' : '失败' }}</span
            >
          </span>
          <span class="flex items-baseline gap-1">
            <span class="text-muted-foreground">退出码</span>
            <span class="font-mono font-semibold">{{ runExitCode ?? '-' }}</span>
          </span>
          <span class="flex items-baseline gap-1">
            <span class="text-muted-foreground">模式</span>
            <span class="font-semibold">{{ dryRunMode ? 'dry-run 预览' : '实际同步' }}</span>
          </span>
        </div>
      </template>
    </StreamLogPanel>

    <!-- 添加 / 导入 MCP 工具弹窗 -->
    <Dialog v-model:open="addDialogOpen">
      <DialogContent
        class="max-h-[calc(100dvh-2rem)] overflow-x-hidden overflow-y-auto [grid-template-columns:minmax(0,1fr)] sm:max-w-xl!"
      >
        <DialogHeader>
          <DialogTitle>添加 MCP 工具</DialogTitle>
          <DialogDescription
            >从 MCP 管理导入，或手动配置一个新的 MCP 服务器（保存到 mcp.json）。</DialogDescription
          >
        </DialogHeader>
        <Tabs default-value="import">
          <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
            <TabsTrigger value="import">从 MCP 管理导入</TabsTrigger>
            <TabsTrigger value="manual">手动添加</TabsTrigger>
          </TabsList>

          <TabsContent value="import" class="space-y-3 pt-2">
            <div class="flex items-center justify-between gap-2">
              <p class="text-sm text-muted-foreground">
                勾选 MCP 管理中的端点（单 MCP / 分组 / 聚合），导入为同步工具。
              </p>
              <Button variant="outline" size="sm" :disabled="importing" @click="loadImportSource">
                <RiLoader4Line v-if="importing" size="14" class="animate-spin" />
                <RiImportLine v-else size="14" />
                {{ importing ? '加载中...' : '刷新列表' }}
              </Button>
            </div>
            <div
              v-if="importSource.length"
              class="max-h-56 space-y-1 overflow-x-hidden overflow-y-auto rounded-md border p-2"
            >
              <label
                v-for="srv in importSource"
                :key="`${srv.kind}:${srv.name}`"
                class="flex min-w-0 cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 hover:bg-muted/40"
              >
                <Checkbox
                  :model-value="importSelected.includes(srv.name)"
                  @update:model-value="toggleImport(srv.name)"
                />
                <span class="min-w-0 flex-1 overflow-hidden">
                  <span class="flex items-baseline gap-2">
                    <span class="truncate font-mono text-sm">{{ srv.server.name }}</span>
                    <span class="truncate text-xs text-muted-foreground">{{ srv.path }}</span>
                  </span>
                  <span class="block truncate text-xs text-muted-foreground">
                    <template v-if="srv.kind === '单 MCP'">
                      {{
                        srv.server.type === 'local'
                          ? srv.server.command?.join(' ')
                          : srv.server.url
                      }}
                    </template>
                    <template v-else-if="srv.kind === '分组'">
                      {{ srv.count ? `聚合 ${srv.count} 个工具` : '该分组暂无工具' }}
                    </template>
                    <template v-else>
                      智能聚合当前所有启用 MCP 工具
                      <template v-if="srv.count">
                        <span class="text-foreground/60"> · </span>
                        <span class="tabular-nums">{{ srv.count }}</span> 工具
                      </template>
                    </template>
                  </span>
                </span>
                <Badge :class="['shrink-0 border', importKindBadgeClass(srv.kind)]">
                  {{ srv.kind }}
                </Badge>
              </label>
            </div>
            <p v-else-if="importing" class="text-sm text-muted-foreground">正在加载…</p>
            <p v-else class="text-sm text-muted-foreground">
              点击「刷新列表」从 MCP 管理获取可导入的服务器。
            </p>
            <DialogFooter>
              <Button :disabled="!importSelected.length" @click="importSelectedServers"
                >导入 {{ importSelected.length }} 个</Button
              >
              <Button variant="ghost" @click="addDialogOpen = false">取消</Button>
            </DialogFooter>
          </TabsContent>

          <TabsContent value="manual" class="space-y-4 pt-2">
            <div class="grid gap-3 sm:grid-cols-2">
              <div class="space-y-1">
                <Label>名称</Label>
                <Input v-model="addForm.name" placeholder="filesystem" />
              </div>
              <div class="space-y-1">
                <Label>类型</Label>
                <Select v-model="addForm.type">
                  <SelectTrigger><SelectValue placeholder="选择类型" /></SelectTrigger>
                  <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                    <SelectItem value="local">local（本地命令）</SelectItem>
                    <SelectItem value="remote">remote（远程网关）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Switch id="add-enabled" v-model="addForm.enabled" />
              <Label for="add-enabled" class="cursor-pointer text-sm">启用</Label>
            </div>
            <div v-if="addForm.type === 'local'" class="space-y-1">
              <Label>启动命令（空格分隔）</Label>
              <Input
                v-model="addForm.command"
                placeholder="npx -y @modelcontextprotocol/server-filesystem /path"
              />
            </div>
            <div v-if="addForm.type === 'local'" class="space-y-2">
              <Label>环境变量</Label>
              <div v-for="(row, index) in addForm.env" :key="index" class="flex gap-2">
                <Input v-model="row.key" placeholder="KEY" />
                <Input v-model="row.value" placeholder="值" />
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger as-child>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="删除环境变量"
                        @click="removeEnvRow(addForm.env, index)"
                      >
                        <RiDeleteBinLine size="16" />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>删除环境变量</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <Button variant="outline" size="sm" @click="addEnvRow(addForm.env)">
                <RiAddLine size="16" />添加环境变量
              </Button>
            </div>
            <template v-else>
              <div class="space-y-1">
                <Label>URL</Label>
                <Input
                  v-model="addForm.url"
                  placeholder="https://your-mcp-gateway.example.com/path"
                />
              </div>
              <div class="space-y-1">
                <Label>Headers（每行一个，格式：名称: 值）</Label>
                <Textarea
                  v-model="addForm.headers"
                  rows="2"
                  placeholder="Authorization: Bearer xxx"
                />
              </div>
            </template>
            <DialogFooter>
              <Button :disabled="savingMcp" @click="addManualServer">保存</Button>
              <Button variant="ghost" @click="addDialogOpen = false">取消</Button>
            </DialogFooter>
          </TabsContent>
        </Tabs>
      </DialogContent>
    </Dialog>

    <!-- 编辑 MCP 工具（行内触发，表单 / JSON 双模式） -->
    <Dialog v-model:open="editDialogOpen">
      <DialogContent
        class="max-h-[calc(100dvh-2rem)] overflow-x-hidden overflow-y-auto [grid-template-columns:minmax(0,1fr)] sm:max-w-xl!"
      >
        <DialogHeader>
          <DialogTitle>编辑 MCP 工具</DialogTitle>
          <DialogDescription>表单与 JSON 两种编辑方式，保存写回 mcp.json。</DialogDescription>
        </DialogHeader>
        <Tabs
          :model-value="editMode"
          class="space-y-4"
          @update:model-value="(next: string) => changeEditMode(next)"
        >
          <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
            <TabsTrigger value="form">表单</TabsTrigger>
            <TabsTrigger value="json">JSON</TabsTrigger>
          </TabsList>

          <TabsContent value="form" class="space-y-4 pt-2">
            <div class="grid gap-3 sm:grid-cols-2">
              <div class="space-y-1">
                <Label>名称</Label>
                <Input v-model="editForm.name" placeholder="filesystem" />
              </div>
              <div class="space-y-1">
                <Label>类型</Label>
                <Select v-model="editForm.type">
                  <SelectTrigger><SelectValue placeholder="选择类型" /></SelectTrigger>
                  <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                    <SelectItem value="local">local（本地命令）</SelectItem>
                    <SelectItem value="remote">remote（远程网关）</SelectItem>
                  </SelectContent>
                </Select>
              </div>
            </div>
            <div class="flex items-center gap-2">
              <Switch id="edit-enabled" v-model="editForm.enabled" />
              <Label for="edit-enabled" class="cursor-pointer text-sm">启用</Label>
            </div>
            <div v-if="editForm.type === 'local'" class="space-y-1">
              <Label>启动命令（空格分隔）</Label>
              <Input
                v-model="editForm.command"
                placeholder="npx -y @modelcontextprotocol/server-filesystem /path"
              />
            </div>
            <div v-if="editForm.type === 'local'" class="space-y-2">
              <Label>环境变量</Label>
              <div v-for="(row, index) in editForm.env" :key="index" class="flex gap-2">
                <Input v-model="row.key" placeholder="KEY" />
                <Input v-model="row.value" placeholder="值" />
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger as-child>
                      <Button
                        variant="ghost"
                        size="icon"
                        aria-label="删除环境变量"
                        @click="removeEnvRow(editForm.env, index)"
                      >
                        <RiDeleteBinLine size="16" />
                      </Button>
                    </TooltipTrigger>
                    <TooltipContent>删除环境变量</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <Button variant="outline" size="sm" @click="addEnvRow(editForm.env)">
                <RiAddLine size="16" />添加环境变量
              </Button>
            </div>
            <template v-else>
              <div class="space-y-1">
                <Label>URL</Label>
                <Input
                  v-model="editForm.url"
                  placeholder="https://your-mcp-gateway.example.com/path"
                />
              </div>
              <div class="space-y-1">
                <Label>Headers（每行一个，格式：名称: 值）</Label>
                <Textarea
                  v-model="editForm.headers"
                  rows="2"
                  placeholder="Authorization: Bearer xxx"
                />
              </div>
            </template>
          </TabsContent>

          <TabsContent value="json" class="space-y-2 pt-2">
            <div class="flex items-center justify-between">
              <Label>JSON 配置（可直接编辑，保存时校验并写回 mcp.json）</Label>
              <Button variant="outline" size="sm" @click="syncEditFormToJson">从表单同步</Button>
            </div>
            <Textarea
              v-model="editJsonDraft"
              rows="14"
              class="max-h-[60vh] w-full resize-none overflow-y-auto font-mono text-xs"
              spellcheck="false"
            />
          </TabsContent>
        </Tabs>
        <DialogFooter>
          <Button :disabled="savingMcp" @click="saveEditServer">保存</Button>
          <Button variant="ghost" @click="editDialogOpen = false">取消</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 全局编辑 mcp.json（整个服务器列表，顶部按钮触发） -->
    <Dialog v-model:open="jsonFileDialogOpen">
      <DialogContent
        class="max-h-[calc(100dvh-2rem)] overflow-x-hidden overflow-y-auto [grid-template-columns:minmax(0,1fr)] sm:max-w-2xl!"
      >
        <DialogHeader>
          <DialogTitle>编辑 mcp.json（全局）</DialogTitle>
          <DialogDescription
            >编辑整个 MCP 服务器列表 JSON，保存后全量写回 mcp.json 文件。</DialogDescription
          >
        </DialogHeader>
        <div class="space-y-2">
          <div class="flex items-center justify-between">
            <Label>mcp.json 内容（服务器数组，可直接增删改）</Label>
            <Badge variant="outline" class="shrink-0 font-mono text-[10px]"
              >{{ serverCountInDraft() }} 个服务器</Badge
            >
          </div>
          <Textarea
            v-model="jsonFileDraft"
            rows="18"
            class="max-h-[60vh] w-full resize-none overflow-y-auto font-mono text-xs"
            spellcheck="false"
          />
        </div>
        <DialogFooter>
          <Button :disabled="savingJsonFile || savingMcp" @click="saveJsonFile"
            >保存到 mcp.json</Button
          >
          <Button variant="ghost" @click="jsonFileDialogOpen = false">取消</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 执行前确认弹窗 -->
    <Dialog v-model:open="confirmOpen">
      <DialogContent class="sm:max-w-md!">
        <DialogHeader>
          <DialogTitle>确认执行同步？</DialogTitle>
          <DialogDescription>以下风险请确认后再继续：</DialogDescription>
        </DialogHeader>
        <div class="space-y-2">
          <div
            v-for="(warning, index) in confirmWarnings"
            :key="index"
            class="flex gap-2 rounded-md border border-amber-600/40 bg-amber-500/10 p-3 text-sm text-amber-700"
          >
            <RiCloseLine size="16" class="mt-0.5 shrink-0" />
            <span>{{ warning }}</span>
          </div>
        </div>
        <DialogFooter>
          <Button @click="confirmAndExecute">确认，开始同步</Button>
          <Button variant="ghost" @click="confirmOpen = false">取消</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 帮助弹窗 -->
    <Dialog v-model:open="helpOpen">
      <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-4xl!">
        <DialogHeader>
          <DialogTitle>平台能力矩阵</DialogTitle>
          <DialogDescription>UnifyAI 同步到各平台的模型 / MCP 支持情况。</DialogDescription>
        </DialogHeader>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>平台</TableHead>
              <TableHead>模型同步</TableHead>
              <TableHead>MCP 同步</TableHead>
              <TableHead>配置文件</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="platform in platforms" :key="platform.id">
              <TableCell class="font-medium">{{ platform.name }}</TableCell>
              <TableCell>
                <Badge :variant="platform.modelSync ? 'default' : 'secondary'">
                  {{ platform.modelSync ? '✓ 支持' : '✗ 不支持' }}
                </Badge>
              </TableCell>
              <TableCell>
                <Badge
                  :variant="
                    platform.mcpSync === true
                      ? 'default'
                      : platform.mcpSync === 'unimplemented'
                        ? 'outline'
                        : 'secondary'
                  "
                >
                  {{
                    platform.mcpSync === true
                      ? '✓ 支持'
                      : platform.mcpSync === 'unimplemented'
                        ? '⚠ 未实现'
                        : '✗ 不支持'
                  }}
                </Badge>
              </TableCell>
              <TableCell class="font-mono text-xs">{{ platform.configPath }}</TableCell>
            </TableRow>
          </TableBody>
        </Table>
        <p class="text-xs leading-5 text-muted-foreground">
          提示：Codex / Claude Code 仅支持 MCP 同步；Reasonix 的 MCP 写入未实现（跳过）； 模型同步对
          OpenCode 为全量覆盖写入，执行前请确认已备份。
        </p>
        <DialogFooter><Button variant="ghost" @click="helpOpen = false">关闭</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
