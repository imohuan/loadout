import { computed, onMounted, reactive, ref } from 'vue'
import { api, request } from '@/lib/api'
import type { ApiKey } from '@/lib/types'
import { useAsyncTask } from './useAsyncTask'
import { useConfirm } from './useConfirm'
import { toast } from 'vue-sonner'
import { getLoadoutBase } from '@/lib/base'

export interface McpServer {
  id: string
  name: string
  description?: string
  transport: string
  command?: string
  args?: string[]
  url?: string
  headers?: Record<string, string>
  env?: Record<string, string>
  enabled?: boolean
  // 是否为内置端点注册的 server（如多模态），前端显示「内置」标签。
  builtin?: boolean
  // 进程运行状态（后端 /api/mcp-servers 返回）：running / stopped / failed。
  status?: 'running' | 'stopped' | 'failed'
  // 失败原因（status=failed 时）。
  error?: string
}
// "实际激活态" = 配置启用 且 进程没挂。status=failed 时即便 enabled=true，
// 开关也要呈现关闭态，与状态列的「失败」Badge 保持一致；且点击=重试启动而非禁用。
export function isServerActive(server: McpServer): boolean {
  return server.enabled !== false && server.status !== 'failed'
}
export interface McpToolGroup {
  id: string
  name: string
  transport: string
  tools: Array<{ name: string; description?: string }>
  error?: string
}
export interface McpGroup {
  name: string
  tools: Array<{ server_id: string; tool_name: string }>
}
export interface McpEndpoint {
  path: string
  kind: '单 MCP' | '分组' | '聚合'
  label: string
  transport: string
  count: number
  // 是否为内置端点（如多模态），前端显示「内置」标签。
  builtin?: boolean
}

