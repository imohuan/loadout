<script setup lang="ts">
// 状态二左栏：平台下拉 + 该平台下的 Key 列表。
//
// 顶部是平台下拉：鼠标移上去就展开平台菜单（换平台是次要动作，不该再多吃一次点击），
// 同时保留点击展开与 Esc 收起，键盘与触屏也能用。下面接该平台的 Key 列表，
// 点一行就把右栏切到那个 Key。
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { RiArrowDownSLine } from '@remixicon/vue'
import type { ChannelStatus } from '@/lib/types'
import type { PlatformSummary } from '@/lib/modelStatus'
import { availableModelCount, keyStatusBrief } from '@/lib/modelStatus'

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

// 平台下拉的展开态。
//
// 主要交互是 hover（鼠标移到触发条上就展开），但 hover 不能是唯一入口：
// 键盘用户与触屏设备没有 hover，开关也就无从触发。所以这里同时维护一个
// 点击态，并且在指针离开整块区域时统一收起，避免菜单留在屏幕上。
const open = ref(false)

function toggleOpen() {
  open.value = !open.value
}

function closeMenu() {
  open.value = false
}

function pickPlatform(baseUrl: string) {
  closeMenu()
  emit('selectPlatform', baseUrl)
}

// Esc 关闭：菜单展开时按 Esc 收起，是下拉菜单的通用预期。
function onKeydown(e: KeyboardEvent) {
  if (e.key === 'Escape' && open.value) {
    e.stopPropagation()
    closeMenu()
  }
}
onMounted(() => window.addEventListener('keydown', onKeydown))
onBeforeUnmount(() => window.removeEventListener('keydown', onKeydown))
</script>

<template>
  <div class="flex min-h-0 shrink-0 flex-col border-border md:w-64! md:border-r!">
    <!-- 平台下拉：hover 即展开（鼠标移上来就能看），点击也能切换；Esc 收起。 -->
    <div class="group/dd relative border-b border-border p-2" @mouseleave="closeMenu">
      <button
        type="button"
        class="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left text-sm"
        aria-label="切换平台"
        :aria-expanded="open"
        @mouseenter="open = true"
        @click="toggleOpen"
      >
        <span
          class="size-2 shrink-0 rounded-full"
          :class="TONE_DOT[activePlatform?.tone || 'ok']"
        />
        <span class="min-w-0 flex-1 truncate font-medium">{{ activePlatform?.name }}</span>
        <span class="shrink-0 text-xs text-muted-foreground tabular-nums">
          {{ activePlatform?.keyCount }} Key
        </span>
        <RiArrowDownSLine size="16" class="shrink-0 text-muted-foreground" />
      </button>

      <div
        class="absolute inset-x-2 top-11 z-30 max-h-80 overflow-auto rounded-md border border-border bg-popover p-1 shadow-lg"
        :class="
          open ? 'visible' : 'invisible opacity-0 group-hover/dd:visible group-hover/dd:opacity-100'
        "
      >
        <button
          v-for="p in platforms"
          :key="p.baseUrl"
          type="button"
          class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-muted"
          :class="p.baseUrl === activeBaseUrl ? 'bg-muted font-medium' : ''"
          @click="pickPlatform(p.baseUrl)"
        >
          <span class="size-2 shrink-0 rounded-full" :class="TONE_DOT[p.tone]" />
          <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
          <span class="shrink-0 text-xs text-muted-foreground tabular-nums">
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
            <!--
              短标签而不是完整徽标：左栏只有 16rem，「账号已禁用（需手动恢复）」这种
              完整文案会把整行占满。成因、恢复时间与规则跳转在右栏头部展示。
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
