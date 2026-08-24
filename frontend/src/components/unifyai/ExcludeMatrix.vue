<script setup lang="ts">
import { computed } from 'vue'
import {
  RiCheckDoubleLine,
  RiDeleteBin2Line,
  RiDeleteBinLine,
  RiEditLine,
  RiInformationLine,
  RiToggleFill,
  RiToggleLine,
} from '@remixicon/vue'
import { PLATFORMS, type McpMatrixCell, type McpServerInfo, type PlatformId } from '@/lib/unifyai'

const props = defineProps<{
  /** 源 mcp.json 全集（行 = 去重后的服务器） */
  servers: McpServerInfo[]
  /** 同步矩阵：serverName → platformId → 是否开启（undefined = 该平台未配置，点击=添加） */
  matrix: Record<string, Record<PlatformId, McpMatrixCell>>
  /** 已禁用的服务器名集合（整行半透明、单元格不可点，同步时跳过；写回 mcp.json 的 enabled） */
  disabled: Set<string>
}>()

const emit = defineEmits<{
  'update:matrix': [value: Record<string, Record<PlatformId, McpMatrixCell>>]
  'update:disabled': [value: Set<string>]
  /** 删除服务器 */
  remove: [name: string]
  /** 编辑单个服务器 */
  edit: [server: McpServerInfo]
}>()

const columns = computed(() => PLATFORMS)

/** 该平台列是否可交互（MCP 未实现/不可读的平台整列禁用） */
function platformLocked(platformId: PlatformId) {
  const platform = PLATFORMS.find((p) => p.id === platformId)
  return platform ? platform.mcpSync !== true : true
}

function cellState(name: string, platformId: PlatformId): McpMatrixCell {
  return props.matrix[name]?.[platformId]
}

function isDisabled(name: string) {
  return props.disabled.has(name)
}

/** 深拷贝矩阵（props 是只读代理，不能直接用 structuredClone） */
function cloneMatrix() {
  return Object.fromEntries(
    Object.entries(props.matrix).map(([name, row]) => [name, { ...row }]),
  ) as Record<string, Record<PlatformId, McpMatrixCell>>
}

/**
 * 单元格交互（forceMcp 语义）：
 * - 左键：在 开启(true) ↔ 删除/未配置(undefined) 间循环（删除 = forceMcp 下该平台不再保留此服务器）
 * - 右键：设为 关闭(false)（传入配置但 enabled:false；阻止默认右键菜单）
 */
function toggleCell(name: string, platformId: PlatformId) {
  if (isDisabled(name) || platformLocked(platformId)) return
  const next = cloneMatrix()
  const current = next[name]?.[platformId]
  // true → undefined（删除）；其他（undefined/false）→ true（开启）
  next[name] = { ...next[name], [platformId]: current === true ? undefined : true }
  emit('update:matrix', next)
}

/** 右键点击：设为关闭（false）；已关闭则切回开启（避免死状态） */
function onCellContextMenu(name: string, platformId: PlatformId) {
  if (isDisabled(name) || platformLocked(platformId)) return
  const next = cloneMatrix()
  const current = next[name]?.[platformId]
  next[name] = { ...next[name], [platformId]: current === false ? true : false }
  emit('update:matrix', next)
}

/** 切换服务器启用/禁用（写回 mcp.json 的 enabled，整行半透明） */
function toggleServerDisabled(name: string) {
  const next = new Set(props.disabled)
  if (next.has(name)) next.delete(name)
  else next.add(name)
  emit('update:disabled', next)
}

/** 行级：全部开启 / 全部关闭（当前行所有平台列统一设置） */
function setRow(name: string, enabled: boolean) {
  if (isDisabled(name)) return
  const next = cloneMatrix()
  next[name] = { ...next[name] }
  for (const platform of PLATFORMS) {
    if (!platformLocked(platform.id)) next[name][platform.id] = enabled
  }
  emit('update:matrix', next)
}

/** 行级：全部删除（设整行为未配置 undefined，配合 forceMcp 从目标平台删除） */
function setRowRemove(name: string) {
  if (isDisabled(name)) return
  const next = cloneMatrix()
  next[name] = { ...next[name] }
  for (const platform of PLATFORMS) {
    if (!platformLocked(platform.id)) next[name][platform.id] = undefined
  }
  emit('update:matrix', next)
}

const stats = computed(() => {
  let on = 0
  let off = 0
  let unset = 0
  for (const row of Object.values(props.matrix)) {
    for (const platform of PLATFORMS) {
      const v = row?.[platform.id]
      if (v === true) on++
      else if (v === false) off++
      else unset++
    }
  }
  return { on, off, unset }
})
</script>

