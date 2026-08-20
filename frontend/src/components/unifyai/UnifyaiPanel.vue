<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiCheckLine,
  RiCloseLine,
  RiInformationLine,
  RiLoader4Line,
  RiPlayLine,
  RiQuestionLine,
  RiRefreshLine,
  RiSettings3Line,
} from '@remixicon/vue'
import PageHeader from '@/components/PageHeader.vue'
import PlatformCard from '@/components/unifyai/PlatformCard.vue'
import ExcludeMatrix from '@/components/unifyai/ExcludeMatrix.vue'
import CommandPreview from '@/components/unifyai/CommandPreview.vue'
import {
  DEFAULT_SOURCE,
  INITIAL_MCP_SERVERS,
  LOG_ICONS,
  PLATFORMS,
  buildCommand,
  fetchMcpServers,
  fetchModelSource,
  updateMetadata,
  type LogLevel,
  type McpServerInfo,
  type ModelSourceStatus,
  type Platform,
  type PlatformId,
  type PlatformResult,
  type SyncLogEntry,
  type SyncMode,
} from '@/lib/unifyai'

// ---------- ① 同步内容三态 ----------
const modeTab = ref<SyncMode>('all')
const mode = computed<SyncMode>(() => modeTab.value)

// ---------- ② 目标平台 ----------
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

// ---------- ③ MCP 过滤 ----------
const allServers = ref<McpServerInfo[]>(INITIAL_MCP_SERVERS)
/** 已禁用的服务器名集合（UI 仍展示但参与同步时跳过） */
const disabledServers = ref<Set<string>>(
  new Set(INITIAL_MCP_SERVERS.filter((s) => !s.enabled).map((s) => s.name)),
)
/** 实际参与同步的服务器（不在 disabled 集合中） */
const enabledServers = computed(() =>
  allServers.value.filter((server) => !disabledServers.value.has(server.name)),
)

const matrix = ref<Record<string, Record<PlatformId, boolean>>>({})
const whitelist = ref<PlatformId[]>([])
/** 白名单开关：false=不限定（所有平台同步 MCP）；true=仅 whitelist 中的平台同步 */
const whitelistEnabled = ref(false)

/** 开关 v-model 包装（get/set） */
const whitelistChecked = computed({
  get: () => whitelistEnabled.value,
  set: (value: boolean) => (whitelistEnabled.value = value),
})

function toggleWhitelistPlatform(platformId: PlatformId) {
  whitelist.value = whitelist.value.includes(platformId)
    ? whitelist.value.filter((id) => id !== platformId)
    : [...whitelist.value, platformId]
}

function initMatrix() {
  const next: Record<string, Record<PlatformId, boolean>> = {}
  // 所有服务器都建占位（含 disabled），启用后行状态干净
  for (const server of allServers.value) {
    next[server.name] = {
      opencode: false,
      codex: false,
      claudecode: false,
      reasonix: false,
      penguin: false,
    }
  }
  // 文档典型用例：Codex 内置 node_env 默认排除
  if (next.node_env) next.node_env.codex = true
  matrix.value = next
}

// ---------- ④ 数据源状态 ----------
const modelSource = ref<ModelSourceStatus>({ kind: 'none', url: '', count: 0 })
const mcpSourcePath = ref('./mcp.json（cwd 优先，回退 ~/.unifyai/mcp.json）')

// ---------- 高级选项 ----------
const advancedOpen = ref(false)
const sourcePath = ref(DEFAULT_SOURCE)
const verbose = ref(false)

// ---------- 命令拼装 ----------
const globalExcludes = computed(() =>
  enabledServers.value
    .filter((server) =>
      PLATFORMS.every((platform) => matrix.value[server.name]?.[platform.id] === true),
    )
    .map((server) => server.name),
)

const perPlatformExcludes = computed(() => {
  const result: Record<PlatformId, string[]> = {
    opencode: [],
    codex: [],
    claudecode: [],
    reasonix: [],
    penguin: [],
  }
  const global = new Set(globalExcludes.value)
  for (const server of enabledServers.value) {
    if (global.has(server.name)) continue
    for (const platform of PLATFORMS) {
      if (matrix.value[server.name]?.[platform.id]) result[platform.id].push(server.name)
    }
  }
  return result
})

