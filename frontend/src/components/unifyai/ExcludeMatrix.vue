<script setup lang="ts">
import { computed, ref } from 'vue'
import {
  RiCheckLine,
  RiCloseLine,
  RiFilterLine,
  RiFilterOffLine,
  RiInformationLine,
  RiToggleFill,
  RiToggleLine,
} from '@remixicon/vue'
import { PLATFORMS, type McpServerInfo, type PlatformId } from '@/lib/unifyai'

const props = defineProps<{
  /** 所有 MCP 服务器（含已禁用的） */
  servers: McpServerInfo[]
  /** 排除矩阵：serverName → platformId → 是否排除 */
  matrix: Record<string, Record<PlatformId, boolean>>
  /** 已禁用的服务器名集合（界面仍展示，但参与同步时跳过） */
  disabled: Set<string>
}>()

const emit = defineEmits<{
  'update:matrix': [value: Record<string, Record<PlatformId, boolean>>]
  'update:disabled': [value: Set<string>]
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

function isDisabled(name: string) {
  return props.disabled.has(name)
}

/** 深拷贝矩阵（不能用 structuredClone：props 是只读代理，会抛 DataCloneError） */
function cloneMatrix() {
  return Object.fromEntries(
    Object.entries(props.matrix).map(([name, row]) => [name, { ...row }]),
  ) as Record<string, Record<PlatformId, boolean>>
}

function toggleCell(name: string, platformId: PlatformId) {
  // 已禁用的服务器不允许再调整平台排除（提示性禁用，但保留可见性便于恢复）
  if (isDisabled(name)) return
  const next = cloneMatrix()
  next[name] = { ...next[name], [platformId]: !isCellExcluded(name, platformId) }
  emit('update:matrix', next)
}

/** 单行：全部排除 / 全部恢复 */
function setRow(name: string, excluded: boolean) {
  if (isDisabled(name)) return
  const next = cloneMatrix()
  next[name] = { ...next[name], ...Object.fromEntries(PLATFORMS.map((p) => [p.id, excluded])) }
  emit('update:matrix', next)
}

/** 切换服务器启用/禁用 */
function toggleServerEnabled(name: string) {
  const next = new Set(props.disabled)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  emit('update:disabled', next)
}

const excludedCount = computed(() => {
  let count = 0
  for (const name of Object.keys(props.matrix))
    for (const platform of PLATFORMS) if (props.matrix[name]?.[platform.id]) count++
  return count
})
</script>

<template>
  <div class="space-y-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="flex items-center gap-1.5 text-sm text-muted-foreground">
        <RiInformationLine size="15" />
        单元格点击切换：整行 ✕ 为全局排除（--mcp-exclude），单个 ✕ 为按平台排除
        （--mcp-exclude-for）。已排除 {{ excludedCount }} 项。
      </p>
      <div class="flex items-center gap-2">
        <Switch v-model="showExcludedOnly" aria-label="只显示被排除项" />
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
            <TableHead class="w-32 text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow
            v-for="server in visibleServers"
            :key="server.name"
            :class="isDisabled(server.name) ? 'opacity-50' : ''"
          >
            <TableCell>
              <div class="flex min-w-0 items-center gap-2">
                <Badge
                  v-if="isServerGloballyExcluded(server.name)"
                  variant="destructive"
                  class="shrink-0 font-normal"
                  >全局排除</Badge
                >
                <Badge
                  v-else-if="isDisabled(server.name)"
                  variant="outline"
                  class="shrink-0 font-normal"
                  >已禁用</Badge
                >
                <span
                  class="truncate font-mono text-xs"
                  :class="
                    isServerGloballyExcluded(server.name) || isDisabled(server.name)
                      ? 'text-muted-foreground'
                      : ''
                  "
                  >{{ server.name }}</span
                >
              </div>
            </TableCell>
            <TableCell v-for="platform in columns" :key="platform.id" class="text-center">
              <button
                type="button"
                class="inline-grid size-6 place-items-center rounded-sm border transition-colors disabled:cursor-not-allowed disabled:opacity-60"
                :class="
                  isDisabled(server.name)
                    ? 'cursor-not-allowed border-border/50 bg-muted/30 text-muted-foreground/40'
                    : isCellExcluded(server.name, platform.id)
                      ? 'border-destructive/50 bg-destructive/10 text-destructive'
                      : 'border-border text-muted-foreground hover:border-primary/50 hover:text-primary'
                "
                :disabled="isDisabled(server.name)"
                :aria-label="
                  isDisabled(server.name)
                    ? `${server.name} 已禁用，无法调整排除`
                    : isCellExcluded(server.name, platform.id)
                      ? `恢复 ${server.name} 在 ${platform.name} 的同步`
                      : `排除 ${server.name} 在 ${platform.name} 的同步`
                "
                :aria-pressed="!isDisabled(server.name) && isCellExcluded(server.name, platform.id)"
                @click="toggleCell(server.name, platform.id)"
              >
                <RiCloseLine
                  v-if="!isDisabled(server.name) && isCellExcluded(server.name, platform.id)"
                  size="14"
                />
                <RiCheckLine v-else size="14" />
              </button>
            </TableCell>
            <TableCell class="text-right">
              <div class="flex justify-end gap-1">
                <Button
                  variant="ghost"
                  size="sm"
                  class="h-7 px-2 text-xs"
                  :disabled="isDisabled(server.name)"
                  @click="setRow(server.name, true)"
                >
                  排除全部
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  class="h-7 px-2 text-xs"
                  :disabled="isDisabled(server.name) || !isServerGloballyExcluded(server.name)"
                  @click="setRow(server.name, false)"
                >
                  恢复全部
                </Button>
                <TooltipProvider>
                  <Tooltip>
                    <TooltipTrigger as-child
                      ><Button
                        variant="ghost"
                        size="icon"
                        class="size-7"
                        :aria-label="isDisabled(server.name) ? `启用 ${server.name}` : `禁用 ${server.name}`"
                        @click="toggleServerEnabled(server.name)"
                      >
                        <RiToggleFill
                          v-if="isDisabled(server.name)"
                          size="14"
                          class="text-emerald-600"
                        />
                        <RiToggleLine v-else size="14" class="text-muted-foreground" />
                      </Button></TooltipTrigger
                    >
                    <TooltipContent>
                      {{ isDisabled(server.name) ? '启用' : '禁用' }}
                    </TooltipContent>
                  </Tooltip>
                </TooltipProvider>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
      <div
        v-if="!visibleServers.length"
        class="flex min-h-32 flex-col items-center justify-center gap-1 border-t p-6 text-center"
      >
        <RiFilterOffLine size="20" class="text-muted-foreground" />
        <p class="text-sm text-muted-foreground">
          {{ showExcludedOnly ? '当前没有全局排除的服务器。' : '没有可同步的 MCP 服务器。' }}
        </p>
      </div>
    </div>
  </div>
</template>
