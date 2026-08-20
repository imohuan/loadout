<script setup lang="ts">
import { reactive, watch } from 'vue'
import ModelChannelList from '@/components/ModelChannelList.vue'
import type { Aggregate, Channel } from '@/lib/types'

const props = defineProps<{
  aggregate?: Aggregate
  channels: Channel[]
  pending?: boolean
  // 是否为复制：只影响标题；复制预填 targets + 新名字，按「添加」流程保存。
  duplicate?: boolean
}>()
const emit = defineEmits<{ save: [value: Aggregate]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })

const form = reactive<Aggregate>({
  name: '',
  enabled: true,
  targets: [{ model: '', channel_id: '', channel_ids: [] }],
})

watch(
  () => props.aggregate,
  (aggregate) => {
    Object.assign(form, {
      name: aggregate?.name || '',
      enabled: aggregate?.enabled ?? true,
      targets: aggregate?.targets?.map((target) => ({ ...target })) || [
        { model: '', channel_id: '', channel_ids: [] },
      ],
    })
  },
  { immediate: true },
)

function submit() {
  if (!form.name) return
  // 目标有效：模型 + 三种粒度至少一种渠道形态（渠道级 / Key 多选 / 单 Key 兼容）。
  const targets = form.targets.filter(
    (t) =>
      t.model &&
      (t.channel_id || t.channel_ids?.length || t.channel_base_url),
  )
  if (targets.length) emit('save', { name: form.name, enabled: form.enabled, targets })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>
          {{ aggregate ? (duplicate ? '复制聚合模型' : '编辑聚合模型') : '添加聚合模型' }}
        </DialogTitle>
        <DialogDescription>按从上到下的顺序依次尝试固定的模型和渠道组合。</DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="max-w-xl space-y-2">
          <Label for="aggregate-name">虚拟模型名</Label>
          <Input
            id="aggregate-name"
            v-model="form.name"
            :disabled="Boolean(aggregate) && !duplicate"
            required
            placeholder="auto-demo"
          />
        </div>
        <div class="space-y-2">
          <Label>目标列表</Label>
          <ModelChannelList
            v-model="form.targets"
            :channels="channels"
            :allow-auto-channel="false"
            :allow-custom-model="false"
            :require-channel-for-model="true"
            :clear-model-on-channel-change="true"
            :show-move="true"
            :show-index="true"
            add-label="添加目标"
          />
        </div>
        <DialogFooter>
          <Button type="submit" :disabled="pending">{{
            pending ? '正在保存' : '保存聚合模型'
          }}</Button>
          <Button type="button" variant="outline" :disabled="pending" @click="open = false"
            >取消</Button
          >
        </DialogFooter>
      </form>
    </DialogContent>
  </Dialog>
</template>
