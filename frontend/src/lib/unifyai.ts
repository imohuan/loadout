// ============================================================================
import { getLoadoutBase } from '@/lib/base'
// UnifyAI 配置同步 —— 前端数据层
//
// 本文件只承载「类型定义 + 平台能力矩阵 + 界面初始数据」，以及通过后端 API 获取配置的
// 数据层函数。
//
// 同步模型（与 UI-DESIGN-SPEC.md §1 一致）：
//   - 模型：全量覆盖写平台配置（源：OpenCodex 代理 / Provider API）
//   - MCP ：矩阵驱动（源：./mcp.json 或 ~/.unifyai/mcp.json）——勾选=开启/添加、未勾选不碰、
//           forceMcp 开启时（UI 默认 true）目标平台未在矩阵中的服务器将被删除（重置语义）
// ============================================================================

// ---- 平台能力矩阵（对应文档 §2，代码内 ADAPTERS 常量的 UI 镜像） ----

export type PlatformId = 'opencode' | 'codex' | 'claudecode' | 'reasonix' | 'penguin' | 'workbuddy'

/** MCP 同步支持度：true=支持；false=不支持；'unimplemented'=代码未实现（Reasonix） */
export type McpSyncSupport = boolean | 'unimplemented'

export interface Platform {
  id: PlatformId
  name: string
  /** 平台卡片品牌色（仅作图标底色，正文仍用语义色） */
  color: string
  /** 模型同步能力 */
  modelSync: boolean
  /** MCP 同步能力 */
  mcpSync: McpSyncSupport
  /** 配置文件路径（卡片 hover 展示） */
  configPath: string
  /** 配置文件格式 */
  format: string
}

export const PLATFORMS: Platform[] = [
  {
    id: 'opencode',
    name: 'OpenCode',
    color: '#3b82f6',
    modelSync: true,
    mcpSync: true,
    configPath: '~/.config/opencode/opencode.json',
    format: 'JSONC',
  },
  {
    id: 'codex',
    name: 'Codex',
    color: '#f59e0b',
    modelSync: false,
    mcpSync: true,
    configPath: '~/.codex/config.toml',
    format: 'TOML',
  },
  {
    id: 'claudecode',
    name: 'Claude Code',
    color: '#d97757',
    modelSync: false,
    mcpSync: true,
    configPath: '~/.claude.json',
    format: 'JSON',
  },
  {
    id: 'reasonix',
    name: 'Reasonix',
    color: '#10b981',
    modelSync: true,
    mcpSync: 'unimplemented',
    configPath: '%APPDATA%/reasonix/config.toml',
    format: 'TOML',
  },
  {
    id: 'penguin',
    name: 'PenguinHarness',
    color: '#8b5cf6',
    modelSync: true,
    mcpSync: true,
    configPath: '~/.penguin/data/default_project/.project_config.toml',
    format: 'TOML / YAML',
  },
  {
    id: 'workbuddy',
    name: 'WorkBuddy',
    color: '#06b6d4',
    modelSync: true,
    mcpSync: true,
    configPath: '~/.workbuddy/models.json',
    format: 'JSON',
  },
]

// ---- MCP 服务器（源：mcp.json，文档 §5.3，由后端直接读写文件） ----

export interface McpServerInfo {
  name: string
  type: 'local' | 'remote'
  enabled: boolean
  /** local：启动命令数组 */
  command?: string[]
  /** remote：网关地址 */
  url?: string
  /** remote：认证 header（仅展示是否存在，不回显密钥） */
  hasAuth?: boolean
  /** remote：完整请求头（写回 mcp.json 用） */
  headers?: Record<string, string>
  /** local：stdio 进程环境变量（写回 mcp.json 用） */
  env?: Record<string, string>
}

/** 界面初始 MCP 列表（后端 mcp.json 读取失败时的兜底，与 mcp.example.json 一致）。 */
export const INITIAL_MCP_SERVERS: McpServerInfo[] = [
  {
    name: 'filesystem',
    type: 'local',
    enabled: true,
    command: ['npx', '-y', '@modelcontextprotocol/server-filesystem', '/path'],
  },
  {
    name: 'node_env',
    type: 'local',
    enabled: true,
    command: ['npx', '-y', '@modelcontextprotocol/server-node-env'],
  },
  {
    name: 'github',
    type: 'remote',
    enabled: true,
    url: 'https://mcp-gateway.example.com/github',
    hasAuth: true,
  },
  {
    name: 'remote-server',
    type: 'remote',
    enabled: true,
    url: 'https://mcp-gateway.example.com',
    hasAuth: true,
  },
  {
    name: 'legacy-search',
    type: 'local',
    enabled: false,
    command: ['npx', '-y', '@modelcontextprotocol/server-legacy'],
  },
]

