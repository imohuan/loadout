<template>
  <CollapsibleBlock v-model:open="open" collapsed-title="思考过程" expanded-title="思考过程" hide-icon>
    <div class="pl-2 border-l-2 border-gray-200 whitespace-pre-wrap" v-text="message.content"></div>
  </CollapsibleBlock>
</template>

<script setup lang="ts">
import { ref } from 'vue';
import type { NormalizedMessage } from '@/lib/chatTypes';
import CollapsibleBlock from './CollapsibleBlock.vue';

defineProps<{
  message: NormalizedMessage;
}>();

const open = ref(true);
</script>
