<template>
  <div class="w-full overflow-hidden">
    <div @click="handleToggle"
      :class="['w-full group inline-flex items-center gap-1.5 text-xs text-gray-500 py-0.5 transition-colors select-none', disabled ? 'cursor-default' : 'hover:text-gray-900 cursor-pointer']">
      <slot name="icon">
        <svg v-if="!hideIcon" class="w-3.5 h-3.5 text-gray-400 group-hover:text-gray-500 shrink-0" fill="none" stroke="currentColor"
          viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
            d="M8 9l3 3-3 3m5 0h3M5 20h14a2 2 0 002-2V6a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z" />
        </svg>
      </slot>
      <slot name="title" :open="open">
        <span class="truncate text-xs font-mix">{{ open ? expandedTitle : collapsedTitle }}</span>
      </slot>
      <svg v-show="!disabled" :class="[open ? '' : '-rotate-90']"
        class="w-3 h-3 transition-all duration-300 text-gray-400 shrink-0 opacity-0 group-hover:opacity-100" fill="none"
        stroke="currentColor" viewBox="0 0 24 24">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
      </svg>
    </div>
    <div class="overflow-hidden transition-[height] duration-300 ease-out" :style="contentStyle">
      <div ref="innerRef">
        <slot />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, watch, onMounted, onBeforeUnmount, nextTick } from "vue";

const props = defineProps({
  collapsedTitle: { type: String, default: "" },
  expandedTitle: { type: String, default: "" },
  open: { type: Boolean, default: false },
  disabled: { type: Boolean, default: false },
  hideIcon: { type: Boolean, default: false },
});

const emit = defineEmits(["update:open"]);

function handleToggle() {
  if (props.disabled) return;
  emit("update:open", !props.open);
}
const innerRef = ref<HTMLElement | null>(null);
const height = ref(0);

function updateHeight() {
  const el = innerRef.value;
  if (el) height.value = el.offsetHeight;
}

const contentStyle = computed(() => {
  if (!props.open) return { height: "0px" };
  return { height: height.value + "px" };
});

let ro: ResizeObserver | null = null;
onMounted(() => {
  updateHeight();
  if (typeof ResizeObserver !== "undefined" && innerRef.value) {
    ro = new ResizeObserver(() => {
      if (props.open) updateHeight();
    });
    ro.observe(innerRef.value);
  }
});

onBeforeUnmount(() => {
  if (ro) {
    ro.disconnect();
    ro = null;
  }
});

watch(() => props.open, async (isOpen) => {
  if (isOpen) {
    await nextTick();
    updateHeight();
  }
});
</script>
