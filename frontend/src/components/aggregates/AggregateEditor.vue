<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { RiAddLine, RiCheckLine, RiDeleteBinLine, RiSearchLine } from '@remixicon/vue'
import type { Aggregate, AggregateTarget, Channel } from '@/lib/types'

const props = defineProps<{ aggregate?: Aggregate; channels: Channel[]; pending?: boolean }>()
const emit = defineEmits<{ save: [value: Aggregate]; cancel: [] }>()
const open = defineModel<boolean>('open', { required: true })

const form = reactive<Aggregate>({ name: '', enabled: true, targets: [{ model: '', channel_id: '' }] })
const popoverOpen = reactive<boolean[]>([false])

watch(
  () => props.aggregate,
  (aggregate) => {
    Object.assign(form, {
      name: aggregate?.name || '',
      enabled: aggregate?.enabled ?? true,
      targets: aggregate?.targets?.map((target) => ({ ...target })) || [
        { model: '', channel_id: '' },
      ],
    })
    popoverOpen.length = 0
    form.targets.forEach(() => popoverOpen.push(false))
  },
  { immediate: true },
)

function modelsFor(channelId: string): string[] {
  const ch = props.channels.find((c) => c.id === channelId)
  return ch?.models ?? []
}

function targetError(target: AggregateTarget): string {
  if (!target.channel_id) return '请选择渠道'
  const ch = props.channels.find((c) => c.id === target.channel_id)
  if (!ch) return '渠道不存在'
  if (!ch.models || ch.models.length === 0) return `渠道「${ch.name}」尚未探测模型，请先在渠道管理刷新`
  if (!target.model) return '请输入或选择模型'
  if (!ch.models.includes(target.model)) return `模型「${target.model}」不在渠道「${ch.name}」的模型目录中`
  return ''
}

const errors = computed(() => form.targets.map((t) => targetError(t)))
const allValid = computed(() => errors.value.every((e) => !e))

function addTarget() {
  form.targets.push({ model: '', channel_id: '' })
  popoverOpen.push(false)
}
function removeTarget(index: number) {
  if (form.targets.length > 1) {
    form.targets.splice(index, 1)
    popoverOpen.splice(index, 1)
  }
}
function changeTarget(index: number, key: keyof AggregateTarget, value: string) {
  form.targets[index]![key] = value
  // 切换渠道后清空模型：旧模型可能不属于新渠道。
  if (key === 'channel_id') {
    form.targets[index]!.model = ''
  }
}
function selectModel(index: number, model: string) {
  form.targets[index]!.model = model
  popoverOpen[index] = false
}
function submit() {
  if (!allValid.value) return
  const targets = form.targets.filter((t) => t.model && t.channel_id)
  if (form.name && targets.length) emit('save', { name: form.name, enabled: form.enabled, targets })
}
</script>

<template>
  <Dialog v-model:open="open">
    <DialogContent class="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl!">
      <DialogHeader>
        <DialogTitle>{{ aggregate ? '编辑聚合模型' : '添加聚合模型' }}</DialogTitle>
        <DialogDescription>按从上到下的顺序依次尝试固定的模型和渠道组合。</DialogDescription>
      </DialogHeader>
      <form class="space-y-4" @submit.prevent="submit">
        <div class="max-w-xl space-y-2">
          <Label for="aggregate-name">虚拟模型名</Label>
          <Input
            id="aggregate-name"
            v-model="form.name"
            :disabled="Boolean(aggregate)"
            required
            placeholder="auto-demo"
          />
        </div>
        <div class="space-y-2">
          <Label>目标列表</Label>
          <div
            v-for="(target, index) in form.targets"
            :key="index"
            class="space-y-1"
          >
            <div class="grid items-start gap-2 sm:grid-cols-[3rem_minmax(0,1fr)_minmax(0,1fr)_auto]">
              <span
                class="flex h-9 items-center justify-center text-sm tabular-nums text-muted-foreground"
                >{{ index + 1 }}</span
              >
              <div class="flex gap-1">
                <Input
                  class="flex-1"
                  :model-value="target.model"
                  placeholder="真实模型名（可手输或点右侧搜索）"
                  :class="errors[index] && 'border-destructive focus-visible:ring-destructive'"
                  @update:model-value="changeTarget(index, 'model', String($event))"
                />
                <Popover v-model:open="popoverOpen[index]">
                  <PopoverTrigger as-child>
                    <Button
                      type="button"
                      variant="outline"
                      size="icon"
                      :disabled="!target.channel_id"
                      aria-label="从渠道模型目录选择"
                    >
                      <RiSearchLine class="size-4" />
                    </Button>
                  </PopoverTrigger>
                  <PopoverContent class="w-80 p-0" align="end">
                    <Command>
                      <CommandInput placeholder="过滤该渠道的模型…" />
                      <CommandList>
                        <CommandEmpty>该渠道暂无可用模型</CommandEmpty>
                        <CommandGroup>
                          <CommandItem
                            v-for="m in modelsFor(target.channel_id)"
                            :key="m"
                            :value="m"
                            @select="selectModel(index, m)"
                          >
                            <RiCheckLine
                              :class="target.model === m ? 'opacity-100' : 'opacity-0'"
                              class="mr-2 size-4"
                            />
                            <span class="truncate">{{ m }}</span>
                          </CommandItem>
                        </CommandGroup>
                      </CommandList>
                    </Command>
                  </PopoverContent>
                </Popover>
              </div>
              <Select
                :model-value="target.channel_id"
                @update:model-value="changeTarget(index, 'channel_id', String($event))"
              >
                <SelectTrigger>
                  <SelectValue placeholder="选择渠道" />
                </SelectTrigger>
                <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                  <SelectGroup>
                    <SelectItem v-for="channel in channels" :key="channel.id" :value="channel.id">
                      {{ channel.name }}
                    </SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
              <TooltipProvider
                ><Tooltip
                  ><TooltipTrigger as-child
                    ><Button
                      variant="ghost"
                      size="icon"
                      type="button"
                      :disabled="form.targets.length === 1"
                      aria-label="移除目标"
                      @click="removeTarget(index)"
                      ><RiDeleteBinLine size="16" /></Button></TooltipTrigger
                  ><TooltipContent>移除目标</TooltipContent></Tooltip
                ></TooltipProvider
              >
            </div>
            <p
              v-if="errors[index]"
              class="pl-[calc(3rem+0.5rem)] text-xs text-destructive"
            >
              {{ errors[index] }}
            </p>
          </div>
          <Button type="button" variant="outline" size="sm" @click="addTarget"
            ><RiAddLine size="16" />添加目标</Button
          >
        </div>
        <DialogFooter>
          <Button type="submit" :disabled="pending || !allValid">{{
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
