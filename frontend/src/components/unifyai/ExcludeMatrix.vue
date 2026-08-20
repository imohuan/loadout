<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  RiCheckLine,
  RiCloseLine,
  RiFilterOffLine,
  RiFilterLine,
  RiInformationLine,
} from '@remixicon/vue'
import { PLATFORMS, type McpServerInfo, type PlatformId } from '@/lib/unifyai'

const props = defineProps<{
  /** 已启用的 MCP 服务器（矩阵行） */
  servers: McpServerInfo[]
  /** 排除矩阵：serverName → platformId → 是否排除 */
  matrix: Record<string, Record<PlatformId, boolean>>
  /** MCP 目标平台白名单；空数组 = 不限定（全部平台同步 MCP） */
  whitelist: PlatformId[]
}>()

const emit = defineEmits<{
  'update:matrix': [value: Record<string, Record<PlatformId, boolean>>]
  'update:whitelist': [value: PlatformId[]]
}>()

/** 展示「只显示被排除项」 */
const showExcludedOnly = ref(false)

const columns = computed(() => PLATFORMS)

const visibleServers = computed(() => {
  if (!showExcludedOnly.value) return props.servers
  return props.servers.filter((server) => isServerGloballyExcluded(server.name))
})

/** 该服务器是否被所有平台排除（整行 ✕ = 全局排除，映射 --mcp-exclude） */
function isServerGloballyExcluded(name: string) {
  const row = props.matrix[name]
  if (!row) return false
  return PLATFORMS.every((platform) => row[platform.id] === true)
}

function isCellExcluded(name: string, platformId: PlatformId) {
  return props.matrix[name]?.[platformId] === true
}

function toggleCell(name: string, platformId: PlatformId) {
  const next = structuredClone(props.matrix)
  next[name] = { ...next[name], [platformId]: !isCellExcluded(name, platformId) }
  emit('update:matrix', next)
}

/** 单行：全部排除 / 全部恢复 */
function setRow(name: string, excluded: boolean) {
  const next = structuredClone(props.matrix)
  next[name] = { ...next[name], ...Object.fromEntries(PLATFORMS.map((p) => [p.id, excluded])) }
  emit('update:matrix', next)
}

/** 白名单开关：关闭 = 不限定（全部） */
const whitelistEnabled = computed(() => props.whitelist.length > 0)

function toggleWhitelist() {
  emit('update:whitelist', whitelistEnabled.value ? [] : PLATFORMS.map((p) => p.id))
}

function toggleWhitelistPlatform(platformId: PlatformId) {
  const next = props.whitelist.includes(platformId)
    ? props.whitelist.filter((id) => id !== platformId)
    : [...props.whitelist, platformId]
  emit('update:whitelist', next)
}

const excludedCount = computed(() => {
  let count = 0
  for (const name of Object.keys(props.matrix))
    for (const platform of PLATFORMS) if (props.matrix[name]?.[platform.id]) count++
  return count
})
</script>

<template>
  <Tabs default-value="matrix">
    <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
      <TabsTrigger value="matrix">排除矩阵</TabsTrigger>
      <TabsTrigger value="whitelist">MCP 目标平台白名单</TabsTrigger>
    </TabsList>

    <TabsContent value="matrix" class="space-y-3 pt-2">
      <div class="flex flex-wrap items-center justify-between gap-2">
        <p class="flex items-center gap-1.5 text-sm text-muted-foreground">
          <RiInformationLine size="15" />
          单元格点击切换：整行 ✕ 为全局排除（--mcp-exclude），单个 ✕ 为按平台排除
          （--mcp-exclude-for）。已排除 {{ excludedCount }} 项。
        </p>
        <div class="flex items-center gap-2">
          <Switch
            :checked="showExcludedOnly"
            @update:checked="showExcludedOnly = $event"
            aria-label="只显示被排除项"
          />
          <Label class="flex items-center gap-1 text-sm">
            <RiFilterOffLine v-if="showExcludedOnly" size="14" />
            <RiFilterLine v-else size="14" />
            只显示被排除项
          </Label>
        </div>
      </div>
      <div class="overflow-x-auto rounded-md border">
        <Table class="w-full min-w-[720px] table-fixed">
          <TableHeader>
            <TableRow>
              <TableHead class="w-44">MCP 服务器</TableHead>
              <TableHead v-for="platform in columns" :key="platform.id" class="w-24 text-center">
                {{ platform.name }}
              </TableHead>
              <TableHead class="w-28 text-right">操作</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <TableRow v-for="server in visibleServers" :key="server.name">
              <TableCell>
                <div class="flex min-w-0 items-center gap-2">
                  <Badge
                    v-if="isServerGloballyExcluded(server.name)"
                    variant="destructive"
                    class="shrink-0 font-normal"
                    >全局排除</Badge
                  >
                  <span
                    class="truncate font-mono text-xs"
                    :class="isServerGloballyExcluded(server.name) ? 'text-muted-foreground' : ''"
                    >{{ server.name }}</span
                  >
                </div>
              </TableCell>
              <TableCell v-for="platform in columns" :key="platform.id" class="text-center">
                <button
                  type="button"
                  class="inline-grid size-6 place-items-center rounded-sm border transition-colors"
                  :class="
                    isCellExcluded(server.name, platform.id)
                      ? 'border-destructive/50 bg-destructive/10 text-destructive'
                      : 'border-border text-muted-foreground hover:border-primary/50 hover:text-primary'
                  "
                  :aria-label="
                    isCellExcluded(server.name, platform.id)
                      ? `恢复 ${server.name} 在 ${platform.name} 的同步`
                      : `排除 ${server.name} 在 ${platform.name} 的同步`
                  "
                  :aria-pressed="isCellExcluded(server.name, platform.id)"
                  @click="toggleCell(server.name, platform.id)"
                >
                  <RiCloseLine v-if="isCellExcluded(server.name, platform.id)" size="14" />
                  <RiCheckLine v-else size="14" />
                </button>
              </TableCell>
              <TableCell class="text-right">
                <div class="flex justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2 text-xs"
                    @click="setRow(server.name, true)"
                  >
                    排除全部
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-2 text-xs"
                    :disabled="!isServerGloballyExcluded(server.name)"
                    @click="setRow(server.name, false)"
                  >
                    恢复全部
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          </TableBody>
        </Table>
      </div>
    </TabsContent>

    <TabsContent value="whitelist" class="space-y-3 pt-2">
      <div class="flex items-center justify-between gap-3 rounded-md border p-3">
        <div class="min-w-0">
          <Label class="text-sm font-medium">仅以下平台执行 MCP 同步（--mcp-platforms）</Label>
          <p class="mt-0.5 text-xs leading-5 text-muted-foreground">
            未列出的平台将完全跳过 MCP 同步（⊘ 白名单外）。关闭开关 = 所有平台同步 MCP。
          </p>
        </div>
        <Switch :checked="whitelistEnabled" @update:checked="toggleWhitelist" />
      </div>
      <div v-if="whitelistEnabled" class="flex flex-wrap gap-2 rounded-md border p-3">
        <button
          v-for="platform in columns"
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
      <p v-else class="text-sm text-muted-foreground">
        <RiCheckLine size="14" class="mr-1 inline text-primary" />
        未限定：全部平台同步 MCP。
      </p>
    </TabsContent>
  </Tabs>
</template>
