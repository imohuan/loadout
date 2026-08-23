<template>
  <div class="code-block group relative my-3 rounded-lg bg-zinc-100/70 overflow-hidden text-[13px]">
    <div class="flex items-center justify-between px-4 pt-2 pb-1">
      <span class="text-[11px] text-zinc-500 font-mono">{{ lang || 'text' }}</span>
      <button
        type="button"
        class="p-1 rounded text-zinc-400 hover:text-zinc-700 transition-opacity"
        :class="copied ? 'opacity-100 text-green-600' : 'opacity-0 group-hover:opacity-100'"
        :title="copied ? '已复制' : '复制'"
        @click="onCopy"
      >
        <svg v-if="!copied" class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2"
            d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
        </svg>
        <svg v-else class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
          <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
        </svg>
      </button>
    </div>
    <pre class="overflow-x-auto px-4 pb-3 m-0 font-mono leading-relaxed"><code v-html="highlighted"></code></pre>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue';
import hljs from 'highlight.js/lib/common'

const props = defineProps<{
  code: string;
  lang?: string;
}>();

const copied = ref(false);

const highlighted = computed(() => {
  const language = props.lang && hljs.getLanguage(props.lang) ? props.lang : 'plaintext';
  return hljs.highlight(props.code, { language }).value;
});

async function onCopy() {
  try {
    if (navigator.clipboard && window.isSecureContext) {
      await navigator.clipboard.writeText(props.code);
    } else {
      const ta = document.createElement('textarea');
      ta.value = props.code;
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
    }
    copied.value = true;
    setTimeout(() => { copied.value = false; }, 1500);
  } catch (e) {
    console.error('copy failed', e);
  }
}
</script>