// ---- 模型来源（文档 §5.2：OpenRouter 元数据缓存 + 降级链路） ----

export type ModelSourceKind = 'openrouter' | 'none'

export interface ModelSourceStatus {
  kind: ModelSourceKind
  /** OpenRouter API 地址 */
  baseUrl: string
  /** API Key（掩码后展示），未配置为空串 */
  apiKeyMasked: string
  /** 元数据缓存模型数量 */
  modelCount: number
  /** 其中支持视觉的模型数 */
  visionCount: number
  /** 其中支持思考的模型数 */
  reasoningCount: number
  /** 缓存文件修改时间（RFC3339），无缓存为空串 */
  cachedAt: string
  /** 缓存缺失/异常原因 */
  degraded?: string
}

/** 界面初始模型来源（后端接口失败时的兜底）。 */
export const INITIAL_MODEL_SOURCE: ModelSourceStatus = {
  kind: 'none',
  baseUrl: 'https://openrouter.ai/api/v1',
  apiKeyMasked: '',
  modelCount: 0,
  visionCount: 0,
  reasoningCount: 0,
  cachedAt: '',
  degraded: '未加载',
}

// ---- OpenCodex 代理模型列表（文档 §5.2 降级链路的首选源） ----

export interface OpenCodexModel {
  provider: string
  modelId: string
  displayName: string
  contextWindow: number | null
  maxOutputTokens: number | null
  /** 匹配 OpenRouter 元数据后的能力标记 */
  supportsVision?: boolean
  supportsThinking?: boolean
}

export interface OpenCodexModelsResult {
  source: string
  proxyUrl: string
  port: number
  hasApiKey: boolean
  apiKeyPreview: string | null
  providerCount: number
  enabledProviderCount: number
  rawCount: number
  degraded: boolean
  degradedReason: string | null
  /** 命中 OpenRouter 元数据缓存的模型数 */
  orMatchedCount?: number
  /** OpenRouter 缓存总模型数 */
  orTotal?: number
  models: OpenCodexModel[]
  count: number
  error?: string
}

/** 按 provider 分组的模型列表（用于 UI 折叠展示）。 */
export interface OpenCodexGroup {
  provider: string
  models: OpenCodexModel[]
}

/** 界面初始 OpenCodex 模型来源（后端接口失败时的兜底）。 */
export const INITIAL_OPENCODEX_MODELS: OpenCodexModelsResult = {
  source: '',
  proxyUrl: 'http://localhost:10100/v1/models',
  port: 10100,
  hasApiKey: false,
  apiKeyPreview: null,
  providerCount: 0,
  enabledProviderCount: 0,
  rawCount: 0,
  degraded: true,
  degradedReason: '未加载',
  orMatchedCount: 0,
  orTotal: 0,
  models: [],
  count: 0,
}

/** 拉取 OpenCodex 代理模型列表（后端调 unifyai --list-models --json）。
 *  enableVision=true 时强制所有模型标记为支持视觉（--enable-vision）。 */
export async function fetchOpenCodexModels(enableVision = false): Promise<OpenCodexModelsResult> {
  try {
    const qs = enableVision ? '?enableVision=1' : ''
    const res = await fetch(`/api/unifyai/opencodex-models${qs}`)
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = (await res.json()) as OpenCodexModelsResult
    return { ...INITIAL_OPENCODEX_MODELS, ...data }
  } catch (err) {
    console.warn('[unifyai] 获取 OpenCodex 模型列表失败，使用初始占位', err)
    return { ...INITIAL_OPENCODEX_MODELS, degradedReason: '后端接口不可用' }
  }
}

/** 按 provider 分组，保持出现顺序。 */
export function groupOpenCodexModels(models: OpenCodexModel[]): OpenCodexGroup[] {
  const groups: OpenCodexGroup[] = []
  for (const m of models) {
    let g = groups.find((x) => x.provider === m.provider)
    if (!g) {
      g = { provider: m.provider, models: [] }
      groups.push(g)
    }
    g.models.push(m)
  }
  return groups
}

