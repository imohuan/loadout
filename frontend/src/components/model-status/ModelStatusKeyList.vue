<script setup lang="ts">
// 状态二左栏：平台下拉 + 该平台下的 Key 列表。
//
// 顶部是平台下拉：鼠标移上去就展开平台菜单（换平台是次要动作，不该再多吃一次点击），
// 同时保留点击展开与 Esc 收起，键盘与触屏也能用。下面接该平台的 Key 列表，
// 点一行就把右栏切到那个 Key。
import { computed, onMounted, ref } from 'vue'
import { Popover, PopoverContent, PopoverTrigger } from 'shadcn-vue-cdn'
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

// 平台下拉的展开态。
//
// 主要交互是 hover（鼠标移到触发条上就展开），但 hover 不能是唯一入口：
// 键盘用户与触屏设备没有 hover，开关也就无从触发。所以这里同时维护一个
// 点击态，并且在指针离开整块区域时统一收起，避免菜单留在屏幕上。
/**
 * 菜单显隐（单一事实源）。
 *
 * shadcn Popover 用 v-model:open 受控：hover / 点击 / Esc 都改这个值，
 * PopoverContent 跟着显示或隐藏，触发条与菜单的定位、偏移交给 reka-ui。
 */
const open = ref(false)

/**
 * hover 武装标志：进入「平台详情」时鼠标大概率正停在触发条的位置上（刚点完平台
 * tag，视图切换后触发条出现在指针下方），浏览器会立刻派发一次 mouseenter ——
 * 不能让这次「假 hover」把菜单弹开。因此挂载后先不武装，等指针真正离开过
 * 一次（mouseleave）再允许 hover 展开；点击展开不受影响。
 */
const armed = ref(false)

function onEnter() {
  if (!armed.value) return
  open.value = true
}

function onLeave() {
  armed.value = true
  open.value = false
}

function pickPlatform(baseUrl: string) {
  onLeave()
  emit('selectPlatform', baseUrl)
}

// Esc 关闭由 Popover 自带（Escape 关闭 + 焦点回触发条），无需手动监听。
onMounted(() => {
  // 组件挂载 = 刚从平台总览切进来，重置武装状态（hover 武装注释所述）。
  armed.value = false
})
</script>

<template>
  <div class="flex min-h-0 shrink-0 flex-col border-border md:w-64! md:border-r!">
    <!--
      平台下拉：hover 即展开（鼠标移上来就能看），点击也能切换；选完/离开/Esc 都收起。
      渲染走 shadcn 的 Popover（与渠道编辑器、模型测试页同一套组件）：定位、偏移、
      碰撞翻转都交给 reka-ui，不再手写 absolute。
      菜单不必跟着触发条等宽：平台名可能很长，给 20rem 宽、align=start。
    -->
    <Popover v-model:open="open">
      <div
        class="group/dd relative border-b border-border p-2"
        @mouseenter="onEnter"
        @mouseleave="onLeave"
      >
        <PopoverTrigger as-child>
          <button
            type="button"
            class="flex w-full items-center gap-2 rounded-md border border-border bg-background px-2.5 py-2 text-left text-sm"
            aria-label="切换平台"
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
        </PopoverTrigger>
      </div>

      <PopoverContent
        class="w-[20rem] max-w-[calc(100vw-6rem)] p-1"
        align="start"
        :side-offset="6"
        :on-open-auto-focus="(e: Event) => e.preventDefault()"
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
      </PopoverContent>
    </Popover>

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
