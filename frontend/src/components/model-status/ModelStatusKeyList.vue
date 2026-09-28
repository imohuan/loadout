<script setup lang="ts">
// 状态二左栏：平台下拉 + 该平台下的 Key 列表。
//
// 顶部是平台下拉：点击展开平台菜单，再点一次收起；点选平台后菜单立即关闭，
// 点外部或 Esc 也会收起。下面接该平台的 Key 列表，点一行就把右栏切到那个 Key。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RiArrowDownSLine } from '@remixicon/vue'
import type { ChannelStatus } from '@/lib/types'
import type { PlatformSummary } from '@/lib/modelStatus'
import {
  availableModelCount,
  keyEnabledState,
  keyStatusBrief,
  PLATFORM_TONE_LABEL,
} from '@/lib/modelStatus'

const props = defineProps<{
  platforms: PlatformSummary[]
  activeBaseUrl: string
  activeKeyId?: string
  isPending?: (key: string) => boolean
}>()

const emit = defineEmits<{
  selectPlatform: [baseUrl: string]
  selectKey: [key: ChannelStatus]
}>()

const activePlatform = computed(
  () => props.platforms.find((p) => p.baseUrl === props.activeBaseUrl) || props.platforms[0],
)

const TONE_DOT: Record<string, string> = {
  ok: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-red-500',
  off: 'bg-slate-400',
}

/** 圆点的悬停解释：黄=部分异常、灰=手动关闭……与 PLATFORM_TONE_LABEL 同源。 */
function toneTitle(tone?: string) {
  return PLATFORM_TONE_LABEL[(tone || 'ok') as keyof typeof PLATFORM_TONE_LABEL]
}

/** 「开启 / 关闭」二元徽标的配色：开启=绿、关闭=灰。故障成因用另一枚彩色短标签。 */
const ENABLED_CLASS: Record<string, string> = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  off: 'bg-slate-500/15 text-slate-700 dark:text-slate-300',
}

/** 左栏一行短标签的配色，与 PlatformTagGrid 的语义色一致。 */
const BRIEF_CLASS: Record<string, string> = {
  ok: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-300',
  warn: 'bg-amber-500/15 text-amber-700 dark:text-amber-300',
  bad: 'bg-red-500/15 text-red-700 dark:text-red-300',
  off: 'bg-slate-500/15 text-slate-700 dark:text-slate-300',
}

function modelSummary(key: ChannelStatus) {
  return `${availableModelCount(key)} / ${key.models.length} 模型可用`
}

/** 菜单显隐（单一事实源）。 */
const open = ref(false)
const triggerRef = ref<HTMLElement | null>(null)
const menuRef = ref<HTMLElement | null>(null)
function toggleOpen() {
  open.value = !open.value
}

function pickPlatform(baseUrl: string) {
  // 选完即收：点选平台的动作已经完成，菜单没有继续挂着的理由。
  open.value = false
  emit('selectPlatform', baseUrl)
}

function onDocumentPointerDown(event: PointerEvent) {
  if (!open.value) return
  const target = event.target as Node
  // 点在触发条或菜单内部不算外部（toggle 交给按钮自己的 click）。
  if (triggerRef.value?.contains(target) || menuRef.value?.contains(target)) return
  // 延迟到当前事件循环结束后再关：pointerdown 可能与「打开菜单的 click」同属
  // 一次物理点击（浏览器先派发 pointerdown 再派发 click），同步关闭会把刚打开
  // 的菜单瞬间关掉，表现为「点了没反应」。
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
    <!--
      平台下拉：点击展开 / 再点收起，选完即收，点外部或 Esc 也会收起。
      曾尝试 shadcn Popover 的 Trigger/Anchor 模式：Trigger 的 click 与
      DismissableLayer 的 outside 判定相互干扰（关闭态下点不开），最终回退到
      手写定位 —— 菜单挂在触发条容器内，top = 触发条底边 + 6px 间距，
      宽 20rem 不随触发条等宽（平台名可能很长），超出视口由 max-w 兜底。
    -->
    <div ref="triggerRef" class="relative border-b border-border p-2">
      <button
        type="button"
        class="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left text-sm"
        aria-label="切换平台"
        :aria-expanded="open"
        aria-haspopup="dialog"
        @click="toggleOpen"
      >
        <span
          class="size-2 shrink-0 rounded-full"
          :class="TONE_DOT[activePlatform?.tone || 'ok']"
          :title="toneTitle(activePlatform?.tone)"
        />
        <span class="min-w-0 flex-1 truncate font-medium">{{ activePlatform?.name }}</span>
        <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
          {{ activePlatform?.keyCount }} Key
        </span>
        <RiArrowDownSLine size="16" class="shrink-0 text-muted-foreground" />
      </button>

      <div
        v-if="open"
        ref="menuRef"
        class="absolute left-2 top-[calc(100%-8px+6px)] z-30 w-[20rem] max-w-[calc(100vw-6rem)] max-h-80 overflow-auto rounded-md border border-border bg-popover p-1 shadow-lg"
      >
        <button
          v-for="p in platforms"
          :key="p.baseUrl"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-muted"
          :class="p.baseUrl === activeBaseUrl ? 'bg-muted font-medium' : ''"
          @click="pickPlatform(p.baseUrl)"
        >
          <span
            class="size-2 shrink-0 rounded-full"
            :class="TONE_DOT[p.tone]"
            :title="toneTitle(p.tone)"
          />
          <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
          <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
            {{ p.availableModelCount }}/{{ p.modelCount }}
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
        :key="key.channel.id"
        type="button"
        class="flex w-full items-center gap-2 rounded-md border border-transparent px-2 py-2 text-left transition-colors hover:border-border hover:bg-background"
        :class="key.channel.id === activeKeyId ? 'border-border bg-background' : ''"
        @click="emit('selectKey', key)"
      >
        <span class="min-w-0 flex-1" :title="key.channel.name">
          <!-- Key 名单独占一行：与状态标签同行时，标签会把名字挤成省略号。 -->
          <span class="block truncate text-sm font-medium">{{ key.channel.name }}</span>
          <span class="mt-0.5 flex items-center gap-1.5">
            <!-- 开 / 关：只回答「这个 Key 现在能不能用」，与故障成因分开。 -->
            <span
              class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium"
              :class="ENABLED_CLASS[keyEnabledState(key).tone]"
            >
              {{ keyEnabledState(key).label }}
            </span>
            <!--
              故障成因短标签：关闭时才出现，解释「为什么关闭」。左栏只有 16rem，
              「账号已禁用（需手动恢复）」这种完整文案会把整行占满，成因、恢复时间
              与规则跳转在右栏头部展示。
            -->
            <span
              v-if="keyStatusBrief(key).label !== '可用'"
              class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium"
              :class="BRIEF_CLASS[keyStatusBrief(key).tone]"
            >
              {{ keyStatusBrief(key).label }}
            </span>
            <span class="min-w-0 truncate text-[11px] tabular-nums text-muted-foreground">
              {{ modelSummary(key) }}
            </span>
          </span>
        </span>
      </button>
      <p v-if="!activePlatform?.keys.length" class="px-2 py-3 text-sm text-muted-foreground">
        该平台暂无 Key
      </p>
    </div>
  </div>
</template>