// ---- 同步状态机（文档 §6.2） ----

export type SyncStage = 'idle' | 'preview' | 'running' | 'done'

export interface PlatformResult {
  platformId: PlatformId
  status: 'success' | 'failed' | 'skipped'
  models?: number
  mcps?: number
  error?: string
}

// ---- 日志条目（文档 §8 图标语义） ----

export type LogLevel =
  'info' | 'success' | 'warn' | 'error' | 'skip' | 'backup' | 'sync' | 'thinking' | 'vision'

export interface SyncLogEntry {
  id: number
  level: LogLevel
  message: string
  /** 关联的平台（平台内缩进显示用） */
  platformId?: PlatformId
}

/** 日志级别 → 展示图标（对应文档 §8.7） */
export const LOG_ICONS: Record<LogLevel, string> = {
  info: '•',
  success: '✓',
  warn: '⚠',
  error: '✗',
  skip: '⊘',
  backup: '💾',
  sync: '📦',
  thinking: '🧠',
  vision: '👁',
}

// ---- 同步内容三态（文档 §3.2） ----

export type SyncMode = 'all' | 'models' | 'mcp'

/**
 * 读取 OpenRouter 模型来源与元数据缓存状态（后端读 ~/.unifyai/cache/openrouter-models.json）。
 * 失败时回落 INITIAL_MODEL_SOURCE（kind=none），保证页面可用。
 */
export async function fetchModelSource(): Promise<ModelSourceStatus> {
  try {
    const res = await fetch('/api/unifyai/model-source')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = (await res.json()) as ModelSourceStatus
    return { ...INITIAL_MODEL_SOURCE, ...data }
  } catch (err) {
    console.warn('[unifyai] 获取模型来源失败，使用初始占位', err)
    return { ...INITIAL_MODEL_SOURCE, degraded: '后端接口不可用' }
  }
}

/**
 * 从后端读取 MCP 服务器列表（后端直接读 mcp.json，不经 CLI）。
 * 失败时回落到内置示例，保证页面可用。
 */
export async function fetchMcpServers(): Promise<McpServerInfo[]> {
  try {
    const res = await fetch('/api/unifyai/mcp-servers')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = (await res.json()) as { servers?: McpServerInfo[] }
    if (data.servers?.length) return data.servers
  } catch {
    // 后端未接入时保持默认
  }
  return INITIAL_MCP_SERVERS
}

/**
 * 把服务器列表写回 mcp.json（后端全量替换）。
 */
export async function saveMcpServers(servers: McpServerInfo[]): Promise<void> {
  const res = await fetch('/api/unifyai/mcp-servers', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ servers }),
  })
  if (!res.ok) throw new Error(`保存 MCP 配置失败：HTTP ${res.status}`)
}

/**
 * MCP 管理的三类端点：单 MCP（上游服务器）、分组（tool group）、聚合（smart aggregate）。
 * 与 useMcpManagement.ts 的 McpEndpoint.kind 同义，独立维护避免互相耦合。
 */
export type McpImportKind = '单 MCP' | '分组' | '聚合'

/**
 * 从 MCP 管理导入时，列表里每项的展示 + 预转换好的 mcp.json 条目。
 * server 字段已经是可直接落 mcp.json 的 McpServerInfo 形态，单 MCP 为 local/remote 原始，
 * 分组/聚合统一为 remote（URL 指向本机 loadout server 的 /mcp/<label>）。
 */
export interface McpImportSource {
  name: string
  kind: McpImportKind
  /** loadout server 上的端点路径，例如 /mcp/beimai_june、/mcp/@到的、/mcp/$smart */
  path: string
  /** 该端点暴露的工具数，仅展示用 */
  count: number
  /** 转换后可直接 append 到 mcp.json 的条目 */
  server: McpServerInfo
}

/**
 * 从 MCP 管理（Loadout 上游服务器 / 分组 / 聚合）导入为 mcp.json 条目：
 * - stdio → local(command=[cmd, ...args])
 * - http/sse → remote(url + headers)
 * - 分组 → remote(url = origin + /mcp/<group>)
 * - 聚合 → remote(url = origin + /mcp/$smart)
 *
 * 三类端点都能用同一个导入对话框选，落到 mcp.json 后由 UnifyAI 同步给下游工具。
 */