const command = computed(() =>
  buildCommand({
    mode: mode.value,
    all: allPlatforms.value,
    platforms: selectedPlatforms.value,
    mcpPlatforms: whitelistEnabled.value && whitelist.value.length ? whitelist.value : null,
    globalExcludes: globalExcludes.value,
    perPlatformExcludes: perPlatformExcludes.value,
    dryRun: false,
    source: sourcePath.value,
    verbose: verbose.value,
  }),
)

// ---------- ⑤ 执行状态机（文档 §6.2） ----------
type Stage = 'idle' | 'running' | 'done'
const stage = ref<Stage>('idle')
const dryRunMode = ref(false)
const logs = ref<SyncLogEntry[]>([])
const results = ref<PlatformResult[]>([])
const platformRunning = ref<Record<PlatformId, 'pending' | 'running' | 'success' | 'failed' | 'skipped'>>(
  { opencode: 'pending', codex: 'pending', claudecode: 'pending', reasonix: 'pending', penguin: 'pending' },
)
const running = computed(() => stage.value === 'running')
const logBox = ref<HTMLElement | null>(null)
let logId = 0

function pushLog(level: LogLevel, message: string, platformId?: PlatformId) {
  logs.value.push({ id: ++logId, level, message, platformId })
}

watch(logs, async () => {
  await nextTick()
  if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
})

function delay(ms: number) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** 白名单是否放行某平台的 MCP（未启用开关 = 全部放行；启用后空列表 = 全部跳过） */
function whitelistAllows(platformId: PlatformId) {
  return !whitelistEnabled.value || whitelist.value.includes(platformId)
}

/** 目标平台列表（含 --all 展开） */
const effectiveTargets = computed(() =>
  allPlatforms.value ? PLATFORMS : PLATFORMS.filter((p) => selectedPlatforms.value.includes(p.id)),
)

/** 单个平台本次实际要同步的内容（用于日志与汇总） */
function platformPlan(platform: Platform): { models: boolean; mcps: number } {
  const models = mode.value !== 'mcp' && platform.modelSync
  let mcps = 0
  if (mode.value !== 'models' && platform.mcpSync === true && whitelistAllows(platform.id)) {
    const global = new Set(globalExcludes.value)
    const per = new Set(perPlatformExcludes.value[platform.id])
    mcps = enabledServers.value.filter((s) => !global.has(s.name) && !per.has(s.name)).length
  }
  return { models, mcps }
}

