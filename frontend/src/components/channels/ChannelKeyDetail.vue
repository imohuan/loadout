<script setup lang="ts">
// 状态二右栏：选中 Key 的详情与操作区。
//
// 原 ChannelTable 展开行里那些按钮全部在这里保留：启用/停用、刷新模型、编辑、
// 上移/下移、删除 Key，以及平台层面的同步模型 / 刷新全部 / 整组优先级 / 添加 Key /
// 删除整组（放在平台信息卡上，因为它们是整组操作）。
import { computed } from 'vue'
import {
  RiAddLine,
  RiArrowDownLine,
  RiArrowUpLine,
  RiDeleteBinLine,
  RiEditLine,
  RiExchangeLine,
  RiLoader4Line,
  RiRefreshLine,
} from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { channelEnabled, channelModelLabel, type ChannelPlatform } from '@/lib/channels'

const props = defineProps<{
  platform: ChannelPlatform
  /** 选中的 Key；平台下没有 Key 时为 undefined。 */
  channel?: Channel
  /** 该 Key 在组内的下标（0 起），用于上移/下移禁用判断。 */
  keyIndex: number
  /** 该平台在全部平台中的下标与总数，用于整组上移/下移禁用判断。 */
  groupIndex: number
  groupCount: number
  isPending?: (key: string) => boolean
}>()

const emit = defineEmits<{
  addKey: [baseUrl: string]
  toggleKey: [key: Channel]
  refreshKey: [key: Channel]
  editKey: [key: Channel]
  moveKey: [key: Channel, direction: 'up' | 'down']
  removeKey: [key: Channel]
  syncModels: [baseUrl: string]
  refreshGroup: [baseUrl: string]
  moveGroup: [baseUrl: string, direction: 'up' | 'down']
  removeGroup: [baseUrl: string]
}>()

// 操作 key：与 ChannelsView.run() 的 key 规则必须完全一致，按钮级 loading/禁用才对得上。
function groupKey(baseUrl: string, action: string) {
  return `group:${baseUrl}:${action}`
}
function keyKey(channel: Channel, action: string) {
  return `key:${channel.id}:${action}`
}
function busy(key: string) {
  return props.isPending ? props.isPending(key) : false
}

/** 平台位置：整组上移/下移由外部按全量列表判断，这里只给出是否可点。 */
const canMoveUp = computed(() => props.keyIndex > 0)
const canMoveDown = computed(() => props.keyIndex < props.platform.keyCount - 1)
const canMoveGroupUp = computed(() => props.groupIndex > 0)
const canMoveGroupDown = computed(() => props.groupIndex < props.groupCount - 1)
</script>