<template>
  <TooltipProvider>
    <div class="space-y-3">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <p class="flex items-center gap-1.5 text-sm text-muted-foreground">
        <RiInformationLine size="15" />
        左键：✓ 开启 ↔ ✕ 删除（force-mcp 下未配置的将从目标平台删除）；右键：✕ 关闭（配置保留但禁用）。改动后点「开始同步」批量落地。
      </p>
      <div class="flex items-center gap-2">
        <span class="text-sm text-muted-foreground tabular-nums">
          开 {{ stats.on }} · 关 {{ stats.off }} · 未配置 {{ stats.unset }}
        </span>
      </div>
    </div>
    <div class="overflow-x-auto rounded-md border">
      <Table class="w-full min-w-[720px] table-fixed">
        <TableHeader>
          <TableRow>
            <TableHead class="w-44">MCP 服务器</TableHead>
            <TableHead v-for="platform in columns" :key="platform.id" class="text-center">
              {{ platform.name }}
            </TableHead>
            <TableHead class="w-56 text-right">操作</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          <TableRow v-for="server in servers" :key="server.name"
            :class="isDisabled(server.name) ? 'opacity-50' : ''">
            <TableCell>
              <div class="flex min-w-0 items-center gap-2">
                <Badge v-if="isDisabled(server.name)" variant="outline" class="shrink-0 font-normal">
                  已禁用</Badge>
                <span class="truncate font-mono text-xs" :class="isDisabled(server.name)
                  ? 'text-muted-foreground'
                  : ''">{{ server.name }}</span>
                <Badge variant="outline" class="shrink-0 px-1.5 font-normal text-[10px]">
                  {{ server.type === 'remote' ? 'remote' : 'local' }}
                </Badge>
              </div>
            </TableCell>
            <TableCell v-for="platform in columns" :key="platform.id" class="text-center">
              <Tooltip v-if="!platformLocked(platform.id)">
                <TooltipTrigger as-child>
                  <button
                    type="button"
                    class="inline-grid size-7 place-items-center rounded-md border text-xs transition-colors disabled:cursor-not-allowed disabled:opacity-60"
                    :class="
                      isDisabled(server.name)
                        ? 'cursor-not-allowed border-border/50 bg-muted/30 text-muted-foreground/40'
                        : cellState(server.name, platform.id) === true
                          ? 'border-emerald-600/40 bg-emerald-500/10 text-emerald-700 dark:text-emerald-300'
                          : cellState(server.name, platform.id) === false
                            ? 'border-border bg-muted/40 text-muted-foreground/60'
                            : 'border-dashed border-red-400/40 bg-red-500/5 text-red-500/70 hover:border-primary/50 hover:text-primary'
                    "
                    :disabled="isDisabled(server.name)"
                    :aria-label="`${server.name} 在 ${platform.name}：${cellState(server.name, platform.id) === undefined ? '未配置（force-mcp 下将被删除），左键开启' : cellState(server.name, platform.id) === true ? '开启，左键删除，右键关闭' : '关闭，左键开启，右键切回开启'}`"
                    :aria-pressed="!isDisabled(server.name) && cellState(server.name, platform.id) === true"
                    @click="toggleCell(server.name, platform.id)"
                    @contextmenu.prevent="onCellContextMenu(server.name, platform.id)"
                  >
                    <span v-if="cellState(server.name, platform.id) === true">✓</span>
                    <span v-else-if="cellState(server.name, platform.id) === false">✕</span>
                    <span v-else class="opacity-70">✕</span>
                  </button>
                </TooltipTrigger>
                <TooltipContent>
                  {{ server.name }} @ {{ platform.name }}：左键 = 开启/删除，右键 = 关闭
                </TooltipContent>
              </Tooltip>
              <span
                v-else
                class="inline-block size-7 rounded-md border border-dashed border-border/60 text-[10px] leading-7 text-muted-foreground/40"
                aria-hidden="true"
              >
                —
              </span>
            </TableCell>
            <TableCell class="text-right">
              <div class="flex justify-end gap-1">
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button variant="ghost" size="icon" class="size-8" :disabled="isDisabled(server.name)"
                      :aria-label="`${server.name} 全部开启`" @click="setRow(server.name, true)">
                      <RiCheckDoubleLine size="14" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>全部开启</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button variant="ghost" size="icon" class="size-8 text-muted-foreground hover:text-destructive"
                      :disabled="isDisabled(server.name)" :aria-label="`${server.name} 全部删除`"
                      @click="setRowRemove(server.name)">
                      <RiDeleteBin2Line size="14" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>全部删除（从所有平台移除）</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button variant="ghost" size="icon" class="size-8"
                      :aria-label="isDisabled(server.name) ? `启用 ${server.name}` : `禁用 ${server.name}`"
                      @click="toggleServerDisabled(server.name)">
                      <RiToggleFill v-if="isDisabled(server.name)" class="size-5 text-emerald-600" />
                      <RiToggleLine v-else class="size-5 text-muted-foreground" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>{{ isDisabled(server.name) ? '启用' : '禁用' }}</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button variant="ghost" size="icon" class="size-8 text-muted-foreground hover:text-foreground"
                      :aria-label="`编辑 ${server.name}`" @click="emit('edit', server)">
                      <RiEditLine size="14" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>编辑</TooltipContent>
                </Tooltip>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <Button variant="ghost" size="icon"
                      class="size-8 text-muted-foreground hover:text-destructive"
                      :aria-label="`删除 ${server.name}`" @click="emit('remove', server.name)">
                      <RiDeleteBinLine size="14" />
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>删除（从 mcp.json 移除）</TooltipContent>
                </Tooltip>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>
      <div v-if="!servers.length"
        class="flex min-h-32 flex-col items-center justify-center gap-1 border-t p-6 text-center">
        <p class="text-sm text-muted-foreground">暂无 MCP 配置，先运行「导入各平台配置到源」。</p>
      </div>
    </div>
    <div class="flex items-center justify-end gap-3">
      <p class="text-xs text-muted-foreground">矩阵改动后到「③ 执行」点「开始同步」落地到各平台。</p>
    </div>
    </div>
  </TooltipProvider>
</template>
