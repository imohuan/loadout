<script lang="ts" setup>
import type { ToasterProps } from 'vue-sonner'
import { reactiveOmit } from '@vueuse/core'
import { Toaster as Sonner } from 'vue-sonner'
import {
  RiAlertLine,
  RiCheckboxCircleLine,
  RiCloseCircleLine,
  RiCloseLine,
  RiInformationLine,
  RiLoader4Line,
} from '@remixicon/vue'

const props = defineProps<ToasterProps>()
const delegatedProps = reactiveOmit(props, 'class', 'toastOptions')
const toastOptions = {
  ...(props.toastOptions || {}),
  closeButton: true,
  closeButtonPosition: 'top-right' as const,
  classes: {
    ...(props.toastOptions?.classes || {}),
    toast: 'rounded-xl',
  },
}
</script>

<template>
  <Sonner
    v-bind="delegatedProps"
    :class="props.class ? `toaster group ${props.class}` : 'toaster group'"
    :toast-options="toastOptions"
    :style="{
      '--normal-bg': 'var(--popover)',
      '--normal-text': 'var(--popover-foreground)',
      '--normal-border': 'var(--border)',
      '--border-radius': 'var(--radius)',
      '--gray2': 'color-mix(in srgb, var(--popover) 90%, transparent)',
      '--gray3': 'var(--border)',
      '--gray4': 'var(--border)',
      '--gray5': 'var(--border)',
      '--gray12': 'var(--popover-foreground)',
    }"
  >
    <template #success-icon><RiCheckboxCircleLine class="size-4" /></template>
    <template #info-icon><RiInformationLine class="size-4" /></template>
    <template #warning-icon><RiAlertLine class="size-4" /></template>
    <template #error-icon><RiCloseCircleLine class="size-4" /></template>
    <template #loading-icon><RiLoader4Line class="size-4 animate-spin" /></template>
    <template #close-icon><RiCloseLine class="size-4" /></template>
  </Sonner>
</template>
