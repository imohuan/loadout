// ============================================================================
// UnifyAI 配置同步 —— 前端数据层
//
// 本文件只承载「类型定义 + 平台能力矩阵 + 界面初始数据」，以及供后台接入的
// 函数签名（当前返回 mock / 抛出 TODO，等待后端实现后替换为真实 API 调用）。
//
// 同步模型（与 UI-DESIGN-SPEC.md §1 一致）：
//   - 模型：全量覆盖写平台配置（源：OpenCodex 代理 / Provider API）
//   - MCP ：增量合并（源：./mcp.json 或 ~/.unifyai/mcp.json），同名覆盖保留未同步项
// ============================================================================

// ---- 平台能力矩阵（对应文档 §2，代码内 ADAPTERS 常量的 UI 镜像） ----

export type PlatformId = 'opencode' | 'codex' | 'claudecode' | 'reasonix' | 'penguin'

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

// ---- 模型来源（文档 §5.2 降级链路） ----

export type ModelSourceKind = 'proxy' | 'fallback' | 'none'

export interface ModelSourceStatus {
  kind: ModelSourceKind
  /** 代理/Provider 地址 */
  url: string
  /** 模型数量 */
  count: number
  /** 降级原因（proxy 失败时） */
  degraded?: string
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
  | 'info'
  | 'success'
  | 'warn'
  | 'error'
  | 'skip'
  | 'backup'
  | 'sync'
  | 'thinking'
  | 'vision'

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

// ============================================================================
// 预留的后台接入点 —— 后端就绪后替换 mock 实现即可，UI 侧无需改动。
// ============================================================================

const NOT_IMPLEMENTED = '后端接口未接入，当前返回模拟数据'

/**
 * 读取模型源配置并获取模型列表（文档 §5.2）：
 *   ① OpenCodex 代理 http://localhost:10100/v1/models（3s 超时）
 *   ② 失败 → 逐个 Provider GET {baseUrl}/models（需 baseUrl + apiKey）
 * TODO(backend): 替换为真实调用后返回 ModelSourceStatus。
 */
export async function fetchModelSource(): Promise<ModelSourceStatus> {
  console.warn(NOT_IMPLEMENTED)
  return { kind: 'proxy', url: 'http://localhost:10100', count: 372 }
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
 * 从 MCP 管理（Loadout 上游服务器）导入为 mcp.json 条目：
 * stdio → local(command=[cmd, ...args])；http/sse → remote(url + headers)。
 */
export async function fetchManagedMcpServers(): Promise<McpServerInfo[]> {
  const res = await fetch('/api/mcp-servers')
  if (!res.ok) throw new Error(`读取 MCP 管理失败：HTTP ${res.status}`)
  const list = (await res.json()) as Array<{
    name: string
    transport: string
    command?: string
    args?: string[]
    url?: string
    headers?: Record<string, string>
    enabled?: boolean
  }>
  return (list || []).map((item) => {
    if (item.transport === 'stdio') {
      return {
        name: item.name,
        type: 'local' as const,
        enabled: item.enabled !== false,
        command: [item.command || 'npx', ...(item.args || [])],
      }
    }
    return {
      name: item.name,
      type: 'remote' as const,
      enabled: item.enabled !== false,
      url: item.url || '',
      headers: item.headers,
      hasAuth: !!(item.headers && Object.keys(item.headers).length),
    }
  })
}

/**
 * 构建 CLI 参数数组（纯前端逻辑，无需后端）。命令预览与真实执行共用。
 * @param opts 由 UI 状态翻译来的参数
 */
export function buildArgs(opts: {
  mode: SyncMode
  all: boolean
  platforms: PlatformId[]
  mcpPlatforms: PlatformId[] | null // null = 未限定（全部平台同步 MCP）
  globalExcludes: string[]
  perPlatformExcludes: Record<PlatformId, string[]>
  dryRun: boolean
  source: string
  verbose: boolean
}): string[] {
  const args: string[] = []
  if (opts.mode === 'models') args.push('--models-only')
  if (opts.mode === 'mcp') args.push('--mcp-only')
  if (opts.all) args.push('--all')
  else args.push(`--platforms ${opts.platforms.join(',')}`)
  if (opts.mcpPlatforms?.length) args.push(`--mcp-platforms ${opts.mcpPlatforms.join(',')}`)
  for (const name of opts.globalExcludes) args.push(`--mcp-exclude ${name}`)
  // 同平台的多个服务器合并到同一参数（逗号分隔），等价于多次指定同一平台
  for (const [platform, names] of Object.entries(opts.perPlatformExcludes)) {
    if (names.length) args.push(`--mcp-exclude-for ${platform}=${names.join(',')}`)
  }
  if (opts.dryRun) args.push('--dry-run')
  if (opts.source !== DEFAULT_SOURCE) args.push(`--source ${opts.source}`)
  if (opts.verbose) args.push('--verbose')
  return args
}

/** 命令预览文本：unifyai + 参数 */
export function buildCommand(opts: Parameters<typeof buildArgs>[0]): string {
  return ['unifyai', ...buildArgs(opts)].join(' ')
}

export const DEFAULT_SOURCE = '~/.opencodex/config.json'

/**
 * 执行同步（文档 §6.1 完整执行序列）。当前返回模拟进度回调，
 * TODO(backend): 接入真实执行器后，改为逐行 push 真实日志。
 */
export async function runSync(): Promise<void> {
  console.warn(NOT_IMPLEMENTED)
}

/**
 * 强制刷新 OpenRouter 模型元数据缓存（--update-metadata，文档 §5.4）。
 * TODO(backend): 替换为真实刷新调用。
 */
export async function updateMetadata(): Promise<void> {
  console.warn(NOT_IMPLEMENTED)
}
