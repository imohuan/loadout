<script setup lang="ts">
import { RiAddLine, RiArrowDownLine, RiArrowUpLine, RiDeleteBinLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import type { SensitiveReplacement } from '@/lib/types'

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
    addLabel: '添加规则',
  },
)

const model = defineModel<SensitiveReplacement[]>({ required: true })

function addItem() {
  model.value.push({ from: '', to: '', regex: false })
}
function removeItem(index: number) {
  if (model.value.length > 1) model.value.splice(index, 1)
}
// 上下移动（排序，即替换执行顺序）。
function moveItem(index: number, direction: -1 | 1) {
  const target = index + direction
  if (target < 0 || target >= model.value.length) return
  const [item] = model.value.splice(index, 1)
  model.value.splice(target, 0, item)
}

// ===== JSON 导入导出 =====
// 导出格式：纯 items 数组（[{from,to,regex}, ...]），剪贴板内容干净直观；
// 导入时兼容「直接数组」与「{ items: [...] }」两种剪贴板内容。
function serialize(): SensitiveReplacement[] {
  // 过滤空规则、归一化字段，避免把空白行导入回来。
  const items: SensitiveReplacement[] = []
  for (const r of model.value || []) {
    const from = (r?.from || '').toString()
    const to = (r?.to ?? '').toString()
    if (!from.trim()) continue
    items.push({ from, to, regex: !!r?.regex })
  }
  return items
}

// 从剪贴板文本里解析出规则数组（不修改 model，只返回结果）。
// 返回 null 表示解析失败（由调用方统一 toast）。
function parseClipboardText(text: string): SensitiveReplacement[] | null {
  if (!text || !text.trim()) return null
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch {
    return null
  }
  // 直接是数组：[...] 形式。
  if (Array.isArray(data)) {
    return normalizeItems(data)
  }
  // 对象形式：{ items: [...] }。
  if (data && typeof data === 'object' && Array.isArray((data as { items?: unknown }).items)) {
    return normalizeItems((data as { items: unknown[] }).items)
  }
  return null
}

// 校验 + 归一化每一项；遇到非法项整体返回 null。
function normalizeItems(raw: unknown[]): SensitiveReplacement[] | null {
  const out: SensitiveReplacement[] = []
  for (const item of raw) {
    if (!item || typeof item !== 'object') return null
    const r = item as Record<string, unknown>
    if (typeof r.from !== 'string') return null
    if (typeof r.to !== 'string') return null
    const regex = r.regex === undefined ? false : !!r.regex
    // 允许 from 为空字符串（保留「占位未填」的行），但完全缺字段则拒绝。
    out.push({ from: r.from, to: r.to, regex })
  }
  return out
}

async function exportToClipboard(): Promise<void> {
  try {
    const payload = serialize()
    const text = JSON.stringify(payload, null, 2)
    await navigator.clipboard.writeText(text)
    toast.success('已复制到剪贴板', {
      description: `${payload.length} 条规则，JSON 格式`,
    })
  } catch (e) {
    toast.error('复制失败', {
      description: e instanceof Error ? e.message : String(e),
    })
  }
}

async function importFromClipboard(): Promise<void> {
  // 仅支持 secure context（https / localhost / 局域网 IP）。
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
  // 直接覆盖整个列表（含 0 条也允许 = 清空）。
  model.value = items.length
    ? items
    : [{ from: '', to: '', regex: false }]
  toast.success('已导入', {
    description: `共 ${items.length} 条规则`,
  })
}

defineExpose({ exportToClipboard, importFromClipboard })
</script>

<template>
  <div class="space-y-2">
    <div v-for="(item, index) in model" :key="index" class="flex items-center gap-2">
      <div class="flex flex-1 items-center gap-1.5">
        <Input v-model="item.from" placeholder="原始内容 / 敏感词" class="flex-1" />
        <span class="shrink-0 text-muted-foreground">→</span>
        <Input v-model="item.to" placeholder="替换后内容" class="flex-1" />
        <label
          class="flex shrink-0 cursor-pointer select-none items-center gap-1.5 text-xs text-muted-foreground"
          title="开启后「原始内容」按正则表达式匹配，替换内容支持 $1 捕获组引用"
        >
          <Switch v-model="item.regex" size="sm" />
          正则
        </label>
      </div>
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
    <Button v-if="showAdd" type="button" variant="outline" size="sm" @click="addItem"
      ><RiAddLine size="16" />{{ addLabel }}</Button
    >
    <p class="text-xs text-muted-foreground">
      按从上到下的顺序依次替换；整体替换若破坏 JSON，会自动降级为只替换
      messages 下的文本内容（不报错）。正则开启时「原始内容」按正则匹配，替换内容支持
      <code class="font-mono">$1</code> 捕获组引用。
    </p>
    <p class="text-xs text-muted-foreground">
      <span class="font-medium">注意：</span>匹配对象是<strong>整个请求体文本</strong>（含 JSON
      字段名与结构符号）。敏感词请避免使用 `model`、`messages`、引号、逗号等可能出现在 JSON
      结构中的词，否则「替换」可能破坏 JSON 导致请求被拒，「命中拒绝」可能误拒正常请求。
    </p>
  </div>
</template>
