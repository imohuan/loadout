<script setup lang="ts">
import { RiCheckLine, RiCloseLine } from '@remixicon/vue'
import type { Platform } from '@/lib/unifyai'

const props = defineProps<{
  platform: Platform
  /** 当前是否选中 */
  selected: boolean
  /** 不支持所选同步内容时置灰不可选 */
  disabled: boolean
  /** 置灰原因（tooltip 展示） */
  disableReason?: string
}>()

const emit = defineEmits<{ toggle: [platform: Platform] }>()

/** 模型能力徽章文案与样式 */
function modelBadge() {
  if (props.platform.modelSync)
    return { text: '模型 ✓', variant: 'default' as const }
  return { text: '模型 ✗', variant: 'secondary' as const }
}

/** MCP 能力徽章：支持 / 未实现 / 不支持 */
function mcpBadge() {
  const support = props.platform.mcpSync
  if (support === true) return { text: 'MCP ✓', variant: 'default' as const }
  if (support === 'unimplemented') return { text: 'MCP ⚠', variant: 'outline' as const }
  return { text: 'MCP ✗', variant: 'secondary' as const }
}
</script>

<template>
  <TooltipProvider>
    <Tooltip>
      <TooltipTrigger as-child>
        <button
          type="button"
          class="group relative flex w-full flex-col gap-2 rounded-md border p-3 text-left transition-colors disabled:cursor-not-allowed disabled:opacity-50"
          :class="
            selected
              ? 'border-primary bg-primary/5 ring-1 ring-primary'
              : 'border-border bg-background hover:border-primary/60'
          "
          :disabled="disabled"
          :aria-pressed="selected"
          @click="emit('toggle', platform)"
        >
          <div class="flex items-center gap-2">
            <span
              class="grid size-6 shrink-0 place-items-center rounded-sm text-[11px] font-bold text-white"
              :style="{ backgroundColor: platform.color }"
            >
              {{ platform.name.slice(0, 1) }}
            </span>
            <span class="min-w-0 flex-1 truncate font-medium">{{ platform.name }}</span>
            <RiCheckLine
              v-if="selected"
              size="16"
              class="shrink-0 text-primary"
              aria-hidden="true"
            />
            <RiCloseLine v-else-if="disabled" size="16" class="shrink-0 text-muted-foreground" />
          </div>
          <div class="flex flex-wrap gap-1">
            <Badge :variant="modelBadge().variant" class="gap-0.5 font-normal"
              >{{ modelBadge().text }}</Badge
            >
            <Badge :variant="mcpBadge().variant" class="gap-0.5 font-normal"
              >{{ mcpBadge().text }}</Badge
            >
          </div>
          <div class="truncate font-mono text-[11px] text-muted-foreground">
            {{ platform.format }}
          </div>
        </button>
      </TooltipTrigger>
      <TooltipContent side="bottom" align="start" class="max-w-xs">
        <div class="space-y-1">
          <div class="font-medium">{{ platform.name }}</div>
          <div class="font-mono text-xs">{{ platform.configPath }}</div>
          <div v-if="disabled" class="text-xs text-destructive">{{ disableReason }}</div>
        </div>
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
</template>
