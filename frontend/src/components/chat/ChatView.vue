<template>
  <div ref="scrollRef" class="flex-1 overflow-y-auto relative">
    <div
      v-if="!props.messages.length"
      class="flex items-center justify-center h-full text-sm text-muted-foreground"
    >
      {{ emptyText }}
    </div>
    <div v-else class="max-w-3xl mx-auto px-6" :class="props.dense ? 'py-2' : 'py-6'">
      <div class="space-y-2">
        <template v-for="(group, gIdx) in groups" :key="gIdx">
          <UserMessage v-if="group.type === 'user'" :message="group.message" />

          <div v-else-if="group.type === 'ai'" class="space-y-2">
            <template v-for="block in group.blocks" :key="blockKey(block)">
              <template v-if="block.type === 'tool-group' && block.tools.length === 1">
                <ToolCallCard :tool-call="block.tools[0]" :key="block.tools[0].id" />
              </template>
              <CollapsibleBlock
                v-else-if="block.type === 'tool-group'"
                :collapsed-title="block.collapsedTitle"
                :expanded-title="block.expandedTitle"
                :open="isBlockOpen('tg-' + block.firstToolId)"
                @update:open="toggleBlock('tg-' + block.firstToolId)"
              >
                <div class="space-y-1 pt-0.5">
                  <ToolCallCard
                    v-for="tool in block.tools"
                    :key="tool.id"
                    :tool-call="tool"
                  />
                </div>
              </CollapsibleBlock>

              <TextBlock v-else-if="block.type === 'text-block'" :message="block.message" />
              <ThinkingBlock v-else-if="block.type === 'thinking' && block.message.content" :message="block.message" />
            </template>
          </div>
        </template>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue';
import UserMessage from './UserMessage.vue';
import TextBlock from './TextBlock.vue';
import ThinkingBlock from './ThinkingBlock.vue';
import CollapsibleBlock from './CollapsibleBlock.vue';
import ToolCallCard from './ToolCallCard.vue';
import { computeGroups, type Block } from '@/composables/useGroups';
import type { NormalizedMessage } from '@/lib/chatTypes';

/**
 * 纯展示对话容器（去掉 store 依赖的 ChatView 改造版）。
 * 数据流：父组件把 NormalizedMessage[] 传进来，内部 computeGroups 分组渲染。
 * 滚动跟随：messages 变化且容器贴底时自动滚到底部。
 */
const props = withDefaults(
  defineProps<{
    messages: NormalizedMessage[];
    emptyText?: string;
    /** dense 模式：内容区内边距从 py-6 缩为 py-2，适用于嵌套在卡片内的紧凑场景 */
    dense?: boolean;
  }>(),
  { emptyText: '暂无对话内容。', dense: false },
);

const scrollRef = ref<HTMLElement | null>(null);

// 折叠状态本地管理（等价 useUiStore.isBlockOpen/toggleBlock）
const blockOpenState = ref<Record<string, boolean>>({});
function isBlockOpen(key: string): boolean {
  return blockOpenState.value[key] ?? false;
}
function toggleBlock(key: string) {
  blockOpenState.value[key] = !isBlockOpen(key);
}

const groups = computed(() => computeGroups(props.messages));

// 消息变化且用户停在底部附近时自动滚到底部
watch(
  () => props.messages.length,
  async () => {
    await nextTick();
    const el = scrollRef.value;
    if (!el) return;
    const nearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 200;
    if (nearBottom) {
      el.scrollTop = el.scrollHeight;
    }
  },
);

function blockKey(block: Block): string {
  if (block.type === 'tool-group') return `tool-group-${block.firstToolId}`;
  if (block.type === 'text-block') return `text-${block.message.id || block.message.itemId || Math.random()}`;
  return `think-${block.message.id || block.message.itemId || Math.random()}`;
}
</script>