export function useMcpManagement() {
  const servers = ref<McpServer[]>([])
  const groups = ref<McpGroup[]>([])
  const tools = ref<McpToolGroup[]>([])
  const mcpKeys = ref<ApiKey[]>([])
  const form = reactive({
    name: '',
    description: '',
    transport: 'http',
    command: '',
    args: '',
    url: '',
    enabled: true,
  })
  const headers = ref<Array<{ key: string; value: string }>>([])
  const env = ref<Array<{ key: string; value: string }>>([])
  const groupName = ref('')
  const selectedTools = ref<Array<{ server_id: string; tool_name: string }>>([])
  const editingId = ref<string | null>(null)
  const editingGroup = ref<string | null>(null)
  const { pending, run, isPending } = useAsyncTask()
  const { confirmDialog } = useConfirm()
  // `initialLoading` only flips to false after the first refresh completes,
  // so a "loading skeleton" can be shown on cold start without flashing the
  // empty state during subsequent in-place refreshes.
  const initialLoading = ref(true)
  // `refreshing` reflects any in-flight refresh and is used to drive button
  // busy states (refresh / save / toggle / delete) without affecting content.
  const refreshing = ref(false)
  const toObject = (rows: Array<{ key: string; value: string }>) =>
    Object.fromEntries(
      rows.filter((row) => row.key.trim()).map((row) => [row.key.trim(), row.value]),
    )
  const fromObject = (value?: Record<string, string>) =>
    Object.entries(value || {}).map(([key, value]) => ({ key, value }))
  const payload = () => ({
    name: form.name.trim(),
    description: form.description.trim(),
    transport: form.transport,
    command: form.command.trim(),
    args: form.args.split(/\s+/).filter(Boolean),
    url: form.url.trim(),
    headers: toObject(headers.value),
    env: toObject(env.value),
    enabled: form.enabled,
  })
  const endpoints = computed<McpEndpoint[]>(() => {
    const result: McpEndpoint[] = servers.value
      .filter((server) => server.enabled !== false)
      .map((server) => ({
        path: '/mcp/' + server.name,
        kind: '单 MCP',
        label: server.name,
        transport: server.transport || 'http',
        count: tools.value.find((item) => item.id === server.id)?.tools.length || 0,
        builtin: server.builtin,
      }))
    result.push(
      ...groups.value.map((group) => ({
        path: '/mcp/' + group.name,
        kind: '分组' as const,
        label: group.name,
        transport: 'http',
        count: group.tools?.length || 0,
      })),
    )
    result.push({
      path: '/mcp/$smart',
      kind: '聚合' as const,
      label: '$smart',
      transport: 'http',
      count: tools.value.reduce((total, item) => total + item.tools.length, 0),
    })
    return result
  })
  // Update a single ref in place to keep Vue's v-for keys stable and avoid
  // re-mounting rows on refresh. This is the main lever for "lossless refresh".
  // `?? []` guards against backends that encode empty tables as JSON `null`
  // (Go nil slices), which would otherwise blow up `.length` in templates.
  function assignList<T>(target: { value: T[] }, next: T[]) {
    target.value = next ?? []
  }
  async function refreshKeys() {
    try {
      const result = await api<{ mcp_keys: ApiKey[] }>('/api/keys')
      mcpKeys.value = result.mcp_keys || []
    } catch {
      mcpKeys.value = []
    }
  }
  async function refresh() {
    refreshing.value = true
    try {
      const [serverList, groupList, toolList] = await Promise.all([
        api<McpServer[]>('/api/mcp-servers'),
        api<McpGroup[]>('/api/groups'),
        api<McpToolGroup[]>('/api/mcp-tools').catch(() => []),
      ])
      assignList(servers, serverList)
      assignList(groups, groupList)
      assignList(tools, toolList)
      await refreshKeys()
    } finally {
      refreshing.value = false
      initialLoading.value = false
    }
  }
  function endpointHasKey(path: string) {
    return mcpKeys.value.some((key) => key.endpoint === path)
  }
  function resetServer() {
    editingId.value = null
    Object.assign(form, {
      name: '',
      description: '',
      transport: 'http',
      command: '',
      args: '',
      url: '',
      enabled: true,
    })
    headers.value = []
    env.value = []
  }
  function editServer(server: McpServer) {
    editingId.value = server.id
    Object.assign(form, {
      name: server.name,
      description: server.description || '',
      transport: server.transport || 'http',
      command: server.command || '',
      args: (server.args || []).join(' '),
      url: server.url || '',
      enabled: server.enabled !== false,
    })
    headers.value = fromObject(server.headers)
    env.value = fromObject(server.env)
  }
  function resetGroup() {
    editingGroup.value = null
    groupName.value = ''
    selectedTools.value = []
  }
  function editGroup(group: McpGroup) {
    editingGroup.value = group.name
    groupName.value = group.name
    selectedTools.value = [...(group.tools || [])]
  }
  function toggleTool(serverId: string, toolName: string) {
    const index = selectedTools.value.findIndex(
      (item) => item.server_id === serverId && item.tool_name === toolName,
    )
    index >= 0
      ? selectedTools.value.splice(index, 1)
      : selectedTools.value.push({ server_id: serverId, tool_name: toolName })
  }
  function selected(serverId: string, toolName: string) {
    return selectedTools.value.some(
      (item) => item.server_id === serverId && item.tool_name === toolName,
    )
  }
  async function testServer(
    serverPayload: McpServer | ReturnType<typeof payload> = payload(),
    name = form.name,
  ) {
    let result: {
      name: string
      ok: boolean
      error?: string
      tools?: Array<{ name: string; description?: string }>
    }
    await run(
      `server:${name}:test`,
      async () => {
        try {
          result = {
            name,
            ...((await api('/api/mcp-servers/test', {
              method: 'POST',
              body: JSON.stringify(serverPayload),
            })) as any),
          }
        } catch (error) {
          result = {
            name,
            ok: false,
            error: error instanceof Error ? error.message : String(error),
          }
        }
      },
      '',
    )
    const description =
      result!.error || `${result!.name}：发现 ${(result!.tools || []).length} 个工具`
    if (result!.ok) toast.success('MCP 连接成功', { description })
    else toast.error('MCP 连接失败', { description })
    return result!
  }
  async function saveServer() {
    await run(
      'save-server',
      async () => {
        const data = payload()
        if (!data.name) throw new Error('名称必填')
        if ((data.transport === 'http' || data.transport === 'sse') && !data.url)
          throw new Error('URL 必填')
        if (data.transport === 'stdio' && !data.command) throw new Error('命令必填')
        if (editingId.value) await request('/api/mcp-servers/' + editingId.value, 'PUT', data)
        else await request('/api/mcp-servers', 'POST', data)
        await refresh()
        await testServer(data, data.name)
      },
      'MCP 服务器已保存',
    )
  }
  async function toggleServer(server: McpServer) {
    // 目标状态 = 取反"实际激活态"，而非翻转 enabled 配置位。
    // failed 时 isServerActive=false → 目标 enabled=true → 点击=重试启动（不是禁用）。
    const nextEnabled = !isServerActive(server)
    // 本地乐观更新：在接口返回前就把该行状态改到位，避免 refresh 慢/失败时
    // UI 停留在旧状态（"已关闭"却显示"启用"之类的不一致）。
    // 关键：用 id 定位行，绝不用索引——刷新/排序后索引会变，会误改到别的配置。
    const idx = servers.value.findIndex((it) => it.id === server.id)
    if (idx >= 0) {
      servers.value[idx] = {
        ...servers.value[idx],
        enabled: nextEnabled,
        // 关闭后进程尚未确认杀死前，不要立刻显示 running/failed，等 refresh 兜底。
        status: nextEnabled ? undefined : 'stopped',
      }
    }
    await run(
      `server:${server.id}:toggle`,
      async () => {
        await request('/api/mcp-servers/' + server.id, 'PUT', {
          ...server,
          enabled: nextEnabled,
        })
        await refresh()
        // 开启后进程没起来（stdio 启动失败/崩溃）→ 明确报错，状态列同步显示「失败」。
        if (nextEnabled) {
          const updated = servers.value.find((it) => it.id === server.id)
          if (updated?.status === 'failed') {
            toast.error('MCP 进程启动失败', {
              description: updated.error || '进程未存活，具体原因见状态列',
            })
            return
          }
        }
        toast.success(nextEnabled ? '已启动' : '已停止')
      },
      '',
    )
  }
  async function removeServer(server: McpServer) {
    if (!(await confirmDialog('删除 MCP「' + server.name + '」？'))) return
    await run(
      `server:${server.id}:remove`,
      async () => {
        await api('/api/mcp-servers', { method: 'DELETE', body: JSON.stringify({ id: server.id }) })
        await refresh()
      },
      'MCP 服务器已删除',
    )
  }
  async function saveGroup() {
    await run(
      'save-group',
      async () => {
        if (!groupName.value.trim()) throw new Error('分组名必填')
        await request('/api/groups', 'PUT', [
          ...groups.value.filter(
            (group) => group.name !== editingGroup.value && group.name !== groupName.value,
          ),
          { name: groupName.value.trim(), tools: selectedTools.value },
        ])
        await refresh()
        resetGroup()
      },
      '分组已保存',
    )
  }
  async function removeGroup(group: McpGroup) {
    if (!(await confirmDialog('删除分组「' + group.name + '」？'))) return
    await run(
      `group:${group.name}:remove`,
      async () => {
        await api('/api/groups', { method: 'DELETE', body: JSON.stringify({ name: group.name }) })
        await refresh()
      },
      '分组已删除',
    )
  }
  async function copy(path: string) {
    await navigator.clipboard.writeText(await getLoadoutBase() + path)
  }
  onMounted(refresh)
  return {
    servers,
    groups,
    tools,
    mcpKeys,
    form,
    headers,
    env,
    groupName,
    selectedTools,
    editingId,
    editingGroup,
    endpoints,
    initialLoading,
    refreshing,
    pending,
    isPending,
    refresh,
    refreshKeys,
    resetServer,
    editServer,
    resetGroup,
    editGroup,
    toggleTool,
    selected,
    testServer,
    saveServer,
    toggleServer,
    removeServer,
    saveGroup,
    removeGroup,
    copy,
    endpointHasKey,
  }
}
