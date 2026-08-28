<script setup lang="ts">
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowUpLine,
  RiDeleteBinLine,
} from '@remixicon/vue'
import { toast } from 'vue-sonner'
import type { MessageInjection } from '@/lib/types'

const props = withDefaults(
  defineProps<{
    showMove?: boolean
    showRemove?: boolean
    showAdd?: boolean
    addLabel?: string
  }>(),
  {
    showMove: true,
    showRemove: true,
    showAdd: true,
    addLabel: '添加注入',
  },
)

const model = defineModel<MessageInjection[]>({ required: true })

// 位置选项文案。
const positionOptions = [
  { value: 'prepend', label: '新增为第一条' },
  { value: 'append', label: '新增为最后一条' },
  { value: 'prepend_first', label: '拼接到原始第一条开头' },
  { value: 'append_first', label: '拼接到原始第一条结尾' },
]
const roleOptions = [
  { value: 'system', label: 'system' },
  { value: 'user', label: 'user' },
  { value: 'assistant', label: 'assistant' },
]

function addItem() {
  model.value.push({ role: 'system', content: '', position: 'prepend' })
}
function removeItem(index: number) {
  if (model.value.length > 1) model.value.splice(index, 1)
}
function moveItem(index: number, direction: -1 | 1) {
  const target = index + direction
  if (target < 0 || target >= model.value.length) return
  const [item] = model.value.splice(index, 1)
  model.value.splice(target, 0, item)
}

// ===== JSON 导入导出 =====
function serialize(): MessageInjection[] {
  const items: MessageInjection[] = []
  for (const r of model.value || []) {
    const content = (r?.content || '').toString()
    if (!content.trim()) continue
    items.push({
      role: (r?.role || 'system').toString(),
      content,
      position: (r?.position || 'prepend').toString(),
    })
  }
  return items
}

function parseClipboardText(text: string): MessageInjection[] | null {
  if (!text || !text.trim()) return null
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch {
    return null
  }
  if (Array.isArray(data)) return normalizeItems(data)
  if (data && typeof data === 'object' && Array.isArray((data as { items?: unknown }).items)) {
    return normalizeItems((data as { items: unknown[] }).items)
  }
  return null
}

function normalizeItems(raw: unknown[]): MessageInjection[] | null {
  const out: MessageInjection[] = []
  for (const item of raw) {
    if (!item || typeof item !== 'object') return null
    const r = item as Record<string, unknown>
    if (typeof r.content !== 'string') return null
    if (typeof r.role !== 'string') return null
    const position = typeof r.position === 'string' ? r.position : 'prepend'
    out.push({ role: r.role, content: r.content, position })
  }
  return out
}

async function exportToClipboard(): Promise<void> {
  try {
    const payload = serialize()
    await navigator.clipboard.writeText(JSON.stringify(payload, null, 2))
    toast.success('已复制到剪贴板', { description: `${payload.length} 条注入，JSON 格式` })
  } catch (e) {
    toast.error('复制失败', { description: e instanceof Error ? e.message : String(e) })
  }
}

async function importFromClipboard(): Promise<void> {
  if (!navigator.clipboard?.readText) {
    toast.error('当前环境不支持读取剪贴板', {
      description: '请先复制 JSON 后再用导入功能（部分浏览器需 HTTPS）。',
    })
    return
  }
  let text: string
  try {
    text = await navigator.clipboard.readText()
  } catch (e) {
    toast.error('读取剪贴板失败', {
      description: e instanceof Error ? e.message : '请检查浏览器剪贴板权限',
    })
    return
  }
  const items = parseClipboardText(text)
  if (!items) {
    toast.error('解析失败', {
      description: '剪贴板内容不是合法的 JSON（需为数组或带 items 字段的对象）',
    })
    return
  }
  model.value = items.length ? items : [{ role: 'system', content: '', position: 'prepend' }]
  toast.success('已导入', { description: `共 ${items.length} 条注入` })
}

defineExpose({ exportToClipboard, importFromClipboard })
</script>

<template>
  <div class="space-y-2">
    <div
      v-for="(item, index) in model"
      :key="index"
      class="rounded-md border border-border p-2"
    >
      <div class="flex items-center gap-1.5">
        <Select v-model="item.role" class="w-[110px] shrink-0">
          <SelectTrigger class="h-7 w-[110px] shrink-0 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper" side="bottom" :side-offset="2">
            <SelectGroup>
              <SelectItem v-for="r in roleOptions" :key="r.value" :value="r.value">{{
                r.label
              }}</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        <Select v-model="item.position" class="min-w-0 flex-1">
          <SelectTrigger class="h-7 text-xs">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper" side="bottom" :side-offset="2">
            <SelectGroup>
              <SelectItem v-for="p in positionOptions" :key="p.value" :value="p.value">{{
                p.label
              }}</SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        <div class="flex flex-1 items-center justify-end gap-0.5">
        <TooltipProvider v-if="showMove">
          <Tooltip>
            <TooltipTrigger as-child>
              <Button
                variant="ghost"
                size="icon"
                type="button"
                :disabled="index === 0"
                aria-label="上移"
                @click="moveItem(index, -1)"
                ><RiArrowUpLine size="16" /></Button
            ></TooltipTrigger>
            <TooltipContent>上移</TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <TooltipProvider v-if="showMove">
          <Tooltip>
            <TooltipTrigger as-child>
              <Button
                variant="ghost"
                size="icon"
                type="button"
                :disabled="index === model.length - 1"
                aria-label="下移"
                @click="moveItem(index, 1)"
                ><RiArrowDownLine size="16" /></Button
            ></TooltipTrigger>
            <TooltipContent>下移</TooltipContent>
          </Tooltip>
        </TooltipProvider>
        <TooltipProvider v-if="showRemove">
          <Tooltip>
            <TooltipTrigger as-child>
              <Button
                variant="ghost"
                size="icon"
                type="button"
                :disabled="model.length === 1"
                aria-label="移除"
                @click="removeItem(index)"
                ><RiDeleteBinLine size="16" /></Button
            ></TooltipTrigger>
            <TooltipContent>移除</TooltipContent>
          </Tooltip>
        </TooltipProvider>
      </div>
      </div>
      <Textarea
        v-model="item.content"
        class="mt-1.5 min-h-14 min-w-0"
        placeholder="输入要注入的文本内容"
      />
    </div>
    <Button v-if="showAdd" type="button" variant="outline" size="sm" @click="addItem"
      ><RiAddLine size="16" />{{ addLabel }}</Button
    >
    <p class="text-xs text-muted-foreground">
      每条注入 = 角色 + 文本 + 位置，按从上到下的顺序依次应用。「原始第一条」指进入本能力前
      messages 的第一条（新注入的项不会覆盖它）。
    </p>
    <p class="text-xs text-muted-foreground">
      <span class="font-medium">位置说明：</span>新增为第一条/最后一条会在 messages
      数组最前/最后插入一条新消息；拼接到原始第一条开头/结尾则把文本追加到第一条消息内容的最前/最后
      （不新增消息）。
    </p>
  </div>
</template>
