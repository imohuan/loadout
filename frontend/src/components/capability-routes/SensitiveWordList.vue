<script setup lang="ts">
import { RiAddLine, RiArrowDownLine, RiArrowUpLine, RiDeleteBinLine } from '@remixicon/vue'
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
