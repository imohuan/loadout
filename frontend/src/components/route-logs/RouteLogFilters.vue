<script setup lang="ts">
import { reactive, watch } from 'vue'
import { RiFilter3Line, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import type { RouteLogFilters } from '@/composables/useRouteLogs'

const props = defineProps<{ channels: Channel[]; isPending?: (key: string) => boolean }>()
const emit = defineEmits<{ apply: [filters: RouteLogFilters]; reset: [] }>()
const form = reactive<RouteLogFilters>({})
watch(
  () => props.channels,
  () => undefined,
)
function busy(key: string) {
  return props.isPending ? props.isPending(key) : false
}
function submit() {
  emit('apply', { ...form })
}
function reset() {
  Object.keys(form).forEach((key) => delete form[key as keyof RouteLogFilters])
  emit('reset')
}
</script>

<template>
  <form class="flex flex-wrap items-end gap-4" @submit.prevent="submit">
    <div class="min-w-52 flex-[2_1_18rem] space-y-1">
      <Label for="log-model">模型</Label>
      <Input id="log-model" v-model="form.model" placeholder="请求模型" />
    </div>
    <div class="min-w-36 flex-1 space-y-1">
      <Label for="log-channel">渠道</Label>
      <Select :model-value="form.channel_id || '__all__'" @update:model-value="
        form.channel_id = $event === '__all__' ? undefined : String($event)
        ">
        <SelectTrigger id="log-channel" class="w-full">
          <SelectValue placeholder="所有渠道" />
        </SelectTrigger>
        <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
          <SelectGroup>
            <SelectItem value="__all__">所有渠道</SelectItem>
            <SelectItem v-for="channel in channels" :key="channel.id" :value="channel.id">
              {{ channel.name }}
            </SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
    <div class="min-w-36 flex-1 space-y-1">
      <Label for="log-result">结果</Label>
      <Select :model-value="form.result || '__all__'"
        @update:model-value="form.result = $event === '__all__' ? undefined : String($event)">
        <SelectTrigger id="log-result" class="w-full">
          <SelectValue placeholder="所有结果" />
        </SelectTrigger>
        <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
          <SelectGroup>
            <SelectItem value="__all__">所有结果</SelectItem>
            <SelectItem value="success">成功</SelectItem>
            <SelectItem value="failed">失败</SelectItem>
            <SelectItem value="running">进行中</SelectItem>
          </SelectGroup>
        </SelectContent>
      </Select>
    </div>
    <div class="min-w-60 flex-[2_1_18rem] space-y-1">
      <Label for="log-from">开始时间</Label>
      <Input id="log-from" v-model="form.from" type="datetime-local" />
    </div>
    <div class="min-w-60 flex-[2_1_18rem] space-y-1">
      <Label for="log-to">结束时间</Label>
      <Input id="log-to" v-model="form.to" type="datetime-local" />
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <Button type="submit" :disabled="busy('apply')">
        <RiLoader4Line v-if="busy('apply')" class="animate-spin" size="16" /><RiFilter3Line v-else size="16" />筛选
      </Button>
      <TooltipProvider>
        <Tooltip>
          <TooltipTrigger as-child><Button type="button" variant="outline" :disabled="busy('apply')" aria-label="重置筛选"
              @click="reset">
              <RiRefreshLine size="16" />
            </Button></TooltipTrigger>
          <TooltipContent>重置筛选</TooltipContent>
        </Tooltip>
      </TooltipProvider>
    </div>
  </form>
</template>
