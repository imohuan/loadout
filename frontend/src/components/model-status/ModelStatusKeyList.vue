<script setup lang="ts">
// 状态二左栏：平台下拉 + 状态筛选 + 该平台下的 Key 列表。
//
// 顶部是平台下拉：点击展开平台菜单，再点一次收起；点选平台后菜单立即关闭，
// 点外部或 Esc 也会收起。下面接状态筛选标签与 Key 列表，点一行就把右栏切到那个 Key。
import { computed } from 'vue'
import { RiArrowDownSLine } from '@remixicon/vue'
import type { ChannelStatus } from '@/lib/types'
import type { KeyTestState, PlatformSummary } from '@/lib/modelStatus'
import {
  availableModelCount,
  filterKeysByGroup,
  keyEnabledState,
  keyGroup,
  keyStatusBrief,
  KEY_GROUP_LABEL,
  KEY_GROUP_ORDER,
  PLATFORM_TONE_LABEL,
  TONE_STYLE,
  TONE_STYLE_SOFT,
  TONE_STYLE_STRONG,
  type KeyGroup,
  type KeyTone,
} from '@/lib/modelStatus'

const props = defineProps<{
  platforms: PlatformSummary[]
  activeBaseUrl: string
  activeKeyId?: string
  isPending?: (key: string) => boolean
  /** 批量测试的实时进度：channel.id → 状态。没测过的 Key 不在表里。 */
  testStates?: Record<string, KeyTestState>
  /**
   * 当前生效的状态筛选标签。
   *
   * 由父组件持有：批量测试要按「列表里实际显示的这些 Key」来跑，父组件必须
   * 拿到同一个筛选值，否则会出现「界面写测试 10 项、实际跑了 17 个」。
   */
  groupFilter: 'all' | KeyGroup
}>()

const emit = defineEmits<{
  selectPlatform: [baseUrl: string]
  selectKey: [key: ChannelStatus]
  'update:groupFilter': [value: 'all' | KeyGroup]
}>()

const activePlatform = computed(
  () => props.platforms.find((p) => p.baseUrl === props.activeBaseUrl) || props.platforms[0],
)

/** 当前生效的筛选标签（父组件持有，这里只读）。 */
const groupFilter = computed(() => props.groupFilter)

/** 各分组的数量，给筛选标签上的角标用。 */
const groupCounts = computed(() => {
  const counts: Record<KeyGroup, number> = { ok: 0, manual: 0, issue: 0, bad: 0 }
  for (const key of activePlatform.value?.keys || []) counts[keyGroup(key)] += 1
  return counts
})

/**
 * 状态筛选标签的语义色。
 *
 * 与列表里那行短标签共用同一份色板（TONE_STYLE）：同一个「可用」，筛选键与短标签
 * 必须是同一个绿。之前这里另抄了一份 TAB_CLASS/TAB_ACTIVE_CLASS，与 ENABLED_CLASS、
 * BRIEF_CLASS 三张表各写各的，改一处就会漏掉另两处。
 */
function tabClasses(strong: boolean): Record<'all' | KeyGroup, string> {
  const palette = strong ? TONE_STYLE_STRONG : TONE_STYLE
  return {
    all: strong
      ? 'border-foreground/40 bg-muted font-semibold text-foreground'
      : 'border-border bg-muted text-muted-foreground',
    ok: palette.ok,
    manual: palette.off,
    issue: palette.warn,
    bad: palette.bad,
  }
}

const TAB_CLASS = tabClasses(false)

/** 选中态：在语义色底上加描边与加粗，而不是换成主色 —— 换掉就看不出状态色了。 */
const TAB_ACTIVE_CLASS = tabClasses(true)

/** 筛选标签：全部 + 四个分组，数量为 0 的分组不渲染（省得挤一行空标签）。 */
const filterTabs = computed(() => {
  const tabs: Array<{ value: 'all' | KeyGroup; label: string; count: number }> = [
    { value: 'all', label: '全部', count: activePlatform.value?.keys.length || 0 },
  ]
  for (const group of KEY_GROUP_ORDER) {
    const count = groupCounts.value[group]
    if (count > 0) tabs.push({ value: group, label: KEY_GROUP_LABEL[group], count })
  }
  return tabs
})