/** 生成完整执行序列日志（对应文档 §8 输出格式） */
function buildExecutionLogs(dryRun: boolean) {
  const lines: Array<{ level: LogLevel; message: string; platformId?: PlatformId }> = []
  lines.push({ level: 'info', message: '🚀 AI Config Sync - 配置同步工具' })
  lines.push({ level: 'info', message: `📂 加载配置: ${sourcePath.value}` })
  lines.push({ level: 'success', message: '✓ 加载配置: 3 个 provider' })

  if (mode.value !== 'mcp') {
    lines.push({
      level: 'success',
      message: `✓ 从 OpenCodex 代理服务获取模型列表 (${modelSource.value.url})`,
    })
    lines.push({ level: 'success', message: `  ✓ 获取到 ${modelSource.value.count} 个模型` })
  }
  if (mode.value !== 'models') {
    lines.push({
      level: 'success',
      message: `✓ MCP 配置 (来自 cwd): ${enabledServers.value.length}/${allServers.value.length} 个服务器启用`,
    })
  }

  // 白名单提示
  for (const platform of effectiveTargets.value) {
    if (
      mode.value !== 'models' &&
      platform.mcpSync === true &&
      !whitelistAllows(platform.id)
    ) {
      lines.push({
        level: 'skip',
        message: `⊘ ${platform.name}: MCP 同步已跳过（不在 --mcp-platforms 白名单）`,
        platformId: platform.id,
      })
    }
  }
  // 排除提示
  const excludedNames = [...globalExcludes.value]
  for (const names of Object.values(perPlatformExcludes.value)) excludedNames.push(...names)
  if (mode.value !== 'models' && excludedNames.length) {
    lines.push({ level: 'skip', message: `⊘ 已排除 MCP: ${[...new Set(excludedNames)].join(', ')}` })
  }

  // 逐平台执行
  for (const platform of effectiveTargets.value) {
    const plan = platformPlan(platform)
    const suffix = dryRun ? '（dry-run 预览）' : ''
    lines.push({
      level: 'sync',
      message: `📦 同步到 ${platform.name}...${suffix}`,
      platformId: platform.id,
    })
    if (!dryRun) {
      lines.push({
        level: 'backup',
        message: `  💾 备份: ${platform.format.includes('YAML') ? 'system_config.yaml' : platform.configPath.split('/').pop()}.bak-${Date.now()}`,
        platformId: platform.id,
      })
    }
    if (mode.value !== 'mcp') {
      if (platform.modelSync) {
        lines.push({
          level: 'success',
          message: dryRun
            ? `  → 将同步 ${modelSource.value.count} 个模型`
            : `  → 同步模型配置 (${modelSource.value.count} 个)`,
          platformId: platform.id,
        })
      } else {
        lines.push({
          level: 'skip',
          message: `  ⊘ 该平台不支持模型同步，跳过`,
          platformId: platform.id,
        })
      }
    }
    if (mode.value !== 'models') {
      if (platform.mcpSync === 'unimplemented') {
        lines.push({
          level: 'skip',
          message: `  ⊘ MCP 配置格式待调查，暂时跳过`,
          platformId: platform.id,
        })
      } else if (platform.mcpSync && !whitelistAllows(platform.id)) {
        lines.push({
          level: 'skip',
          message: `  ⊘ MCP 同步已跳过（不在白名单）`,
          platformId: platform.id,
        })
      } else if (platform.mcpSync && plan.mcps > 0) {
        lines.push({
          level: 'success',
          message: dryRun ? `  → 将同步 ${plan.mcps} 个 MCP 服务器` : `  → 同步 MCP 配置 (${plan.mcps} 个)`,
          platformId: platform.id,
        })
      }
    }
    lines.push({
      level: 'success',
      message: dryRun ? `✓ ${platform.name} 预览通过` : `✓ ${platform.name} 同步成功`,
      platformId: platform.id,
    })
  }

  const successCount = effectiveTargets.value.length
  lines.push({ level: 'info', message: '==================================================' })
  lines.push({ level: 'success', message: `✓ 成功: ${successCount} 个平台` })
  lines.push({ level: 'success', message: '✗ 失败: 0 个平台' })
  lines.push({ level: 'info', message: '==================================================' })
  return lines
}

async function runSimulated(dryRun: boolean) {
  if (running.value) return
  stage.value = 'running'
  dryRunMode.value = dryRun
  logs.value = []
  results.value = []
  platformRunning.value = {
    opencode: 'pending',
    codex: 'pending',
    claudecode: 'pending',
    reasonix: 'pending',
    penguin: 'pending',
  }
  const lines = buildExecutionLogs(dryRun)
  for (const line of lines) {
    await delay(220)
    pushLog(line.level, line.message, line.platformId)
    if (line.platformId) platformRunning.value[line.platformId] = 'running'
  }
  // 汇总结果（模拟全部成功）
  results.value = effectiveTargets.value.map((platform) => {
    const plan = platformPlan(platform)
    return {
      platformId: platform.id,
      status: 'success' as const,
      models: plan.models ? modelSource.value.count : undefined,
      mcps: plan.mcps || undefined,
    }
  })
  for (const platform of effectiveTargets.value) {
    platformRunning.value[platform.id] = 'success'
  }
  stage.value = 'done'
}

// ---------- 执行前确认弹窗（文档 §10.3） ----------
const confirmOpen = ref(false)
const confirmWarnings = ref<string[]>([])