export async function fetchManagedMcpServers(): Promise<McpImportSource[]> {
  const origin = await getLoadoutBase()
  // 三个接口互不依赖 → 并行请求；任一失败不阻塞导入（catch 兜底 null → 空数组）。
  // Go 后端空表会返回 JSON `null`（useMcpManagement 用 `?? []` 防御同理），
  // 这里统一在 parseArray 里兜底，避免 .map/.reduce 在 null 上抛 TypeError。
  async function parseArray(res: Response | null): Promise<unknown[]> {
    if (!res?.ok) return []
    try {
      const parsed = await res.json()
      return Array.isArray(parsed) ? parsed : []
    } catch {
      return []
    }
  }
  type ServerItem = {
    name: string
    transport: string
    command?: string
    args?: string[]
    url?: string
    headers?: Record<string, string>
    env?: Record<string, string>
    enabled?: boolean
  }
  type GroupItem = { name: string; tools?: unknown[] }
  type ToolsItem = { tools?: unknown[] }
  const [serverList, groupList, toolsList] = (await Promise.all([
    fetch('/api/mcp-servers').then(parseArray).catch(() => []),
    fetch('/api/groups').then(parseArray).catch(() => []),
    fetch('/api/mcp-tools').then(parseArray).catch(() => []),
  ])) as [ServerItem[], GroupItem[], ToolsItem[]]
  // 聚合端点暴露的工具总数（与 useMcpManagement 端 $smart 计数口径一致）
  const aggregateCount = toolsList.reduce(
    (sum: number, item: ToolsItem) => sum + (item.tools?.length || 0),
    0,
  )

  const single = serverList.map<McpImportSource>((item) => {
    const enabled = item.enabled !== false
    if (item.transport === 'stdio') {
      return {
        name: item.name,
        kind: '单 MCP' as const,
        path: `/mcp/${item.name}`,
        count: 0,
        server: {
          name: item.name,
          type: 'local' as const,
          enabled,
          command: [item.command || 'npx', ...(item.args || [])],
          env: item.env,
        },
      }
    }
    const hasHeaders = !!(item.headers && Object.keys(item.headers).length)
    return {
      name: item.name,
      kind: '单 MCP' as const,
      path: `/mcp/${item.name}`,
      count: 0,
      server: {
        name: item.name,
        type: 'remote' as const,
        enabled,
        url: item.url || '',
        headers: item.headers,
        hasAuth: hasHeaders,
      },
    }
  })

  const groups = groupList.map<McpImportSource>((group) => ({
    name: group.name,
    kind: '分组' as const,
    path: `/mcp/${group.name}`,
    count: group.tools?.length || 0,
    server: {
      name: group.name,
      type: 'remote' as const,
      enabled: true,
      url: `${origin}/mcp/${encodeURIComponent(group.name)}`,
    },
  }))

  const aggregate: McpImportSource = {
    // 聚合端点固定是 loadout server 上的 /mcp/$smart，但 mcp.json 的 key 不能带 $
    // （OpenCode/Codex/Claude 等 TOML/JSON 配置对 `$` 不友好），落到同步工具前
    // 重命名为 `mcp-smart`。name 是 UI 列表的勾选 key + mcp.json key，前后一致；
    // path / url 仍是 `$smart`，指向原聚合端点不变。
    name: 'mcp-smart',
    kind: '聚合' as const,
    path: '/mcp/$smart',
    count: aggregateCount,
    server: {
      name: 'mcp-smart',
      type: 'remote' as const,
      enabled: true,
      url: `${origin}/mcp/$smart`,
    },
  }

  return [...single, ...groups, aggregate]
}

/**
 * kind → 徽标 tint 配色（沿用全局徽标/指标 tint 规范：
 *   bg-{color}-500/15 text-{color}-700 dark:text-{color}-300 border-{color}-500/20）。
 * 与 McpPanel endpoints 列表同色家族，便于跨页面识别。
 */
export function importKindBadgeClass(kind: McpImportKind): string {
  switch (kind) {
    case '单 MCP':
      return 'bg-slate-500/15 text-slate-700 dark:text-slate-300 border-slate-500/20'
    case '分组':
      return 'bg-violet-500/15 text-violet-700 dark:text-violet-300 border-violet-500/20'
    case '聚合':
      return 'bg-amber-500/15 text-amber-700 dark:text-amber-300 border-amber-500/20'
  }
}