/** 列表最终渲染的 Key：可用 → 手动关闭 → 异常 → 不可用，再按标签过滤。 */
const visibleKeys = computed(() =>
  filterKeysByGroup(activePlatform.value?.keys || [], groupFilter.value),
)

/** 切换平台时重置筛选：上一个平台的「异常」标签在新平台可能一个 Key 都不剩。 */
function pickPlatform(baseUrl: string) {
  emit('update:groupFilter', 'all')
  emit('selectPlatform', baseUrl)
}

function testState(key: ChannelStatus): KeyTestState | undefined {
  return props.testStates?.[key.channel.id]
}

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
  ok: TONE_STYLE_SOFT.ok,
  off: TONE_STYLE_SOFT.off,
}

/** 左栏一行短标签的配色：与 PlatformTagGrid 的语义色同源。 */
const BRIEF_CLASS: Record<KeyTone, string> = TONE_STYLE_SOFT

function modelSummary(key: ChannelStatus) {
  return `${availableModelCount(key)} / ${key.models.length} 模型可用`
}
</script>

<template>
  <div class="flex min-h-0 shrink-0 flex-col border-border md:w-64! md:border-r!">
    <!--
      平台下拉：用 shadcn 的 DropdownMenu 渲染，动画（fade + zoom + slide）、
      定位、碰撞翻转、Esc/点外部收起都由 reka-ui 负责，与其他下拉同源。
      选完即收：DropdownMenuItem 选中后 reka 自动关闭。
      菜单不跟随触发条等宽（平台名可能很长），给 20rem 固定宽、超出视口由
      max-w 兜底；行内有圆点 + 平台名 + 计数三列，用 -mx-1 抵消 item 默认内边距。
    -->
    <div class="border-b border-border p-2">
      <DropdownMenu>
        <DropdownMenuTrigger as-child>
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
            <!--
              数字统一按「Key」这一个口径：可用 Key / 总 Key。
              之前这里是「17 Key」（Key 总数），下拉里却是「10/10」（可用模型/模型总数），
              两个数单位不同，看起来就像「总数把禁用的 Key 抹掉了」。
            -->
            <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
              {{ activePlatform?.availableKeyCount }}/{{ activePlatform?.keyCount }} Key
            </span>
            <RiArrowDownSLine size="16" class="shrink-0 text-muted-foreground" />
          </button>
        </DropdownMenuTrigger>
        <DropdownMenuContent
          align="start"
          :side-offset="6"
          class="max-h-80 w-[20rem] max-w-[calc(100vw-6rem)]"
        >
          <DropdownMenuItem
            v-for="p in platforms"
            :key="p.baseUrl"
            class="-mx-1 gap-2 px-2 py-1.5"
            :class="p.baseUrl === activeBaseUrl ? 'bg-accent font-medium' : ''"
            @select="pickPlatform(p.baseUrl)"
          >
            <span
              class="size-2 shrink-0 rounded-full"
              :class="TONE_DOT[p.tone]"
              :title="toneTitle(p.tone)"
            />
            <span class="min-w-0 flex-1 truncate">{{ p.name }}</span>
            <span class="shrink-0 text-xs tabular-nums text-muted-foreground">
              {{ p.availableKeyCount }}/{{ p.keyCount }} Key
            </span>
          </DropdownMenuItem>
          <p v-if="!platforms.length" class="px-2 py-3 text-xs text-muted-foreground">
            没有可切换的平台
          </p>
        </DropdownMenuContent>
      </DropdownMenu>
    </div>

    <!--
      状态筛选标签：点一下只看该状态的 Key。
      左对齐、带状态色，数量为 0 的分组不渲染。
    -->
    <div
      v-if="filterTabs.length > 1"
      class="flex flex-wrap items-center gap-1 border-b border-border px-2 py-1.5"
    >
      <button
        v-for="tab in filterTabs"
        :key="tab.value"
        type="button"
        class="rounded border px-1.5 py-0.5 text-[11px] font-medium transition-colors"
        :class="
          groupFilter === tab.value
            ? TAB_ACTIVE_CLASS[tab.value]
            : `${TAB_CLASS[tab.value]} hover:border-foreground/30`
        "
        @click="emit('update:groupFilter', tab.value)"
      >
        {{ tab.label }}
        <span class="tabular-nums opacity-70">{{ tab.count }}</span>
      </button>
    </div>

    <!--
      Key 列表：一行一个 Key，选中项高亮。

      这里不做拖拽排序：显示顺序是按状态分组排过的（可用 → 手动关闭 → 异常 →
      不可用），不是「渠道与模型」里的真实路由顺序。在这份重排过的列表上再拖，
      要么得把结果换算回库序（跨组拖拽还会被分组弹回），要么就得放弃状态分组。
      排序统一在「渠道与模型」页做，两边共用同一份 channels.key_position。
    -->
    <div class="max-h-72 min-h-0 flex-1 overflow-auto p-1.5 md:max-h-none!">
      <button
        v-for="key in visibleKeys"
        :key="key.channel.id"
        type="button"
        class="flex w-full items-center gap-2 rounded-md px-2 py-2 text-left transition-colors"
        :class="key.channel.id === activeKeyId ? 'bg-muted' : 'hover:bg-muted/60'"
        @click="emit('selectKey', key)"
      >
        <span class="min-w-0 flex-1" :title="key.channel.name">
          <!-- Key 名单独占一行：与状态标签同行时，标签会把名字挤成省略号。 -->
          <span
            class="block truncate text-sm"
            :class="
              key.channel.id === activeKeyId
                ? 'font-semibold text-foreground'
                : 'font-medium text-foreground'
            "
            >{{ key.channel.name }}</span
          >
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

          <!--
            测试进度行：正在测 / 测试通过 / 测试失败三种状态各占一行，测完都保留，
            直到下一次测试才清掉。
            失败信息给足宽度换行显示，比挤成一行省略号更有用 —— 上游错误原文
            通常就是「额度已用尽」「账号已禁用」这类关键判断，截断了等于没给。
          -->
          <span v-if="testState(key)" class="mt-1 block">
            <template v-if="testState(key)?.phase === 'running'">
              <span class="flex items-center gap-1.5">
                <span
                  class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium bg-sky-500/15 text-sky-700 dark:text-sky-300"
                >
                  正在执行中
                </span>
                <span class="min-w-0 truncate font-mono text-[10px] text-muted-foreground">
                  {{ testState(key)?.model }}
                </span>
              </span>
            </template>
            <template v-else-if="testState(key)?.phase === 'ok'">
              <span class="flex flex-wrap items-center gap-1.5">
                <span
                  class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium bg-emerald-500/15 text-emerald-700 dark:text-emerald-300"
                >
                  测试通过
                </span>
                <span class="min-w-0 truncate font-mono text-[10px] text-muted-foreground">
                  {{ testState(key)?.model }}
                </span>
              </span>
            </template>
            <template v-else-if="testState(key)?.phase === 'failed'">
              <span class="flex flex-wrap items-center gap-1.5">
                <span
                  class="shrink-0 rounded px-1 py-0.5 text-[10px] font-semibold bg-red-500/20 text-red-700 ring-1 ring-red-500/40 dark:text-red-300"
                >
                  测试失败
                </span>
                <span class="min-w-0 font-mono text-[10px] text-muted-foreground">
                  {{ testState(key)?.model }}
                </span>
              </span>
              <span
                class="mt-0.5 block text-[10px] leading-snug text-red-700/90 dark:text-red-300/90"
              >
                {{ testState(key)?.error }}
              </span>
            </template>
          </span>
        </span>
      </button>
      <p v-if="!visibleKeys.length" class="px-2 py-3 text-sm text-muted-foreground">
        {{ activePlatform?.keys.length ? '该状态下没有 Key' : '该平台暂无 Key' }}
      </p>
    </div>
  </div>
</template>