function startSync() {
  const warnings: string[] = []
  const targets = effectiveTargets.value
  const hasOpenCode = targets.some((p) => p.id === 'opencode')
  const hasClaude = targets.some((p) => p.id === 'claudecode')
  if (mode.value !== 'mcp' && hasOpenCode)
    warnings.push('OpenCode：模型同步将清空重写 provider 配置，手动配置的其他 provider 会被清除。')
  if (targets.length) warnings.push('目标平台配置文件将被覆盖，执行时自动备份为 .bak-{时间戳}。')
  if (mode.value !== 'models' && hasClaude)
    warnings.push('Claude Code：enabled:false 的服务器将从配置中删除（而非禁用）。')
  if (warnings.length) {
    confirmWarnings.value = warnings
    confirmOpen.value = true
  } else {
    runSimulated(false)
  }
}

// ---------- 帮助 ----------
const helpOpen = ref(false)

// ---------- 元数据缓存刷新（--update-metadata，模拟反馈） ----------
const metadataUpdating = ref(false)
const metadataUpdatedAt = ref('')
async function handleUpdateMetadata() {
  if (metadataUpdating.value) return
  metadataUpdating.value = true
  try {
    await updateMetadata()
    await delay(500)
    metadataUpdatedAt.value = new Date().toLocaleTimeString('zh-CN', { hour12: false })
    toast.success('模型元数据缓存已刷新', {
      description: 'OpenRouter 410+ 模型元数据已更新（context / vision / reasoning）',
    })
  } finally {
    metadataUpdating.value = false
  }
}

// ---------- 日志清空 ----------
function clearLogs() {
  logs.value = []
  results.value = []
  stage.value = 'idle'
  dryRunMode.value = false
}

