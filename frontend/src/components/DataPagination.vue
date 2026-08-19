<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { RiArrowLeftLine, RiArrowRightLine, RiSkipLeftLine, RiSkipRightLine } from '@remixicon/vue'

/**
 * 通用分页栏：页码按钮（带省略号）+ 每页条数选择 + 跳页输入框 + 总条数/总页数。
 * 纯受控组件，配合 v-model:page / v-model:pageSize 使用。
 */
const props = withDefaults(
  defineProps<{
    total: number
    page: number
    pageSize: number
    pageSizes?: number[]
    disabled?: boolean
  }>(),
  { pageSizes: () => [10, 20, 50, 100], disabled: false },
)
const emit = defineEmits<{
  'update:page': [value: number]
  'update:pageSize': [value: number]
}>()

const pageCount = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
// props 只读，v-model 需要计算属性中转
const currentPage = computed({
  get: () => props.page,
  set: (value: number) => emit('update:page', value),
})
const jump = ref('')

// 总量/每页条数变化导致当前页越界时自动纠正
watch(pageCount, (count) => {
  if (props.page > count) emit('update:page', count)
})
// 切换每页条数回到第一页
watch(
  () => props.pageSize,
  () => emit('update:page', 1),
)

function goTo(value: number) {
  emit('update:page', Math.min(Math.max(1, value), pageCount.value))
}
function submitJump() {
  const n = Number.parseInt(jump.value, 10)
  if (Number.isNaN(n)) {
    jump.value = ''
    return
  }
  goTo(n)
  jump.value = ''
}
</script>

<template>
  <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
    <p class="text-sm text-muted-foreground">
      共 <span class="font-medium tabular-nums text-foreground">{{ total }}</span> 条，第
      <span class="font-medium tabular-nums text-foreground">{{ page }}</span>
      / <span class="tabular-nums">{{ pageCount }}</span> 页
    </p>

    <div class="flex flex-wrap items-center gap-x-4 gap-y-2">
      <!-- 每页条数 -->
      <div class="flex items-center gap-1.5">
        <span class="text-sm text-muted-foreground">每页</span>
        <Select
          :model-value="String(pageSize)"
          :disabled="disabled"
          @update:model-value="emit('update:pageSize', Number($event))"
        >
          <SelectTrigger class="h-8 w-[72px]" aria-label="每页条数">
            <SelectValue />
          </SelectTrigger>
          <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
            <SelectGroup>
              <SelectItem v-for="size in pageSizes" :key="size" :value="String(size)">
                {{ size }}
              </SelectItem>
            </SelectGroup>
          </SelectContent>
        </Select>
        <span class="text-sm text-muted-foreground">条</span>
      </div>

      <!-- 页码按钮 -->
      <Pagination
        v-model:page="currentPage"
        :total="total"
        :items-per-page="pageSize"
        :disabled="disabled"
        class="mx-0 w-auto flex-none"
      >
        <PaginationContent v-slot="{ items }">
          <PaginationItem>
            <PaginationFirst as-child>
              <Button
                variant="ghost"
                size="icon"
                :disabled="page === 1 || disabled"
                aria-label="首页"
              >
                <RiSkipLeftLine />
              </Button>
            </PaginationFirst>
          </PaginationItem>
          <PaginationItem>
            <PaginationPrevious as-child>
              <Button
                variant="ghost"
                size="icon"
                :disabled="page === 1 || disabled"
                aria-label="上一页"
              >
                <RiArrowLeftLine />
              </Button>
            </PaginationPrevious>
          </PaginationItem>
          <template v-for="(item, index) in items" :key="index">
            <PaginationItem
              v-if="item.type === 'page'"
              :value="item.value"
              :is-active="item.value === page"
            >
              {{ item.value }}
            </PaginationItem>
            <PaginationEllipsis v-else />
          </template>
          <PaginationItem>
            <PaginationNext as-child>
              <Button
                variant="ghost"
                size="icon"
                :disabled="page === pageCount || disabled"
                aria-label="下一页"
              >
                <RiArrowRightLine />
              </Button>
            </PaginationNext>
          </PaginationItem>
          <PaginationItem>
            <PaginationLast as-child>
              <Button
                variant="ghost"
                size="icon"
                :disabled="page === pageCount || disabled"
                aria-label="末页"
              >
                <RiSkipRightLine />
              </Button>
            </PaginationLast>
          </PaginationItem>
        </PaginationContent>
      </Pagination>

      <!-- 跳页输入框 -->
      <div class="flex items-center gap-1.5">
        <span class="text-sm text-muted-foreground">跳至</span>
        <Input
          v-model="jump"
          type="number"
          min="1"
          :max="pageCount"
          class="h-8 w-16 text-center"
          :disabled="disabled"
          aria-label="跳转到指定页"
          @keydown.enter="submitJump"
          @blur="submitJump"
        />
        <span class="text-sm text-muted-foreground">页</span>
      </div>
    </div>
  </div>
</template>
