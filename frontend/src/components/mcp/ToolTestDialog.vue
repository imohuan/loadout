<script setup lang="ts">
import TranslateText from '@/components/TranslateText.vue'

/**
 * 「测试工具」弹窗：按工具 JSON Schema 渲染参数表单并执行调用。
 * 状态与逻辑仍由父组件（McpPanel）持有，本组件只负责该弹窗的模板呈现，
 * 通过 props 接收状态、通过 props 回调触发父组件的方法，行为与拆分前完全一致。
 */

interface ActiveTool {
  serverId: string
  serverName: string
  name: string
  description?: string
}

defineProps<{
  open: boolean
  activeTool: ActiveTool | null
  toolSchema: Record<string, any> | null
  toolArgs: Record<string, any>
  toolResult: any
  toolError: string
  toolLoading: boolean
  toolExecuting: boolean
  schemaProperties: () => [string, Record<string, any>][]
  schemaRequired: () => Set<string>
  schemaOptions: (schema: Record<string, any>) => any[]
  fieldPlaceholder: (schema: Record<string, any>) => string
  fieldUsesTextarea: (schema: Record<string, any>) => boolean
  toolDescKey: (serverName: string, toolName: string) => string
  toolParamDescKey: (serverName: string, toolName: string, paramName: string) => string
  onExecute: () => void
  onSetToolBoolean: (name: string, value: boolean) => void
  onClose: () => void
}>()
</script>

<template>
  <Dialog :open="open" @update:open="(v: boolean) => (v ? undefined : onClose())">
    <DialogContent class="flex max-h-[calc(100dvh-2rem)] flex-col overflow-hidden sm:max-w-2xl!">
      <DialogHeader>
        <DialogTitle class="truncate">测试工具：{{ activeTool?.name }}</DialogTitle>
        <DialogDescription class="line-clamp-2 break-words">
          <TranslateText v-if="activeTool?.description" :source="activeTool.description"
            :text-key="toolDescKey(activeTool.serverName, activeTool.name)" source-type="mcp"
            :source-id="`${activeTool.serverName}/${activeTool.name}`" />
          <span v-else>填写参数后点击执行调用。</span>
        </DialogDescription>
      </DialogHeader>
      <div v-if="toolLoading" class="flex-1 py-8 text-center text-sm text-muted-foreground">
        正在读取输入配置...
      </div>
      <div v-else class="relative flex min-h-0 flex-1 flex-col ">
        <div class="flex-1 space-y-4 overflow-y-auto overflow-x-visible px-1
                 [scrollbar-width:thin] [scrollbar-color:theme(colors.zinc.400)_transparent]
                 [&::-webkit-scrollbar]:w-1.5
                 [&::-webkit-scrollbar-track]:bg-transparent
                 [&::-webkit-scrollbar-thumb]:rounded-full
                 [&::-webkit-scrollbar-thumb]:bg-zinc-300/80
                 hover:[&::-webkit-scrollbar-thumb]:bg-zinc-400/80
                 dark:[&::-webkit-scrollbar-thumb]:bg-zinc-600/80
                 dark:hover:[&::-webkit-scrollbar-thumb]:bg-zinc-500/80">
          <div v-if="toolError"
            class="rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
            {{ toolError }}
          </div>
          <div v-if="schemaProperties().length" class="space-y-4 pb-2">
            <div v-for="[name, schema] in schemaProperties()" :key="name" class="space-y-1.5">
              <Label>{{ name }}
                <span v-if="schemaRequired().has(name)" class="text-destructive">*</span></Label>
              <p v-if="schema.description" class="break-words text-xs leading-5 text-muted-foreground">
                <TranslateText :source="schema.description"
                  :text-key="toolParamDescKey(activeTool?.serverName || '', activeTool?.name || '', name)"
                  source-type="mcp"
                  :source-id="`${activeTool?.serverName || ''}/${activeTool?.name || ''}/param/${name}/description`" />
              </p>
              <Select v-if="schemaOptions(schema).length" v-model="toolArgs[name]">
                <SelectTrigger>
                  <SelectValue :placeholder="fieldPlaceholder(schema) || '选择值'" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem v-for="option in schemaOptions(schema)" :key="String(option)" :value="String(option)">{{
                      String(option) }}</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select><Textarea v-else-if="fieldUsesTextarea(schema)" v-model="toolArgs[name]" rows="4" :placeholder="schema.type === 'array' || schema.type === 'object'
                  ? '请输入 JSON'
                  : fieldPlaceholder(schema)
                " />
              <div v-else-if="schema.type === 'boolean'" class="flex items-center gap-2">
                <Checkbox :model-value="!!toolArgs[name]" @update:model-value="onSetToolBoolean(name, $event)" /><span
                  class="text-sm text-muted-foreground">{{
                    toolArgs[name] ? '是' : '否'
                  }}</span>
              </div>
              <Input v-else v-model="toolArgs[name]"
                :type="schema.type === 'number' || schema.type === 'integer' ? 'number' : 'text'" :min="schema.minimum"
                :max="schema.maximum" :step="schema.multipleOf || (schema.type === 'integer' ? 1 : undefined)"
                :placeholder="fieldPlaceholder(schema)" />
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
        <!-- 底部白色蒙版，提示内容可向下滚动 -->
        <div
          class="pointer-events-none absolute inset-x-0 bottom-0 h-8 bg-gradient-to-t from-background to-transparent" />
      </div>
      <DialogFooter><Button :disabled="toolExecuting || toolLoading" @click="onExecute">
          {{ toolExecuting ? '执行中...' : '执行调用' }} </Button><Button variant="outline" @click="onClose">关闭</Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