onMounted(() => {
  initMatrix()
  fetchModelSource().then((source) => (modelSource.value = source))
  fetchMcpServers().then((servers) => {
    allServers.value = INITIAL_MCP_SERVERS
    const names = new Set(servers.map((s) => s.name))
    if (names.size) initMatrix()
  })
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="UnifyAI 配置同步"
      description="把模型与 MCP 配置从 OpenCodex 源同步到 5 个 AI 开发平台的本地配置文件"
    >
      <template #actions>
        <Button variant="outline" :disabled="metadataUpdating" @click="handleUpdateMetadata">
          <RiLoader4Line v-if="metadataUpdating" size="16" class="animate-spin" />
          <RiRefreshLine v-else size="16" />
          {{ metadataUpdating ? '刷新中...' : '更新元数据' }}
        </Button>
        <Button variant="ghost" @click="helpOpen = true">
          <RiQuestionLine size="16" />帮助
        </Button>
      </template>
    </PageHeader>

    <!-- ② 目标平台（头部含同步内容 + 全部平台） -->
    <Card class="rounded-md">
      <CardHeader class="flex flex-col gap-3 space-y-0 lg:flex-row lg:items-center lg:justify-between">
        <div class="space-y-0.5">
          <CardTitle class="text-base">② 目标平台</CardTitle>
          <CardDescription>勾选要同步的平台；不支持所选能力的平台将置灰（执行时跳过并提示）。</CardDescription>
        </div>
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
          <Tabs v-model="modeTab">
            <TabsList variant="line" class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-2">
              <TabsTrigger value="all">全部同步</TabsTrigger>
              <TabsTrigger value="models">仅模型</TabsTrigger>
              <TabsTrigger value="mcp">仅 MCP</TabsTrigger>
            </TabsList>
          </Tabs>
          <div class="flex items-center gap-2">
            <Switch id="all-platforms" v-model="allPlatforms" />
            <Label for="all-platforms" class="cursor-pointer whitespace-nowrap text-sm font-medium">全部平台（--all）</Label>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <div class="grid grid-cols-2 gap-2 md:grid-cols-3" style="grid-template-columns: repeat(5, minmax(0, 1fr));">
          <PlatformCard
            v-for="platform in PLATFORMS"
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

    <!-- ③ MCP 同步过滤（排除矩阵 + 白名单，并列展示） -->
    <Card v-if="mode !== 'models'" class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">③ MCP 同步过滤</CardTitle>
        <CardDescription
          >三个过滤维度：排除矩阵（全局/按平台）、平台白名单，优先级：白名单 → 排除。</CardDescription
        >
      </CardHeader>
      <CardContent class="space-y-6">
        <section>
          <h4 class="mb-3 text-sm font-medium">排除矩阵</h4>
          <ExcludeMatrix
            :servers="allServers"
            :disabled="disabledServers"
            v-model:matrix="matrix"
            @update:disabled="disabledServers = $event"
          />
        </section>
        <Separator />
        <section class="space-y-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0">
              <h4 class="text-sm font-medium">仅以下平台执行 MCP 同步（--mcp-platforms）</h4>
              <p class="mt-0.5 text-xs leading-5 text-muted-foreground">
                未列出的平台将完全跳过 MCP 同步（⊘ 白名单外）。关闭开关 = 所有平台同步 MCP。
              </p>
            </div>
            <Switch v-model="whitelistChecked" />
          </div>
          <div v-if="whitelistEnabled" class="flex flex-wrap gap-2 rounded-md border p-3">
            <button
              v-for="platform in PLATFORMS"
              :key="platform.id"
              type="button"
              class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-sm transition-colors"
              :class="
                whitelist.includes(platform.id)
                  ? 'border-primary bg-primary/10 text-primary'
                  : 'border-border text-muted-foreground hover:border-primary/50'
              "
              :aria-pressed="whitelist.includes(platform.id)"
              @click="toggleWhitelistPlatform(platform.id)"
            >
              <RiCheckLine v-if="whitelist.includes(platform.id)" size="14" />
              {{ platform.name }}
              <Badge v-if="platform.mcpSync === 'unimplemented'" variant="outline" class="px-1 text-[10px]"
                >未实现</Badge
              >
            </button>
          </div>
          <p
            v-if="whitelistEnabled && !whitelist.length"
            class="flex items-center gap-1.5 text-xs text-amber-600"
          >
            <RiInformationLine size="13" />
            白名单为空：所有平台（含已选目标）的 MCP 同步都会被跳过。
          </p>
          <p v-else-if="!whitelistEnabled" class="flex items-center gap-1 text-sm text-muted-foreground">
            <RiCheckLine size="14" class="text-primary" />
            未限定：全部平台同步 MCP。
          </p>
        </section>
      </CardContent>
    </Card>

    <!-- ④ 数据预览 -->
    <CommandPreview
      :command="command"
      :model-source="modelSource"
      :mcp-source-path="mcpSourcePath"
      :mcp-enabled="enabledServers.length"
      :mcp-total="allServers.length"
      :metadata-updated-at="metadataUpdatedAt"
    />

    <!-- ⑤ 高级选项 + 操作区 -->
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">⑤ 执行</CardTitle>
        <CardDescription>预览（dry-run）只展示不写文件；开始同步前会弹确认，备份后写入各平台配置。</CardDescription>
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
            <div class="flex items-center gap-2 pt-5">
              <Switch id="verbose" v-model="verbose" />
              <Label for="verbose" class="cursor-pointer text-sm">显示详细堆栈信息（--verbose）</Label>
            </div>
          </div>
        </div>
        <div class="flex flex-wrap justify-end gap-2">
          <Button variant="outline" :disabled="running" @click="runSimulated(true)">
            <RiPlayLine size="16" />预览（dry-run）
          </Button>
          <Button :disabled="running" @click="startSync">
            <RiLoader4Line v-if="running" size="16" class="animate-spin" />
            <RiPlayLine v-else size="16" />
            开始同步
          </Button>
        </div>
      </CardContent>
    </Card>

    <!-- ⑥ 日志 / 结果 -->
    <Card v-if="logs.length" class="rounded-md">
      <CardHeader class="space-y-3">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <div class="space-y-0.5">
            <CardTitle class="text-base">
              {{ dryRunMode ? '⑥ 预览结果（未写入任何文件）' : '⑥ 执行结果' }}
            </CardTitle>
            <CardDescription>实时滚动日志，图标语义：✓ 成功 / ⚠ 警告 / ✗ 失败 / ⊘ 排除跳过。</CardDescription>
          </div>
          <div class="flex items-center gap-2">
            <Badge v-if="dryRunMode" variant="outline">--dry-run</Badge>
            <Button variant="ghost" size="sm" @click="clearLogs">清空</Button>
          </div>
        </div>
        <div class="flex flex-wrap gap-1.5">
          <Badge
            v-for="platform in PLATFORMS"
            :key="platform.id"
            :variant="
              platformRunning[platform.id] === 'success'
                ? 'default'
                : platformRunning[platform.id] === 'pending'
                  ? 'outline'
                  : 'secondary'
            "
            class="gap-1 font-normal"
          >
            <RiLoader4Line
              v-if="platformRunning[platform.id] === 'running'"
              size="12"
              class="animate-spin"
            />
            <RiCheckLine v-else-if="platformRunning[platform.id] === 'success'" size="12" />
            <RiCloseLine v-else size="12" />
            {{ platform.name }}
          </Badge>
        </div>
      </CardHeader>
      <CardContent class="space-y-4">
        <div
          ref="logBox"
          class="max-h-72 overflow-y-auto rounded-md border bg-muted/40 p-3 font-mono text-xs leading-6"
        >
          <div
            v-for="entry in logs"
            :key="entry.id"
            class="flex gap-2 whitespace-pre-wrap break-all"
            :class="{
              'text-destructive': entry.level === 'error',
              'text-amber-600': entry.level === 'warn' || entry.level === 'skip',
              'text-emerald-600': entry.level === 'success',
              'text-muted-foreground': entry.level === 'info',
            }"
          >
            <span class="shrink-0 select-none">{{ LOG_ICONS[entry.level] }}</span>
            <span>{{ entry.message }}</span>
          </div>
        </div>
        <div v-if="stage === 'done'" class="grid gap-3 sm:grid-cols-3">
          <div class="rounded-md border bg-emerald-500/5 p-3">
            <div class="text-xs text-muted-foreground">成功平台</div>
            <div class="mt-1 text-2xl font-semibold text-emerald-600">
              {{ results.filter((r) => r.status === 'success').length }}
            </div>
          </div>
          <div class="rounded-md border p-3">
            <div class="text-xs text-muted-foreground">失败平台</div>
            <div class="mt-1 text-2xl font-semibold">
              {{ results.filter((r) => r.status === 'failed').length }}
            </div>
          </div>
          <div class="rounded-md border p-3">
            <div class="text-xs text-muted-foreground">同步模型 / MCP 服务器</div>
            <div class="mt-1 text-2xl font-semibold">
              {{ modelSource.count }} / {{ enabledServers.length }}
            </div>
          </div>
        </div>
      </CardContent>
    </Card>

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
          <Button @click="confirmOpen = false; runSimulated(false)">确认，开始同步</Button>
          <Button variant="ghost" @click="confirmOpen = false">取消</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>

    <!-- 帮助弹窗 -->
    <Dialog v-model:open="helpOpen">
      <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl!">
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
            <TableRow v-for="platform in PLATFORMS" :key="platform.id">
              <TableCell class="font-medium">{{ platform.name }}</TableCell>
              <TableCell>
                <Badge :variant="platform.modelSync ? 'default' : 'secondary'">
                  {{ platform.modelSync ? '✓ 支持' : '✗ 不支持' }}
                </Badge>
              </TableCell>
              <TableCell>
                <Badge
                  :variant="
                    platform.mcpSync === true ? 'default' : platform.mcpSync === 'unimplemented' ? 'outline' : 'secondary'
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
          提示：Codex / Claude Code 仅支持 MCP 同步；Reasonix 的 MCP 写入未实现（跳过）；
          模型同步对 OpenCode 为全量覆盖写入，执行前请确认已备份。
        </p>
        <DialogFooter><Button variant="ghost" @click="helpOpen = false">关闭</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
