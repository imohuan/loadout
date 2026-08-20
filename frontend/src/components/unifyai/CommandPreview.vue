<script setup lang="ts">
import { ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiClipboardLine,
  RiCloudLine,
  RiCodeSSlashLine,
  RiFileList3Line,
  RiServerLine,
} from '@remixicon/vue'
import type { ModelSourceKind, ModelSourceStatus } from '@/lib/unifyai'

const props = defineProps<{
  /** 实时拼装的 CLI 命令 */
  command: string
  /** 模型来源状态（文档 §5.2） */
  modelSource: ModelSourceStatus
  /** MCP 配置文件路径 */
  mcpSourcePath: string
  /** MCP 服务器启用数 / 总数 */
  mcpEnabled: number
  mcpTotal: number
  /** 元数据缓存更新时间（空 = 未刷新） */
  metadataUpdatedAt?: string
}>()

const expanded = ref(true)

async function copyCommand() {
  await navigator.clipboard.writeText(props.command)
  toast.success('命令已复制', { description: '粘贴到终端执行即可完成同步' })
}

const modelLabel: Record<
  ModelSourceKind,
  { text: string; variant: 'default' | 'outline' | 'destructive' }
> = {
  proxy: { text: 'OpenCodex 代理', variant: 'default' },
  fallback: { text: '已降级逐个 Provider', variant: 'outline' },
  none: { text: '模型源不可用', variant: 'destructive' },
}
</script>

<template>
  <Card class="rounded-md">
    <CardHeader class="flex flex-row items-center justify-between space-y-0 pb-2">
      <div class="space-y-0.5">
        <CardTitle class="text-base">数据预览</CardTitle>
        <CardDescription>将要同步的数据来源与最终执行命令。</CardDescription>
      </div>
      <Button variant="ghost" size="icon" class="size-8" @click="expanded = !expanded">
        <RiArrowDownSLine v-if="expanded" size="16" />
        <RiArrowRightSLine v-else size="16" />
      </Button>
    </CardHeader>
    <CardContent v-show="expanded" class="space-y-3">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="flex items-center gap-3 rounded-md border p-3">
          <span class="grid size-8 shrink-0 place-items-center rounded-md bg-muted">
            <RiCloudLine size="16" class="text-muted-foreground" />
          </span>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-1.5 text-sm">
              <span class="font-medium">模型来源</span>
              <Badge :variant="modelLabel[modelSource.kind].variant" class="font-normal">
                {{ modelLabel[modelSource.kind].text }}
              </Badge>
            </div>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">
              {{ modelSource.url }} · {{ modelSource.count }} 个模型
            </p>
            <p v-if="metadataUpdatedAt" class="mt-0.5 text-xs text-emerald-600">
              元数据缓存：{{ metadataUpdatedAt }} 已刷新
            </p>
          </div>
        </div>
        <div class="flex items-center gap-3 rounded-md border p-3">
          <span class="grid size-8 shrink-0 place-items-center rounded-md bg-muted">
            <RiFileList3Line size="16" class="text-muted-foreground" />
          </span>
          <div class="min-w-0">
            <div class="flex flex-wrap items-center gap-1.5 text-sm">
              <span class="font-medium">MCP 来源</span>
              <Badge variant="secondary" class="font-normal">{{ mcpEnabled }}/{{ mcpTotal }} 启用</Badge>
            </div>
            <p class="mt-0.5 truncate font-mono text-xs text-muted-foreground">{{ mcpSourcePath }}</p>
          </div>
        </div>
      </div>
      <div class="rounded-md border bg-muted/40 p-3">
        <div class="mb-2 flex items-center justify-between gap-2">
          <div class="flex items-center gap-2 text-xs font-medium text-muted-foreground">
            <RiCodeSSlashLine size="14" />
            命令预览（实时生成，等宽字体）
          </div>
          <Button variant="outline" size="sm" class="h-7 px-2 text-xs" @click="copyCommand">
            <RiClipboardLine size="14" />复制命令
          </Button>
        </div>
        <code class="block overflow-x-auto whitespace-pre rounded-md bg-background p-3 font-mono text-xs leading-6">
          {{ command }}
        </code>
        <p class="mt-2 flex items-center gap-1 text-xs text-muted-foreground">
          <RiServerLine size="13" />
          复制到终端执行即可，等价于下方所有 UI 选项。
        </p>
      </div>
    </CardContent>
  </Card>
</template>