/** --list all --json 的 metadata 部分（OpenRouter 元数据缓存状态）。 */
export interface McpMetadataStatus {
  path: string
  modelCount: number
  cachedAt: string | null
  degraded?: string
}

/** 后端 --list platforms / --list all 的原始平台结构（unifyai CLI 输出，含 modelStatus/mcpStatus）。 */
export interface BackendPlatform {
  id: string
  name: string
  supportsModels?: boolean
  modelStatus?: string
  supportsMcp?: boolean
  mcpStatus?: string
  configPath?: string
  configFormat?: string
}

/** --list all --json 完整输出（前端初始化一次拉全全部配置）。 */
export interface AllConfigResult {
  platforms: BackendPlatform[]
  models: OpenCodexModelsResult
  mcp: McpMatrixResult
  metadata: McpMetadataStatus
}

/**
 * 一次获取全部配置（后端调 unifyai --list all --json）：
 * 平台能力 + 模型列表 + MCP 矩阵 + 元数据缓存状态。
 * 失败回落内置默认，保证页面可用。
 */
export async function fetchAllConfig(): Promise<AllConfigResult> {
  try {
    const res = await fetch('/api/unifyai/all')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const data = (await res.json()) as AllConfigResult
    return {
      platforms: data.platforms ?? [],
      models: data.models ?? INITIAL_OPENCODEX_MODELS,
      mcp: data.mcp ?? { source: null, platforms: [] },
      metadata: data.metadata ?? { path: '', modelCount: 0, cachedAt: null },
    }
  } catch (err) {
    console.warn('[unifyai] 获取全部配置失败，使用内置默认', err)
    return {
      platforms: [],
      models: INITIAL_OPENCODEX_MODELS,
      mcp: { source: null, platforms: [] },
      metadata: { path: '', modelCount: 0, cachedAt: null, degraded: '后端接口不可用' },
    }
  }
}

/** 同步配置文件路径（后端 ~/.unifyai/sync.json，命令预览与执行共用）。 */
export const SYNC_CONFIG_PATH = '~/.unifyai/sync.json'

/**
 * 把前端当前同步配置写入 ~/.unifyai/sync.json（后端 PUT），返回实际路径。
 * 执行同步前必须调用：sync.json 是 --config 引用的唯一配置来源。
 */
export async function saveSyncConfig(config: unknown): Promise<string> {
  const res = await fetch('/api/unifyai/sync-config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: typeof config === 'string' ? config : JSON.stringify(config),
  })
  if (!res.ok) throw new Error(`保存同步配置失败：HTTP ${res.status}`)
  const data = (await res.json()) as { path?: string }
  return data.path || SYNC_CONFIG_PATH
}

/**
 * 构建 CLI 参数数组（纯前端逻辑，无需后端）。命令预览与真实执行共用。
 *
 * 统一走 --config：UI 状态先由 saveSyncConfig 落盘到 sync.json，执行/预览一律
 * `--config <path>`（一个配置文件承载全部同步配置，替代旧的多个 flag 与内联 JSON）。
 */
export function buildArgs(_opts: {
  mode: SyncMode
  all: boolean
  platforms: PlatformId[]
  mcpPlatforms: PlatformId[] | null // null = 未限定（全部平台同步 MCP）
  globalExcludes: string[]
  perPlatformExcludes: Record<PlatformId, string[]>
  dryRun: boolean
  source: string
  verbose: boolean
  /**
   * 多模态视觉开关（--enable-vision）。由前端「强制视觉」Switch 显式传入：
   * true → 配置 enableVision；false / 未传 → 不设置。
   */
  enableVision?: boolean
}): string[] {
  const args = ['--config', SYNC_CONFIG_PATH]
  // dryRun 不进 sync.json，作为 CLI flag 传入（--config 与 --dry-run 可叠加）
  if (_opts.dryRun) args.push('--dry-run')
  return args
}

/** 把 UI 选项映射为同步配置对象（saveSyncConfig 落盘的内容）。
 * 包含模式/平台/MCP/视觉/源，CLI 仅消费 mode/platforms/mcp/enableVision/source/forceMcp。
 */
