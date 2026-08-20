<script setup lang="ts">
import { ref, watch } from 'vue'
import {
  Checkbox,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Button,
} from 'shadcn-vue-cdn'
import { RiDownload2Line, RiLoaderLine, RiLockLine } from '@remixicon/vue'
import { toast } from 'vue-sonner'
import {
  exportConfig,
  transferSectionOptions,
  type TransferSectionKey,
} from '@/composables/useConfigTransfer'

const props = defineProps<{ open: boolean }>()
const emit = defineEmits<{ 'update:open': [value: boolean] }>()

const selected = ref<TransferSectionKey[]>(transferSectionOptions.map((opt) => opt.key))
const pending = ref(false)

watch(
  () => props.open,
  (value) => {
    // 每次打开恢复默认全选。
    if (value) selected.value = transferSectionOptions.map((opt) => opt.key)
  },
)

function toggle(key: TransferSectionKey) {
  const index = selected.value.indexOf(key)
  if (index >= 0) selected.value.splice(index, 1)
  else selected.value.push(key)
}

const allSelected = () => selected.value.length === transferSectionOptions.length

function toggleAll() {
  selected.value = allSelected() ? [] : transferSectionOptions.map((opt) => opt.key)
}

async function doExport() {
  if (!selected.value.length || pending.value) return
  pending.value = true
  try {
    await exportConfig(selected.value)
    toast.success('配置已导出', {
      description: `${selected.value.length} 类配置已打包为 zip 下载`,
    })
    emit('update:open', false)
  } catch (error) {
    toast.error(error instanceof Error ? error.message : '导出失败')
  } finally {
    pending.value = false
  }
}
</script>

<template>
  <Dialog :open="open" @update:open="emit('update:open', $event)">
    <DialogContent class="sm:max-w-lg!">
      <DialogHeader>
        <DialogTitle>导出配置</DialogTitle>
        <DialogDescription>
          选择要导出的配置，一键打包为 zip 下载，可迁移到其他 Loadout 实例。
        </DialogDescription>
      </DialogHeader>

      <div
        class="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-xs text-amber-700 dark:border-amber-500/30 dark:bg-amber-500/10 dark:text-amber-400"
      >
        <div class="flex items-start gap-2">
          <RiLockLine size="14" class="mt-0.5 shrink-0" />
          <span>
            导出文件包含<strong>渠道 API 密钥（明文）</strong>和 MCP
            请求头。请妥善保管，不要上传到公共仓库。
          </span>
        </div>
      </div>

      <div class="space-y-1">
        <div class="flex items-center justify-between">
          <span class="text-xs text-muted-foreground"
            >已选 {{ selected.length }} / {{ transferSectionOptions.length }} 类</span
          >
          <Button
            type="button"
            variant="ghost"
            size="sm"
            class="h-6 px-2 text-xs"
            @click="toggleAll"
          >
            {{ allSelected() ? '取消全选' : '全选' }}
          </Button>
        </div>
        <div class="divide-y divide-border rounded-md border border-border">
          <label
            v-for="opt in transferSectionOptions"
            :key="opt.key"
            class="flex cursor-pointer items-start gap-3 px-3 py-2.5 transition-colors hover:bg-muted/60"
          >
            <Checkbox
              class="mt-0.5"
              :model-value="selected.includes(opt.key)"
              @update:model-value="toggle(opt.key)"
            />
            <span class="min-w-0">
              <span class="block text-sm font-medium">{{ opt.label }}</span>
              <span class="block text-xs text-muted-foreground">{{ opt.description }}</span>
            </span>
          </label>
        </div>
      </div>

      <DialogFooter>
        <Button variant="outline" @click="emit('update:open', false)">取消</Button>
        <Button :disabled="!selected.length || pending" @click="doExport">
          <RiLoaderLine v-if="pending" size="16" class="animate-spin" />
          <RiDownload2Line v-else size="16" />
          导出 zip
        </Button>
      </DialogFooter>
    </DialogContent>
  </Dialog>
</template>
