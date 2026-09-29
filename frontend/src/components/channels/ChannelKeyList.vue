<script setup lang="ts">
// 状态二左栏：平台下拉 + 该平台的 Key 列表。
//
// 平台下拉是纯点击交互（点开、再点收起、选完即收、点外部或 Esc 也收起）。
// 曾尝试用 shadcn Popover 的 Trigger 模式，但它的 click 会与 DismissableLayer 的
// outside 判定相互干扰（关闭态下点不开），故沿用模型状态页验证过的手写定位方案：
// 菜单挂在触发条容器内、top = 触发条底边 + 6px、宽 20rem 不跟随触发条等宽。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RiArrowDownSLine } from '@remixicon/vue'
import type { Channel } from '@/lib/types'
import { channelEnabled, channelModelLabel, type ChannelPlatform } from '@/lib/channels'

const props = defineProps<{
  platforms: ChannelPlatform[]
  activeBaseUrl: string
  activeKeyId?: string
}>()

const emit = defineEmits<{
  selectPlatform: [baseUrl: string]
  selectKey: [key: Channel]
}>()

const activePlatform = computed(
  () => props.platforms.find((p) => p.baseUrl === props.activeBaseUrl) || props.platforms[0],
)

/** 菜单显隐（单一事实源）。 */
const open = ref(false)
const triggerRef = ref<HTMLElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)

function toggleOpen() {
  open.value = !open.value
}

function pickPlatform(baseUrl: string) {
  open.value = false
  emit('selectPlatform', baseUrl)
}

/**
 * 点在菜单外则收起。
 *
 * 延迟到当前事件循环结束后再关：pointerdown 与「打开菜单的 click」属于同一次
 * 物理点击（浏览器先派发 pointerdown 再派发 click），同步关闭会把刚打开的菜单
 * 瞬间关掉，表现为「点了没反应」。
 */
function onDocumentPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node
  if (triggerRef.value?.contains(target) || menuRef.value?.contains(target)) return
  setTimeout(() => {
    open.value = false
  }, 0)
}

function onDocumentKeydown(event: KeyboardEvent) {
  if (event.key === 'Escape' && open.value) {
    event.stopPropagation()
    open.value = false
  }
}

onMounted(() => {
  document.addEventListener('pointerdown', onDocumentPointerDown, true)
  document.addEventListener('keydown', onDocumentKeydown)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', onDocumentPointerDown, true)
  document.removeEventListener('keydown', onDocumentKeydown)
})
</script>

<template>
  <div class="flex min-h-0 shrink-0 flex-col border-border md:w-64! md:border-r!">
    <!-- 平台下拉：点击展开 / 再点收起，选完即收，点外部或 Esc 也会收起。 -->
    <div ref="triggerRef" class="relative border-b border-border p-2">
      <button
        type="button"
        class="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left text-sm"
        aria-label="切换平台"
        :aria-expanded="open"
        aria-haspopup="dialog"
        @click="toggleOpen"
      >
        <span class="min-w-0 flex-1 truncate font-medium">{{ activePlatform?.name }}</span>
        <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
          {{ activePlatform?.keyCount }} Key
        </span>
        <RiArrowDownSLine size="16" class="shrink-0 text-muted-foreground" />
      </button>

      <div
        v-if="open"
        ref="menuRef"
        class="absolute left-2 top-[calc(100%-8px+6px)] z-30 max-h-80 w-[20rem] max-w-[calc(100vw-6rem)] overflow-auto rounded-md border border-border bg-popover p-1 shadow-lg"
      >
        <button
          v-for="p in platforms"
          :key="p.baseUrl"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-muted"
          :class="p.baseUrl === activeBaseUrl ? 'bg-muted font-medium' : ''"
          @click="pickPlatform(p.baseUrl)"
        >
          <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
          <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
            {{ p.enabledKeyCount }}/{{ p.keyCount }}
          </span>
        </button>
        <p v-if="!platforms.length" class="px-2 py-3 text-xs text-muted-foreground">
          没有可切换的平台
        </p>
      </div>
    </div>

    <!-- Key 列表：一行一个 Key，选中项高亮。 -->
    <div class="min-h-0 flex-1 overflow-auto p-1.5">
      <button
        v-for="key in activePlatform?.keys || []"
        :key="key.id"
        type="button"
        class="flex w-full items-center gap-2 rounded-md border border-transparent px-2 py-2 text-left transition-colors hover:border-border hover:bg-background"
        :class="key.id === activeKeyId ? 'border-border bg-background' : ''"
        @click="emit('selectKey', key)"
      >
        <span class="min-w-0 flex-1" :title="key.name">
          <span class="block truncate text-sm font-medium">{{ key.name }}</span>
          <span class="mt-0.5 block truncate text-[11px] text-muted-foreground">{{
            channelModelLabel(key)
          }}</span>
        </span>
        <span
          class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium"
          :class="
            channelEnabled(key)
              ? 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300'
              : 'bg-slate-500/15 text-slate-700 dark:text-slate-300'
          "
        >
          {{ channelEnabled(key) ? '启用' : '禁用' }}
        </span>
      </button>
      <p v-if="!activePlatform?.keys.length" class="px-2 py-3 text-sm text-muted-foreground">
        该平台暂无 Key
      </p>
    </div>
  </div>
</template>
