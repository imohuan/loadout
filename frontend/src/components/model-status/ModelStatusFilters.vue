<script setup lang="ts">
import { reactive } from 'vue'
import { RiFilter3Line, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
import type { ModelStatusFilters } from '@/composables/useModelStatus'

const props = defineProps<{ isPending?: (key: string) => boolean }>()
const emit = defineEmits<{ apply: [filters: ModelStatusFilters]; reset: [] }>()
const form = reactive<ModelStatusFilters>({})

function busy(key: string) {
  return props.isPending ? props.isPending(key) : false
}
function submit() {
  emit('apply', { ...form })
}
function reset() {
  form.model = undefined
  form.manual_enabled = undefined
  form.status = undefined
  emit('reset')
}
</script>

<template>
  <form class="flex flex-wrap items-end gap-4" @submit.prevent="submit">
    <div class="min-w-128 space-y-1">
      <Label for="ms-model">模型</Label>
      <Input id="ms-model" v-model="form.model" placeholder="搜索模型（支持 * 通配符，如 deepseek*）" />
    </div>
    <div class="min-w-24 space-y-1">
      <Label for="ms-enabled">开关</Label>
      <Select :model-value="form.manual_enabled ?? '__all__'" @update:model-value="
        form.manual_enabled = $event === '__all__' ? undefined : $event === 'true'
        ">
        <SelectTrigger id="ms-enabled" class="w-full">
          <SelectValue placeholder="全部开关" />
        </SelectTrigger>
        <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
          <SelectGroup>
            <SelectItem value="__all__">全部开关</SelectItem>
            <SelectItem value="true">已开启</SelectItem>
            <SelectItem value="false">已关闭</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
    <div class="min-w-24 space-y-1">
      <Label for="ms-status">状态</Label>
      <Select :model-value="form.status || '__all__'"
        @update:model-value="form.status = $event === '__all__' ? undefined : String($event)">
        <SelectTrigger id="ms-status" class="w-full">
          <SelectValue placeholder="全部状态" />
        </SelectTrigger>
        <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
          <SelectGroup>
            <SelectItem value="__all__">全部状态</SelectItem>
            <SelectItem value="available">可用</SelectItem>
            <SelectItem value="cooling">冷却中</SelectItem>
            <SelectItem value="disabled">不可用</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <Button type="submit" :disabled="busy('filter')">
        <RiLoader4Line v-if="busy('filter')" class="animate-spin" size="16" /><RiFilter3Line v-else size="16" />筛选
      </Button>
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger as-child><Button type="button" variant="outline" aria-label="重置筛选" @click="reset">
              <RiRefreshLine size="16" />
            </Button></TooltipTrigger>
          <TooltipContent>重置筛选</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  </form>
</template>
