<script setup lang="ts">
import type { Platform } from '@/lib/unifyai'

/**
 * 「平台能力矩阵」帮助弹窗：纯展示各平台对模型 / MCP 同步的支持情况。
 * 完全自包含，只依赖 platforms 数据与自身开关，供 UnifyaiPanel 引用。
 */
defineProps<{
  open: boolean
  platforms: Platform[]
}>()

defineEmits<{
  'update:open': [value: boolean]
}>()
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => $emit('update:open', v)">
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
      <DialogFooter><Button variant="ghost" @click="$emit('update:open', false)">关闭</Button></DialogFooter>
    </DialogContent>
  </Dialog>
</template>