export function buildConfigObject(opts: Parameters<typeof buildArgs>[0]): Record<string, unknown> {
  const cfg: Record<string, unknown> = {
    mode: opts.mode,
    // all 与 platforms 二选一：all=true 时只写 all，不写 platforms（CLI 以 all 为准）
    ...(opts.all ? { all: true } : { platforms: opts.platforms }),
  }
  // dryRun 不写入 sync.json，由 CLI --dry-run flag 传入
  if (opts.enableVision) cfg.enableVision = true
  cfg.source = opts.source
  // 强制重置 MCP：默认开启（目标平台现有但不在同步列表的服务器全部禁用/移除），
  // UI 不提供开关，想关闭需手动改 sync.json 的 forceMcp: false
  cfg.forceMcp = true
  if (opts.mcpPlatforms?.length) cfg.mcp = { platforms: opts.mcpPlatforms }
  else cfg.mcp = {}
  if (opts.globalExcludes.length) (cfg.mcp as Record<string, unknown>).exclude = opts.globalExcludes
  const excludeFor: Record<string, string[]> = {}
  for (const [platform, names] of Object.entries(opts.perPlatformExcludes)) {
    if (names.length) excludeFor[platform] = names
  }
  if (Object.keys(excludeFor).length) (cfg.mcp as Record<string, unknown>).excludeFor = excludeFor

  return cfg
}

/** 命令预览文本：npx unifyai@latest --config <path>（与后端 resolveCmd 执行方式一致） */
export function buildCommand(_opts: Parameters<typeof buildArgs>[0]): string {
  return ['npx', 'unifyai@latest', '--config', SYNC_CONFIG_PATH].join(' ')
}

export const DEFAULT_SOURCE = '~/.opencodex/config.json'


// ============================================================================
// MCP 同步矩阵（--list-mcp --json 数据层）
// ============================================================================

/** --list-mcp --json 中单个服务器条目（config 为平台/mcp.json 原始配置）。 */
export interface McpMatrixServerItem {
  name: string
  enabled: boolean
  config?: Record<string, unknown>
}

/** --list-mcp --json 的 source 字段（源 mcp.json 全集）。 */
export interface McpMatrixSource {
  path: string
  servers: McpMatrixServerItem[]
}

/** --list-mcp --json 中单个平台的状态。 */
export interface McpMatrixPlatform {
  platform: string
  name: string
  configPath: string
  /** false = 该平台 MCP 不可读（Reasonix），矩阵列禁用 */
  readable: boolean
  servers: McpMatrixServerItem[]
}

/** --list-mcp --json 完整输出。 */
export interface McpMatrixResult {
  source: McpMatrixSource | null
  platforms: McpMatrixPlatform[]
}

/**
 * 矩阵单元格四态：
 * - true = 开启（写 enabled:true）
 * - false = 关闭（OpenCode/Codex 写 enabled:false，Claude/Penguin 移除）
 * - 'remove' = 删除条目（所有平台直接删配置）
 * - undefined = 该平台未配置（点击=添加）
 */
export type McpMatrixCell = boolean | 'remove' | undefined

/**
 * 获取 MCP 矩阵数据（后端调 unifyai --list-mcp --json）。
 * 失败返回空结构（前端回落内置默认），保证页面可用。
 */
export async function fetchMcpMatrix(): Promise<McpMatrixResult> {
  try {
    const res = await fetch('/api/unifyai/mcp-matrix')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    return (await res.json()) as McpMatrixResult
  } catch (err) {
    console.warn('[unifyai] 获取 MCP 矩阵失败', err)
    return { source: null, platforms: [] }
  }
}

/** --import-mcp：合并各平台 MCP 配置到源 mcp.json。 */
export const IMPORT_MCP_ARGS = ['--import-mcp']

/** 把 UI 矩阵转置为同步配置对象的 mcp.matrix 结构（saveSyncConfig 落盘用）。 */
export function buildMatrixConfig(
  matrix: Record<string, Record<PlatformId, McpMatrixCell>>,
): Record<string, unknown> {
  const transposed: Record<string, Record<string, boolean | 'remove'>> = {}
  for (const [name, row] of Object.entries(matrix)) {
    for (const [pid, cell] of Object.entries(row)) {
      if (cell === undefined) continue
      if (!transposed[pid]) transposed[pid] = {}
      transposed[pid][name] = cell
    }
  }
  const clean: Record<string, Record<string, boolean | 'remove'>> = {}
  for (const [pid, entries] of Object.entries(transposed)) {
    if (Object.keys(entries).length > 0) clean[pid] = entries
  }
  return { mcp: { matrix: clean } }
}
