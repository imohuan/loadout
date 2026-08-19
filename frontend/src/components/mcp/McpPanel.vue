<script setup lang="ts">
import { reactive, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiCheckLine,
  RiCheckboxBlankLine,
  RiCheckboxLine,
  RiClipboardLine,
  RiDeleteBinLine,
  RiEditLine,
  RiFileCopyLine,
  RiKey2Line,
  RiRefreshLine,
  RiTestTubeLine,
} from '@remixicon/vue'
import PageHeader from '@/components/PageHeader.vue'
import EmptyState from '@/components/EmptyState.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import { useMcpManagement } from '@/composables/useMcpManagement'
import { useManagementApi } from '@/composables/useManagementApi'
import { useAsyncTask } from '@/composables/useAsyncTask'
const mcp = reactive(useMcpManagement())
const api = useManagementApi()
const { pending: keyPending, run: runKey } = useAsyncTask()
const labels: Record<string, string> = { http: 'Streamable HTTP', sse: 'SSE', stdio: 'stdio' }
const activeTab = ref('upstream')
const serverDialog = ref(false)
const groupDialog = ref(false)
const expandedEndpoints = ref<string[]>([])
const endpointKeys = ref<Record<string, string>>({})
const expandedServers = ref<string[]>([])
const testDialog = ref(false)
const editToolDialog = ref(false)
const activeTool = ref<{
  serverId: string
  serverName: string
  name: string
  description?: string
} | null>(null)
const toolSchema = ref<Record<string, any> | null>(null)
const toolArgs = reactive<Record<string, any>>({})
const toolResult = ref<any>(null)
const toolError = ref('')
const toolLoading = ref(false)
const toolExecuting = ref(false)
const toolEditName = ref('')
const toolEditDescription = ref('')
const expandedGroupServers = ref<string[]>([])
const add = (rows: Array<{ key: string; value: string }>) => rows.push({ key: '', value: '' })
const remove = (rows: Array<{ key: string; value: string }>, index: number) => rows.splice(index, 1)
function openServerDialog(server?: (typeof mcp.servers)[number]) {
  if (server) mcp.editServer(server)
  else mcp.resetServer()
  serverDialog.value = true
}
function openGroupDialog(group?: (typeof mcp.groups)[number]) {
  if (group) {
    mcp.editGroup(group)
  } else {
    mcp.resetGroup()
  }
  expandedGroupServers.value = []
  if (!mcp.tools.length) return
  const initial = mcp.tools[0]
  if (initial) expandedGroupServers.value.push(initial.id)
  if (group) {
    const ids = new Set(mcp.tools.map((entry) => entry.id))
    for (const item of group.tools || [])
      if (ids.has(item.server_id)) expandedGroupServers.value.push(item.server_id)
  }
  groupDialog.value = true
}
async function saveServer() {
  await mcp.saveServer()
  if (!mcp.pending) serverDialog.value = false
}
async function saveGroup() {
  await mcp.saveGroup()
  if (!mcp.pending) groupDialog.value = false
}
async function testServerInDialog() {
  await mcp.testServer()
}
function toggleEndpoint(path: string) {
  const index = expandedEndpoints.value.indexOf(path)
  if (index >= 0) expandedEndpoints.value.splice(index, 1)
  else expandedEndpoints.value.push(path)
}
function isEndpointExpanded(path: string) {
  return expandedEndpoints.value.includes(path)
}
function toggleServer(serverId: string) {
  const index = expandedServers.value.indexOf(serverId)
  if (index >= 0) expandedServers.value.splice(index, 1)
  else expandedServers.value.push(serverId)
}
function isServerExpanded(serverId: string) {
  return expandedServers.value.includes(serverId)
}
function isGroupServerExpanded(serverId: string) {
  return expandedGroupServers.value.includes(serverId)
}
function toggleGroupServer(serverId: string) {
  const index = expandedGroupServers.value.indexOf(serverId)
  if (index >= 0) expandedGroupServers.value.splice(index, 1)
  else expandedGroupServers.value.push(serverId)
}
function serverTools(serverId: string) {
  return mcp.tools.find((entry) => entry.id === serverId)?.tools || []
}
function selectedCountForServer(serverId: string) {
  return mcp.selectedTools.filter((item) => item.server_id === serverId).length
}
function isAllServerToolsSelected(serverId: string) {
  const tools = serverTools(serverId)
  if (!tools.length) return false
  return tools.every((tool) => mcp.selected(serverId, tool.name))
}
function toggleAllServerTools(serverId: string) {
  const tools = serverTools(serverId)
  if (!tools.length) return
  if (isAllServerToolsSelected(serverId)) {
    for (const tool of tools) {
      if (mcp.selected(serverId, tool.name)) mcp.toggleTool(serverId, tool.name)
    }
  } else {
    for (const tool of tools) {
      if (!mcp.selected(serverId, tool.name)) mcp.toggleTool(serverId, tool.name)
    }
  }
}
function schemaProperties() {
  return Object.entries((toolSchema.value?.properties || {}) as Record<string, any>)
}
function schemaRequired() {
  return new Set<string>((toolSchema.value?.required || []) as string[])
}
function schemaOptions(schema: Record<string, any>) {
  return Array.isArray(schema.enum)
    ? schema.enum
    : (schema.oneOf || [])
        .map((item: Record<string, any>) => item.const)
        .filter((value: unknown) => value !== undefined)
}
function fieldPlaceholder(schema: Record<string, any>) {
  const example = schema.examples?.[0] ?? schema.example
  return example === undefined ? schema.placeholder || '' : String(example)
}
function fieldUsesTextarea(schema: Record<string, any>) {
  return (
    schema.type === 'array' ||
    schema.type === 'object' ||
    schema.format === 'textarea' ||
    schema.format === 'multiline'
  )
}
function defaultToolValue(schema: Record<string, any>) {
  if (schema.default !== undefined)
    return fieldUsesTextarea(schema) && typeof schema.default === 'object'
      ? JSON.stringify(schema.default, null, 2)
      : schema.default
  if (schema.type === 'boolean') return false
  return ''
}
function resetToolArgs() {
  Object.keys(toolArgs).forEach((key) => delete toolArgs[key])
  for (const [name, schema] of schemaProperties()) toolArgs[name] = defaultToolValue(schema)
}
async function openToolTest(
  server: { id: string; name: string },
  tool: { name: string; description?: string },
) {
  activeTool.value = {
    serverId: server.id,
    serverName: server.name,
    name: tool.name,
    description: tool.description,
  }
  testDialog.value = true
  toolSchema.value = null
  toolResult.value = null
  toolError.value = ''
  toolLoading.value = true
  try {
    const result = await api.mcpToolSchema(server.id, tool.name)
    toolSchema.value = result.inputSchema || { type: 'object', properties: {} }
    resetToolArgs()
  } catch (error) {
    toolError.value = error instanceof Error ? error.message : String(error)
  } finally {
    toolLoading.value = false
  }
}
async function executeTool() {
  if (!activeTool.value) return
  toolExecuting.value = true
  toolError.value = ''
  toolResult.value = null
  try {
    const args: Record<string, any> = {}
    for (const [name, schema] of schemaProperties()) {
      const value = toolArgs[name]
      if (schema.type === 'number' || schema.type === 'integer')
        args[name] = value === '' ? undefined : Number(value)
      else if (schema.type === 'array' || schema.type === 'object') {
        if (value === '' || value === undefined) args[name] = schema.type === 'array' ? [] : {}
        else args[name] = JSON.parse(value)
      } else args[name] = value
    }
    toolResult.value = await api.callMcpTool({
      server_id: activeTool.value.serverId,
      tool_name: activeTool.value.name,
      arguments: args,
    })
  } catch (error) {
    toolError.value = error instanceof Error ? error.message : String(error)
  } finally {
    toolExecuting.value = false
  }
}
function openToolEdit(server: { name: string }, tool: { name: string; description?: string }) {
  activeTool.value = {
    serverId: '',
    serverName: server.name,
    name: tool.name,
    description: tool.description,
  }
  toolEditName.value = tool.name
  toolEditDescription.value = tool.description || ''
  editToolDialog.value = true
}
function setToolBoolean(name: string, value: boolean) {
  toolArgs[name] = value
}
function endpointUrl(path: string) {
  return `${window.location.origin}${path}`
}
async function createMcpKey(endpoint: string) {
  await runKey(async () => {
    const result = await api.createMcpKey(endpoint)
    endpointKeys.value = { ...endpointKeys.value, [endpoint]: result.key }
    if (!isEndpointExpanded(endpoint)) expandedEndpoints.value.push(endpoint)
    // Only refresh the key list; the rest of the table is untouched so the
    // "已配置/无认证" badge flips without re-rendering the whole list.
    await mcp.refreshKeys()
  }, 'MCP 端点密钥已签发')
}
async function deleteMcpKey(endpoint: string) {
  await runKey(async () => {
    await api.deleteMcpKey(endpoint)
    const index = expandedEndpoints.value.indexOf(endpoint)
    if (index >= 0) expandedEndpoints.value.splice(index, 1)
    if (endpointKeys.value[endpoint]) {
      const next = { ...endpointKeys.value }
      delete next[endpoint]
      endpointKeys.value = next
    }
    await mcp.refreshKeys()
  }, '端点密钥已删除')
}
async function copyKey(value: string) {
  await navigator.clipboard.writeText(value)
}
async function copyConfig(endpoint: {
  path: string
  kind: string
  label: string
  transport: string
}) {
  const url = endpointUrl(endpoint.path)
  const serverConfig: Record<string, any> = {}
  if (endpoint.transport === 'stdio') {
    // stdio endpoints are proxied as a local command by the host runtime.
    serverConfig.command = 'loadout'
    serverConfig.args = ['mcp', 'serve', endpoint.label]
  } else {
    serverConfig.url = url
  }
  if (mcp.endpointHasKey(endpoint.path)) {
    serverConfig.headers = { 'X-Loadout-Key': '<YOUR_MCP_KEY>' }
  }
  const config = {
    mcpServers: {
      [endpoint.label]: serverConfig,
    },
  }
  await navigator.clipboard.writeText(JSON.stringify(config, null, 2))
  toast.success('配置已复制', {
    description: '键名是 mcpServers（Claude Desktop / Cursor / VSCode 等通用格式）',
  })
}
</script>
<template>
  <div class="space-y-6">
    <PageHeader title="MCP 管理" description="配置上游 MCP、工具分组与可复制的连接端点"
      ><template #actions
        ><Button variant="outline" :disabled="mcp.refreshing" @click="mcp.refresh">
          <RiRefreshLine size="16" :class="mcp.refreshing ? 'animate-spin' : ''" />刷新 </Button
        ><Button v-if="activeTab === 'upstream'" @click="openServerDialog()">
          <RiAddLine size="16" />添加上游 MCP </Button
        ><Button v-else-if="activeTab === 'groups'" @click="openGroupDialog()">
          <RiAddLine size="16" />添加分组 MCP
        </Button></template
      >
    </PageHeader>
    <LoadingBlock v-if="mcp.initialLoading" />
    <Tabs v-else v-model="activeTab" class="space-y-4">
      <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
        <TabsTrigger value="upstream">上游 MCP</TabsTrigger>
        <TabsTrigger value="groups">分组 MCP</TabsTrigger>
        <TabsTrigger value="endpoints">连接端点配置</TabsTrigger>
      </TabsList>
      <TabsContent value="upstream" class="space-y-4">
        <Card class="rounded-md">
          <CardHeader>
            <CardTitle class="text-base">上游服务器</CardTitle>
            <CardDescription>维护 HTTP、SSE 或 stdio 服务，并测试连接状态。</CardDescription>
          </CardHeader>
          <CardContent class="p-0">
            <div v-if="mcp.servers.length" class="w-full overflow-hidden">
              <Table class="table-fixed w-full">
                <colgroup>
                  <col class="w-10" />
                  <col />
                  <col class="" />
                  <col class="w-[100px]" />
                  <col class="w-[180px]" />
                </colgroup>
                <TableHeader>
                  <TableRow>
                    <TableHead></TableHead>
                    <TableHead>名称</TableHead>
                    <TableHead>传输</TableHead>
                    <TableHead>状态</TableHead>
                    <TableHead class="text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <template v-for="server in mcp.servers" :key="server.id">
                    <TableRow>
                      <TableCell class="px-0"
                        ><Button
                          variant="ghost"
                          size="icon"
                          class="size-8"
                          :aria-label="isServerExpanded(server.id) ? '收起工具' : '展开工具'"
                          :aria-expanded="isServerExpanded(server.id)"
                          @click="toggleServer(server.id)"
                        >
                          <RiArrowDownSLine v-if="isServerExpanded(server.id)" size="16" />
                          <RiArrowRightSLine v-else size="16" /> </Button
                      ></TableCell>
                      <TableCell>
                        <div class="font-medium">{{ server.name }}</div>
                        <div class="w-full truncate text-xs text-muted-foreground">
                          {{ server.url || server.command }}
                        </div>
                      </TableCell>
                      <TableCell>{{ labels[server.transport] || server.transport }}</TableCell>
                      <TableCell>
                        <Badge :variant="server.enabled === false ? 'secondary' : 'default'">{{
                          server.enabled === false ? '禁用' : '启用'
                        }}</Badge>
                      </TableCell>
                      <TableCell>
                        <div class="flex justify-end gap-1">
                          <TooltipProvider>
                            <Tooltip>
                              <TooltipTrigger as-child
                                ><Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="测试"
                                  @click="mcp.testServer(server, server.name)"
                                >
                                  <RiTestTubeLine size="16" /> </Button
                              ></TooltipTrigger>
                              <TooltipContent>测试连接</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger as-child
                                ><Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="编辑"
                                  @click="openServerDialog(server)"
                                >
                                  <RiEditLine size="16" /> </Button
                              ></TooltipTrigger>
                              <TooltipContent>编辑</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger as-child
                                ><Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="切换状态"
                                  @click="mcp.toggleServer(server)"
                                >
                                  <RiRefreshLine size="16" /> </Button
                              ></TooltipTrigger>
                              <TooltipContent>切换状态</TooltipContent>
                            </Tooltip>
                            <Tooltip>
                              <TooltipTrigger as-child
                                ><Button
                                  variant="ghost"
                                  size="icon"
                                  aria-label="删除"
                                  @click="mcp.removeServer(server)"
                                >
                                  <RiDeleteBinLine size="16" /> </Button
                              ></TooltipTrigger>
                              <TooltipContent>删除</TooltipContent>
                            </Tooltip>
                          </TooltipProvider>
                        </div>
                      </TableCell>
                    </TableRow>
                    <TableRow
                      v-if="isServerExpanded(server.id)"
                      class="bg-muted/30 hover:bg-muted/30"
                    >
                      <TableCell :colspan="5" class="whitespace-normal p-0 w-full overflow-hidden">
                        <div class="space-y-3 px-4 py-4">
                          <div class="flex items-center justify-between gap-3">
                            <div class="min-w-0">
                              <div class="font-medium">工具列表</div>
                              <div class="text-xs text-muted-foreground">
                                显示该上游服务器提供的工具名称和描述。
                              </div>
                            </div>
                            <Badge variant="outline" class="shrink-0"
                              >{{ serverTools(server.id).length }} 个工具</Badge
                            >
                          </div>
                          <div
                            v-if="serverTools(server.id).length"
                            class="divide-y rounded-md border bg-background"
                          >
                            <div
                              v-for="tool in serverTools(server.id)"
                              :key="tool.name"
                              class="flex flex-col gap-3 px-3 py-3 sm:flex-row sm:items-center sm:justify-between"
                            >
                              <div class="min-w-0 flex-1 overflow-hidden">
                                <div class="truncate font-mono text-sm">{{ tool.name }}</div>
                                <div
                                  class="mt-1 line-clamp-2 break-words text-xs leading-5 text-muted-foreground"
                                >
                                  {{ tool.description || '暂无描述' }}
                                </div>
                              </div>
                              <div class="flex shrink-0 gap-2">
                                <Button
                                  variant="outline"
                                  size="sm"
                                  @click="openToolTest(server, tool)"
                                >
                                  <RiTestTubeLine size="16" />测试 </Button
                                ><Button
                                  variant="ghost"
                                  size="sm"
                                  @click="openToolEdit(server, tool)"
                                >
                                  <RiEditLine size="16" />编辑
                                </Button>
                              </div>
                            </div>
                          </div>
                          <EmptyState
                            v-else
                            title="没有工具"
                            description="该服务器当前没有返回可用工具。"
                          />
                        </div>
                      </TableCell>
                    </TableRow>
                  </template>
                </TableBody>
              </Table>
            </div>
            <EmptyState
              v-else
              title="没有上游 MCP"
              description="添加一个 HTTP、SSE 或 stdio 服务开始使用。"
            />
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="groups" class="space-y-4">
        <Card class="rounded-md">
          <CardHeader>
            <CardTitle class="text-base">分组 MCP</CardTitle>
            <CardDescription>从已连接的上游服务中挑选工具，生成独立端点。</CardDescription>
          </CardHeader>
          <CardContent class="p-0">
            <div v-if="mcp.groups.length" class="overflow-x-auto">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>分组</TableHead>
                    <TableHead>工具数</TableHead>
                    <TableHead class="text-right">操作</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  <TableRow v-for="group in mcp.groups" :key="group.name">
                    <TableCell class="font-medium">{{ group.name }}</TableCell>
                    <TableCell>{{ group.tools?.length || 0 }}</TableCell>
                    <TableCell class="text-right">
                      <div class="flex justify-end gap-1">
                        <Button variant="ghost" size="sm" @click="openGroupDialog(group)">
                          <RiEditLine size="16" />编辑
                        </Button>
                        <TooltipProvider>
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="删除分组"
                                @click="mcp.removeGroup(group)"
                              >
                                <RiDeleteBinLine size="16" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>删除分组</TooltipContent>
                          </Tooltip>
                        </TooltipProvider>
                      </div>
                    </TableCell>
                  </TableRow>
                </TableBody>
              </Table>
            </div>
            <EmptyState v-else title="没有分组 MCP" description="添加一个分组来组合上游工具。" />
          </CardContent>
        </Card>
      </TabsContent>
      <TabsContent value="endpoints" class="space-y-4">
        <Card class="rounded-md">
          <CardHeader>
            <CardTitle class="text-base">连接端点</CardTitle>
            <CardDescription
              >复制地址给兼容 MCP 的客户端，并在对应端点配置认证密钥。</CardDescription
            >
          </CardHeader>
          <CardContent class="p-0">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead class="w-10 min-w-10 max-w-10"></TableHead>
                  <TableHead>类型</TableHead>
                  <TableHead>地址</TableHead>
                  <TableHead>工具数</TableHead>
                  <TableHead>认证</TableHead>
                  <TableHead class="text-right">操作</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                <template v-for="endpoint in mcp.endpoints" :key="endpoint.path">
                  <TableRow>
                    <TableCell class="w-10 pr-0">
                      <Button
                        variant="ghost"
                        size="icon"
                        class="size-8"
                        :aria-label="
                          isEndpointExpanded(endpoint.path) ? '收起端点详情' : '展开端点详情'
                        "
                        :aria-expanded="isEndpointExpanded(endpoint.path)"
                        @click="toggleEndpoint(endpoint.path)"
                      >
                        <RiArrowDownSLine v-if="isEndpointExpanded(endpoint.path)" size="16" />
                        <RiArrowRightSLine v-else size="16" />
                      </Button>
                    </TableCell>
                    <TableCell>
                      <Badge variant="secondary">{{ endpoint.kind }}</Badge>
                    </TableCell>
                    <TableCell class="font-mono text-xs">{{ endpoint.path }}</TableCell>
                    <TableCell>{{ endpoint.count }}</TableCell>
                    <TableCell>
                      <Badge :variant="mcp.endpointHasKey(endpoint.path) ? 'default' : 'outline'">
                        <RiKey2Line
                          v-if="mcp.endpointHasKey(endpoint.path)"
                          size="12"
                          class="mr-1"
                        />
                        {{ mcp.endpointHasKey(endpoint.path) ? '已配置' : '无认证' }}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div class="flex justify-end gap-2">
                        <Button variant="outline" size="sm" @click="mcp.copy(endpoint.path)">
                          <RiClipboardLine size="16" />复制地址
                        </Button>
                        <Button variant="outline" size="sm" @click="copyConfig(endpoint)">
                          <RiFileCopyLine size="16" />复制配置
                        </Button>
                        <Button
                          v-if="mcp.endpointHasKey(endpoint.path)"
                          variant="destructive"
                          size="sm"
                          :disabled="keyPending"
                          @click="deleteMcpKey(endpoint.path)"
                        >
                          <RiDeleteBinLine size="16" />删除认证
                        </Button>
                        <Button
                          v-else
                          variant="secondary"
                          size="sm"
                          :disabled="keyPending"
                          @click="createMcpKey(endpoint.path)"
                        >
                          <RiKey2Line size="16" />创建端点密钥
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                  <TableRow
                    v-if="isEndpointExpanded(endpoint.path)"
                    class="bg-muted/30 hover:bg-muted/30"
                  >
                    <TableCell :colspan="6" class="p-0">
                      <div class="grid gap-4 px-4 py-4 md:grid-cols-2">
                        <div class="space-y-3">
                          <div>
                            <div class="text-xs font-medium text-muted-foreground">端点地址</div>
                            <code
                              class="mt-1 block break-all rounded bg-background px-2 py-1 font-mono text-xs"
                              >{{ endpointUrl(endpoint.path) }}</code
                            >
                          </div>
                          <div>
                            <div class="text-xs font-medium text-muted-foreground">认证 Header</div>
                            <code
                              class="mt-1 block rounded bg-background px-2 py-1 font-mono text-xs"
                              >X-Loadout-Key</code
                            >
                          </div>
                          <div>
                            <div class="text-xs font-medium text-muted-foreground">认证状态</div>
                            <Badge
                              :variant="mcp.endpointHasKey(endpoint.path) ? 'default' : 'secondary'"
                              >{{
                                mcp.endpointHasKey(endpoint.path) ? '已配置密钥' : '未配置'
                              }}</Badge
                            >
                          </div>
                        </div>
                        <div class="space-y-3">
                          <div class="flex items-center justify-between">
                            <div>
                              <div class="font-medium">Token 列表</div>
                              <div class="text-xs text-muted-foreground">
                                后续接入后端 token 历史记录。
                              </div>
                            </div>
                            <Badge variant="outline">{{
                              endpointKeys[endpoint.path] ? '1 条临时记录' : '暂无记录'
                            }}</Badge>
                          </div>
                          <div
                            v-if="endpointKeys[endpoint.path]"
                            class="rounded-md border bg-background p-3"
                          >
                            <div class="mb-2 flex items-center justify-between gap-2">
                              <span class="text-xs text-muted-foreground"
                                >刚刚创建的 Token（仅本次可见）</span
                              >
                              <TooltipProvider>
                                <Tooltip>
                                  <TooltipTrigger as-child
                                    ><Button
                                      variant="ghost"
                                      size="icon"
                                      class="size-8"
                                      aria-label="复制 Token"
                                      @click="copyKey(endpointKeys[endpoint.path])"
                                    >
                                      <RiClipboardLine size="16" /> </Button
                                  ></TooltipTrigger>
                                  <TooltipContent>复制 Token</TooltipContent>
                                </Tooltip>
                              </TooltipProvider>
                            </div>
                            <code class="block break-all font-mono text-xs">{{
                              endpointKeys[endpoint.path]
                            }}</code>
                          </div>
                          <div
                            v-else
                            class="rounded-md border border-dashed p-3 text-sm text-muted-foreground"
                          >
                            暂无 token 记录
                          </div>
                        </div>
                      </div>
                    </TableCell>
                  </TableRow>
                </template>
              </TableBody>
            </Table>
          </CardContent>
        </Card>
      </TabsContent>
    </Tabs>
    <Dialog v-model:open="serverDialog">
      <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-2xl!">
        <DialogHeader>
          <DialogTitle>{{ mcp.editingId ? '编辑上游 MCP' : '添加上游 MCP' }}</DialogTitle>
          <DialogDescription>配置连接方式和认证信息。</DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div class="grid gap-3 sm:grid-cols-3">
            <div class="space-y-1 sm:col-span-2">
              <Label>名称</Label><Input v-model="mcp.form.name" placeholder="github" />
            </div>
            <div class="space-y-1">
              <Label>传输</Label
              ><Select v-model="mcp.form.transport">
                <SelectTrigger>
                  <SelectValue placeholder="选择传输方式" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem value="http">Streamable HTTP</SelectItem>
                    <SelectItem value="sse">SSE</SelectItem>
                    <SelectItem value="stdio">stdio</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </div>
          </div>
          <div class="space-y-1">
            <Label>描述</Label>
            <Textarea
              v-model="mcp.form.description"
              rows="2"
              placeholder="描述这个 MCP 的作用，将作为工具分类描述展示"
            />
          </div>
          <div v-if="mcp.form.transport !== 'stdio'" class="space-y-3">
            <div class="space-y-1">
              <Label>URL</Label
              ><Input v-model="mcp.form.url" placeholder="http://127.0.0.1:8000/mcp" />
            </div>
            <div class="space-y-2">
              <Label>Headers</Label>
              <div v-for="(row, index) in mcp.headers" :key="index" class="flex gap-2">
                <Input v-model="row.key" placeholder="名称" /><Input
                  v-model="row.value"
                  placeholder="值"
                />
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger as-child
                      ><Button
                        variant="ghost"
                        size="icon"
                        aria-label="删除 Header"
                        @click="remove(mcp.headers, index)"
                      >
                        <RiDeleteBinLine size="16" /> </Button
                    ></TooltipTrigger>
                    <TooltipContent>删除 Header</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <Button variant="outline" size="sm" @click="add(mcp.headers)">
                <RiAddLine size="16" />添加 Header
              </Button>
            </div>
          </div>
          <div v-else class="space-y-3">
            <div class="grid gap-3 sm:grid-cols-2">
              <div class="space-y-1">
                <Label>命令</Label><Input v-model="mcp.form.command" placeholder="npx" />
              </div>
              <div class="space-y-1">
                <Label>参数</Label><Input v-model="mcp.form.args" placeholder="-y server-package" />
              </div>
            </div>
            <div class="space-y-2">
              <Label>环境变量</Label>
              <div v-for="(row, index) in mcp.env" :key="index" class="flex gap-2">
                <Input v-model="row.key" placeholder="KEY" /><Input
                  v-model="row.value"
                  placeholder="值"
                />
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger as-child
                      ><Button
                        variant="ghost"
                        size="icon"
                        aria-label="删除变量"
                        @click="remove(mcp.env, index)"
                      >
                        <RiDeleteBinLine size="16" /> </Button
                    ></TooltipTrigger>
                    <TooltipContent>删除变量</TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
              <Button variant="outline" size="sm" @click="add(mcp.env)">
                <RiAddLine size="16" />添加变量
              </Button>
            </div>
          </div>
        </div>
        <DialogFooter
          ><Button :disabled="mcp.pending" @click="saveServer"> 保存 </Button
          ><Button variant="outline" :disabled="mcp.pending" @click="testServerInDialog">
            测试连接 </Button
          ><Button variant="ghost" @click="serverDialog = false">取消</Button></DialogFooter
        >
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="groupDialog">
      <DialogContent
        class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-4xl!"
        @pointer-down-outside.prevent
        @interact-outside.prevent
        @escape-key-down.prevent
      >
        <DialogHeader>
          <DialogTitle>{{ mcp.editingGroup ? '编辑分组 MCP' : '添加分组 MCP' }}</DialogTitle>
          <DialogDescription>选择要暴露到该分组端点的工具。</DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div class="space-y-1">
            <Label>分组名</Label><Input v-model="mcp.groupName" placeholder="编程向" />
          </div>
          <div v-if="mcp.tools.length" class="space-y-2">
            <div class="flex items-center justify-between">
              <Label>选择上游 MCP 工具</Label>
              <Badge variant="secondary">已选 {{ mcp.selectedTools.length }} 个工具</Badge>
            </div>
            <div class="max-h-[55vh] space-y-2 overflow-y-auto rounded-md border p-2">
              <div
                v-for="entry in mcp.tools"
                :key="entry.id"
                class="rounded-md border bg-background"
              >
                <div class="flex items-center justify-between gap-2 px-2 py-2">
                  <button
                    type="button"
                    class="flex min-w-0 flex-1 items-center gap-2 text-left"
                    :aria-label="isGroupServerExpanded(entry.id) ? '收起工具' : '展开工具'"
                    :aria-expanded="isGroupServerExpanded(entry.id)"
                    @click="toggleGroupServer(entry.id)"
                  >
                    <RiArrowDownSLine
                      v-if="isGroupServerExpanded(entry.id)"
                      size="16"
                      class="shrink-0"
                    />
                    <RiArrowRightSLine v-else size="16" class="shrink-0" />
                    <span class="truncate font-medium">{{ entry.name }}</span>
                    <Badge
                      v-if="selectedCountForServer(entry.id) > 0"
                      variant="default"
                      class="shrink-0"
                    >
                      {{ selectedCountForServer(entry.id) }} / {{ entry.tools.length }}
                    </Badge>
                    <Badge v-else variant="outline" class="shrink-0"
                      >{{ entry.tools.length }} 个工具</Badge
                    >
                  </button>
                  <button
                    type="button"
                    class="text-muted-foreground hover:text-foreground inline-flex shrink-0 items-center gap-1 text-xs"
                    :aria-label="isAllServerToolsSelected(entry.id) ? '取消全选' : '全选工具'"
                    @click="toggleAllServerTools(entry.id)"
                  >
                    <RiCheckboxLine v-if="isAllServerToolsSelected(entry.id)" size="14" />
                    <RiCheckboxBlankLine v-else size="14" />
                    全选
                  </button>
                </div>
                <div v-if="isGroupServerExpanded(entry.id)" class="border-t">
                  <div v-if="entry.tools.length" class="divide-y">
                    <label
                      v-for="tool in entry.tools"
                      :key="tool.name"
                      class="flex cursor-pointer gap-3 px-3 py-3 last:border-b-0 hover:bg-muted/40"
                    >
                      <Checkbox
                        :model-value="mcp.selected(entry.id, tool.name)"
                        @update:model-value="mcp.toggleTool(entry.id, tool.name)"
                      />
                      <span class="min-w-0 flex-1">
                        <span class="block truncate font-mono text-sm">{{ tool.name }}</span>
                        <span
                          class="mt-1 block line-clamp-2 break-words text-xs leading-5 text-muted-foreground"
                          >{{ tool.description || '暂无描述' }}</span
                        >
                      </span>
                    </label>
                  </div>
                  <div v-else class="px-3 py-3 text-xs text-muted-foreground">该上游暂无工具。</div>
                </div>
              </div>
            </div>
          </div>
          <EmptyState v-else title="没有可选工具" description="先添加并启用上游 MCP。" />
        </div>
        <DialogFooter
          ><Button :disabled="mcp.pending" @click="saveGroup">
            <RiCheckLine size="16" />保存分组 </Button
          ><Button variant="outline" @click="groupDialog = false">取消</Button></DialogFooter
        >
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="testDialog">
      <DialogContent class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden sm:max-w-2xl!">
        <DialogHeader>
          <DialogTitle class="truncate">测试工具：{{ activeTool?.name }}</DialogTitle>
          <DialogDescription class="line-clamp-2 break-words">{{
            activeTool?.description || '填写参数后点击执行调用。'
          }}</DialogDescription>
        </DialogHeader>
        <div v-if="toolLoading" class="flex-1 py-8 text-center text-sm text-muted-foreground">
          正在读取输入配置...
        </div>
        <div v-else class="min-h-0 flex-1 space-y-4 overflow-y-auto">
          <div
            v-if="toolError"
            class="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive"
          >
            {{ toolError }}
          </div>
          <div v-if="schemaProperties().length" class="space-y-4">
            <div v-for="[name, schema] in schemaProperties()" :key="name" class="space-y-1.5">
              <Label
                >{{ name }}
                <span v-if="schemaRequired().has(name)" class="text-destructive">*</span></Label
              >
              <p
                v-if="schema.description"
                class="break-words text-xs leading-5 text-muted-foreground"
              >
                {{ schema.description }}
              </p>
              <Select v-if="schemaOptions(schema).length" v-model="toolArgs[name]">
                <SelectTrigger>
                  <SelectValue :placeholder="fieldPlaceholder(schema) || '选择值'" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem
                      v-for="option in schemaOptions(schema)"
                      :key="String(option)"
                      :value="String(option)"
                      >{{ String(option) }}</SelectItem
                    >
                  </SelectGroup>
                </SelectContent> </Select
              ><Textarea
                v-else-if="fieldUsesTextarea(schema)"
                v-model="toolArgs[name]"
                rows="4"
                :placeholder="
                  schema.type === 'array' || schema.type === 'object'
                    ? '请输入 JSON'
                    : fieldPlaceholder(schema)
                "
              />
              <div v-else-if="schema.type === 'boolean'" class="flex items-center gap-2">
                <Checkbox
                  :model-value="!!toolArgs[name]"
                  @update:model-value="setToolBoolean(name, $event)"
                /><span class="text-sm text-muted-foreground">{{
                  toolArgs[name] ? '是' : '否'
                }}</span>
              </div>
              <Input
                v-else
                v-model="toolArgs[name]"
                :type="schema.type === 'number' || schema.type === 'integer' ? 'number' : 'text'"
                :min="schema.minimum"
                :max="schema.maximum"
                :step="schema.multipleOf || (schema.type === 'integer' ? 1 : undefined)"
                :placeholder="fieldPlaceholder(schema)"
              />
            </div>
          </div>
          <div v-else class="rounded-md border border-dashed p-4 text-sm text-muted-foreground">
            该工具不需要输入参数。
          </div>
          <div v-if="toolResult" class="space-y-1">
            <Label>执行结果</Label>
            <pre class="max-h-80 overflow-auto rounded-md bg-muted p-3 text-xs">{{
              JSON.stringify(toolResult, null, 2)
            }}</pre>
          </div>
        </div>
        <DialogFooter
          ><Button :disabled="toolExecuting || toolLoading || !!toolError" @click="executeTool">
            {{ toolExecuting ? '执行中...' : '执行调用' }} </Button
          ><Button variant="outline" @click="testDialog = false">关闭</Button></DialogFooter
        >
      </DialogContent>
    </Dialog>
    <Dialog v-model:open="editToolDialog">
      <DialogContent class="sm:max-w-lg!">
        <DialogHeader>
          <DialogTitle>编辑工具（占位）</DialogTitle>
          <DialogDescription>当前仅提供界面预览，保存后端功能待实现。</DialogDescription>
        </DialogHeader>
        <div class="space-y-4">
          <div class="space-y-1.5"><Label>工具名称</Label><Input v-model="toolEditName" /></div>
          <div class="space-y-1.5">
            <Label>工具描述</Label><Textarea v-model="toolEditDescription" rows="4" />
          </div>
        </div>
        <DialogFooter><Button @click="editToolDialog = false">完成</Button></DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
</template>
