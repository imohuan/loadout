<script setup lang="ts">
// 目标模型下拉选择器：搜索 + 多选/单选 + 自定义添加，抽象自 CapabilityRouteEditor。
import { computed, ref } from 'vue'
import { RiCloseLine, RiSearchLine } from '@remixicon/vue'

const props = withDefaults(
  defineProps<{
    /** 候选模型列表（搜索源；调用方可按渠道等条件过滤后再传入） */
    models: string[]
    /** 多选（数组 v-model）还是单选（字符串 v-model） */
    multiple?: boolean
    modelValue: string[] | string
    /** 是否允许搜索不到时自定义添加 */
    allowCustom?: boolean
    /** 加载中态（仅影响触发按钮文案） */
    loading?: boolean
  }>(),
  { multiple: true, allowCustom: true, loading: false },
)
const emit = defineEmits<{ 'update:modelValue': [value: string[] | string] }>()

const open = ref(false)
const search = ref('')

// 统一按数组处理；单选模式只取首项。
const selected = computed<string[]>(() =>
  props.multiple
    ? [...(props.modelValue as string[])]
    : props.modelValue
      ? [props.modelValue as string]
      : [],
)

function commit(list: string[]) {
  emit('update:modelValue', props.multiple ? list : (list[0] ?? ''))
}

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return props.models
  return props.models.filter((m) => m.toLowerCase().includes(q))
})

function toggle(m: string) {
  const list = [...selected.value]
  const i = list.indexOf(m)
  if (i >= 0) {
    list.splice(i, 1)
  } else if (props.multiple) {
    list.push(m)
  } else {
    list.splice(0, list.length, m) // 单选：替换
  }
  commit(list)
}

function addCustom() {
  const name = search.value.trim()
  if (!name || selected.value.includes(name)) return
  if (props.multiple) commit([...selected.value, name])
  else emit('update:modelValue', name)
  search.value = ''
  open.value = false
}

// 回车直接添加（避免误触表单提交）。
function onEnter() {
  if (!search.value.trim()) return
  addCustom()
}

const triggerLabel = computed(() => {
  if (props.loading) return '加载模型中…'
  if (selected.value.length) {
    return props.multiple ? `已选 ${selected.value.length} 个模型` : selected.value[0]
  }
  return props.multiple ? '选择目标模型（可搜索 / 自定义）' : '选择目标模型'
})
</script>

<template>
  <div class="space-y-2">
    <Popover v-model:open="open">
      <PopoverTrigger as-child>
        <Button type="button" variant="outline" class="w-full justify-between font-normal">
          <span class="truncate text-muted-foreground">{{ triggerLabel }}</span>
          <RiSearchLine class="size-4 shrink-0 opacity-50" />
        </Button>
      </PopoverTrigger>
      <PopoverContent class="w-[480px] max-w-[calc(100vw-2rem)] p-2" align="start">
        <div class="space-y-2">
          <Input
            v-model="search"
            :placeholder="multiple ? '搜索模型…' : '搜索或选择模型…'"
            @keydown.esc="open = false"
            @keydown.enter.prevent="onEnter"
          />
          <div
            v-if="filtered.length"
            class="flex max-h-56 flex-wrap gap-1.5 overflow-y-auto rounded-md border border-border p-2"
          >
            <Button
              v-for="m in filtered"
              :key="m"
              type="button"
              size="sm"
              :variant="selected.includes(m) ? 'default' : 'outline'"
              @click="toggle(m)"
              >{{ m }}</Button
            >
          </div>
          <div v-else class="flex flex-col items-center gap-2 rounded-md border border-border p-3">
            <p class="text-xs text-muted-foreground">未找到「{{ search }}」</p>
            <Button
              v-if="allowCustom"
              type="button"
              size="sm"
              variant="outline"
              :disabled="!search.trim()"
              @click="addCustom"
              >自定义添加</Button
            >
            <p v-else class="text-xs text-muted-foreground">无匹配的候选模型</p>
          </div>
        </div>
      </PopoverContent>
    </Popover>
    <!-- 多选：已选模型徽标（含移除） -->
    <div v-if="multiple && selected.length" class="flex flex-wrap gap-1.5">
      <Badge v-for="m in selected" :key="m" variant="secondary" class="gap-1 py-0 pr-1">
        {{ m }}
        <button
          type="button"
          class="rounded-full p-0.5 hover:bg-muted hover:text-destructive"
          aria-label="移除"
          @click="toggle(m)"
        >
          <RiCloseLine class="size-3" />
        </button>
      </Badge>
    </div>
  </div>
</template>