<template>
  <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <!-- 平台操作条：整组级动作（同步模型 / 刷新全部 / 整组优先级 / 添加 / 删除）。 -->
    <div class="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2 text-sm">
      <span class="text-xs text-muted-foreground">
        {{ platform.keyCount }} 个 Key · {{ platform.enabledKeyCount }} 个启用 ·
        {{ platform.modelCount }} 个模型
      </span>
      <span class="flex-1" />
      <Button
        variant="outline"
        size="sm"
        :disabled="busy(groupKey(platform.baseUrl, 'sync-models'))"
        title="把一份模型列表同步到本平台的其他 Key"
        @click="emit('syncModels', platform.baseUrl)"
      >
        <RiLoader4Line
          v-if="busy(groupKey(platform.baseUrl, 'sync-models'))"
          class="animate-spin"
          size="14"
        />
        <RiExchangeLine v-else size="14" />同步模型
      </Button>
      <Button
        variant="outline"
        size="sm"
        :disabled="busy(groupKey(platform.baseUrl, 'refresh'))"
        @click="emit('refreshGroup', platform.baseUrl)"
      >
        <RiLoader4Line
          v-if="busy(groupKey(platform.baseUrl, 'refresh'))"
          class="animate-spin"
          size="14"
        />
        <RiRefreshLine v-else size="14" />刷新全部模型
      </Button>
      <Button
        variant="outline"
        size="sm"
        :disabled="busy(groupKey(platform.baseUrl, 'move-up')) || !canMoveGroupUp"
        aria-label="提高整组优先级"
        title="提高整组优先级"
        @click="emit('moveGroup', platform.baseUrl, 'up')"
      >
        <RiArrowUpLine size="14" />
      </Button>
      <Button
        variant="outline"
        size="sm"
        :disabled="busy(groupKey(platform.baseUrl, 'move-down')) || !canMoveGroupDown"
        aria-label="降低整组优先级"
        title="降低整组优先级"
        @click="emit('moveGroup', platform.baseUrl, 'down')"
      >
        <RiArrowDownLine size="14" />
      </Button>
      <Button variant="outline" size="sm" @click="emit('addKey', platform.baseUrl)">
        <RiAddLine size="14" />添加 Key
      </Button>
      <Button
        variant="outline"
        size="sm"
        :disabled="busy(groupKey(platform.baseUrl, 'remove'))"
        @click="emit('removeGroup', platform.baseUrl)"
      >
        <RiLoader4Line
          v-if="busy(groupKey(platform.baseUrl, 'remove'))"
          class="animate-spin"
          size="14"
        />
        <RiDeleteBinLine v-else size="14" />删除整组
      </Button>
    </div>

    <template v-if="channel">
      <!-- Key 头部：名称、启用状态、模型数。 -->
      <div class="flex flex-wrap items-center gap-3 border-b border-border px-3 py-2.5">
        <span class="text-sm font-semibold">{{ channel.name }}</span>
        <Badge :variant="channelEnabled(channel) ? 'default' : 'secondary'">{{
          channelEnabled(channel) ? '启用' : '禁用'
        }}</Badge>
        <span class="text-xs tabular-nums text-muted-foreground">{{
          channelModelLabel(channel)
        }}</span>
        <span
          class="ml-auto hidden min-w-0 truncate font-mono text-[11px] text-muted-foreground lg:block"
          :title="channel.base_url"
        >
          {{ channel.base_url }}
        </span>
      </div>

      <!-- Key 操作条：单个 Key 的动作。 -->
      <div class="flex flex-wrap items-center gap-2 border-b border-border bg-muted/30 px-3 py-2">
        <Button
          variant="outline"
          size="sm"
          :disabled="busy(keyKey(channel, 'toggle'))"
          @click="emit('toggleKey', channel)"
        >
          <RiLoader4Line v-if="busy(keyKey(channel, 'toggle'))" class="animate-spin" size="14" />
          {{ channelEnabled(channel) ? '停用' : '启用' }}
        </Button>
        <Button
          variant="outline"
          size="sm"
          :disabled="busy(keyKey(channel, 'refresh'))"
          @click="emit('refreshKey', channel)"
        >
          <RiLoader4Line v-if="busy(keyKey(channel, 'refresh'))" class="animate-spin" size="14" />
          <RiRefreshLine v-else size="14" />刷新模型
        </Button>
        <Button variant="outline" size="sm" @click="emit('editKey', channel)">
          <RiEditLine size="14" />编辑
        </Button>
        <span class="mx-0.5 h-5 w-px bg-border" />
        <Button
          variant="outline"
          size="sm"
          aria-label="上移 Key"
          :disabled="busy(keyKey(channel, 'move-up')) || !canMoveUp"
          @click="emit('moveKey', channel, 'up')"
        >
          <RiArrowUpLine size="14" />
        </Button>
        <Button
          variant="outline"
          size="sm"
          aria-label="下移 Key"
          :disabled="busy(keyKey(channel, 'move-down')) || !canMoveDown"
          @click="emit('moveKey', channel, 'down')"
        >
          <RiArrowDownLine size="14" />
        </Button>
        <Button
          variant="outline"
          size="sm"
          :disabled="busy(keyKey(channel, 'remove'))"
          @click="emit('removeKey', channel)"
        >
          <RiLoader4Line v-if="busy(keyKey(channel, 'remove'))" class="animate-spin" size="14" />
          <RiDeleteBinLine v-else size="14" />删除 Key
        </Button>
      </div>

      <!-- 模型清单：只读展示该 Key 的模型目录（编辑走弹窗）。 -->
      <div class="min-h-0 flex-1 overflow-auto p-3">
        <div v-if="channel.models_detail?.length" class="flex flex-wrap gap-1.5">
          <span
            v-for="m in channel.models_detail"
            :channel="m.model"
            class="rounded border px-1.5 py-0.5 font-mono text-[11px]"
            :class="
              m.enabled
                ? 'border-border bg-muted text-foreground/80'
                : 'border-border bg-background text-muted-foreground line-through'
            "
            :title="m.model"
            >{{ m.model }}</span
          >
        </div>
        <div v-else-if="channel.models?.length" class="flex flex-wrap gap-1.5">
          <span
            v-for="m in channel.models"
            :channel="m"
            class="rounded border border-border bg-muted px-1.5 py-0.5 font-mono text-[11px] text-foreground/80"
            >{{ m }}</span
          >
        </div>
        <p v-else-if="channel.models_error" class="text-sm text-muted-foreground">
          模型探测失败：{{ channel.models_error }}
        </p>
        <p v-else class="text-sm text-muted-foreground">
          该 Key 暂未配置模型，点上方「编辑」添加。
        </p>
        <p class="mt-3 text-xs text-muted-foreground">
          划线 = 已从候选里取消勾选（不参与普通模型路由）。
        </p>
      </div>
    </template>

    <div v-else class="flex flex-1 items-center justify-center p-8">
      <p class="text-sm text-muted-foreground">该平台没有 Key，点上方「添加 Key」创建。</p>
    </div>
  </div>
</template>
