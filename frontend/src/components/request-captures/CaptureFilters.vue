<script setup lang="ts">
import { reactive } from 'vue'
import { RiFilter3Line, RiLoader4Line, RiRefreshLine } from '@remixicon/vue'
import type { CaptureFilters } from '@/composables/useRequestCaptures'

/**
 * 抓包面板的筛选行：请求 ID / 请求模型 / 开始时间 / 结束时间。
 *
 * 「请求 ID」是抓包最有用的入口——通常从某条转发日志拿到 Request ID，再到这里
 * 反查当时的原始报文。另外两个时间条件与「转发日志」页保持同一操作习惯。
 *
 * 渠道与输出模型不在这里筛选：抓包是入口快照，那两个值不在捕获文件里，
 * 需要时用请求日志和路由日志两张表聚合查询（请求 ID 可关联）。
 */
defineProps<{ isPending?: (key: string) => boolean }>()
const emit = defineEmits<{ apply: [filters: CaptureFilters]; reset: [] }>()
const form = reactive<CaptureFilters>({})

function submit() {
  emit('apply', { ...form })
}
function reset() {
  Object.keys(form).forEach((key) => delete form[key as keyof CaptureFilters])
  emit('reset')
}
</script>

<template>
  <form class="flex flex-wrap items-end gap-4" @submit.prevent="submit">
    <div class="min-w-44 flex-1 space-y-1">
      <Label for="capture-request-id">请求 ID</Label>
      <Input
        id="capture-request-id"
        v-model="form.request_id"
        placeholder="与转发日志里的 Request ID 一致"
      />
    </div>
    <div class="min-w-44 flex-1 space-y-1">
      <Label for="capture-model">请求模型</Label>
      <Input id="capture-model" v-model="form.model" placeholder="客户端请求的模型" />
    </div>
    <div class="min-w-56 flex-1 space-y-1">
      <Label for="capture-from">开始时间</Label>
      <Input id="capture-from" v-model="form.from" type="datetime-local" />
    </div>
    <div class="min-w-56 flex-1 space-y-1">
      <Label for="capture-to">结束时间</Label>
      <Input id="capture-to" v-model="form.to" type="datetime-local" />
    </div>
    <div class="flex shrink-0 items-center gap-2">
      <Button type="submit" :disabled="isPending?.('apply')">
        <RiLoader4Line v-if="isPending?.('apply')" class="animate-spin" size="16" />
        <RiFilter3Line v-else size="16" />
        筛选
      </Button>
      <Button type="button" variant="outline" aria-label="重置筛选" @click="reset">
        <RiRefreshLine size="16" />
      </Button>
    </div>
  </form>
</template>

