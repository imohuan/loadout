<script setup lang="ts">
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  Button,
} from 'shadcn-vue-cdn'
import { useConfirm } from '@/composables/useConfirm'

const { state, resolve } = useConfirm()

function onOpenChange(value: boolean) {
  // Esc / 点击遮罩关闭时视为取消
  if (!value) resolve(false)
}
</script>

<template>
  <AlertDialog :open="state.open" @update:open="onOpenChange">
    <AlertDialogContent>
      <AlertDialogHeader>
        <AlertDialogTitle>{{ state.options.title }}</AlertDialogTitle>
        <AlertDialogDescription v-if="state.options.description">
          {{ state.options.description }}
        </AlertDialogDescription>
      </AlertDialogHeader>
      <AlertDialogFooter>
        <Button variant="outline" @click="resolve(false)">{{ state.options.cancelText }}</Button>
        <Button
          :variant="state.options.destructive ? 'destructive' : 'default'"
          @click="resolve(true)"
        >
          {{ state.options.confirmText }}
        </Button>
      </AlertDialogFooter>
    </AlertDialogContent>
  </AlertDialog>
</template>
