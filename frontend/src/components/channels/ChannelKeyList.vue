<script setup lang="ts">
// 状态二左栏：平台下拉 + 该平台的 Key 列表。
//
// 平台下拉是纯点击交互（点开、再点收起、选完即收、点外部或 Esc 也收起）。
// 曾尝试用 shadcn Popover 的 Trigger 模式，但它的 click 会与 DismissableLayer 的
// outside 判定相互干扰（关闭态下点不开），故沿用模型状态页验证过的手写定位方案：
// 菜单挂在触发条容器内、top = 触发条底边 + 6px、宽 20rem 不跟随触发条等宽。
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RiArrowDownSLine, RiCloseLine, RiSearchLine } from '@remixicon/vue'
import { VueDraggable } from 'vue-draggable-plus'
import type { Channel, ChannelStatus } from '@/lib/types'
import { channelEnabled, channelModelLabel, type ChannelPlatform } from '@/lib/channels'
import { keyStatusBrief, TONE_STYLE_SOFT } from '@/lib/modelStatus'

const props = defineProps<{
  platforms: ChannelPlatform[]
  activeBaseUrl: string
  activeKeyId?: string
  /** Key id → 健康状态；缺省时退回只显示启用状态。 */
  statusById?: Map<string, ChannelStatus>
}>()

const emit = defineEmits<{
  selectPlatform: [baseUrl: string]
  selectKey: [key: Channel]
  reorder: [baseUrl: string, keys: Channel[]]
}>()

const activePlatform = computed(
  () => props.platforms.find((p) => p.baseUrl === props.activeBaseUrl) || props.platforms[0],
)

/**
 * Key 搜索词：只按名字过滤左栏列表。
 *
 * 搜索框固定在列表上方（不在滚动容器里），滚到列表底下也还能改搜索词。
 * 过滤只影响「显示哪些行」，拖拽排序仍以整组为准：拖完把完整顺序回传给页面，
 * 不会因为只显示了一半的 Key 就把没显示的那些挤出列表。
 */
const keyword = ref('')
const searching = computed(() => keyword.value.trim().length > 0)
function matchesKeyword(key: Channel) {
  const q = keyword.value.trim().toLowerCase()
  if (!q) return true
  return [key.name, key.channel_name || ''].some((text) => text.toLowerCase().includes(q))
}

/** 左栏一行的状态：优先显示健康状态，没有状态数据时退回启用/禁用。 */
function keyStatus(key: Channel) {
  const status = props.statusById?.get(key.id)
  if (status) return keyStatusBrief(status)
  return channelEnabled(key)
    ? { label: '启用', tone: 'ok' as const }
    : { label: '禁用', tone: 'off' as const }
}

// 组内拖拽排序（vue-draggable-plus）：只渲染当前平台这一组 Key，拖拽列表用本地
// 副本承载内部移动，松手后把新顺序回传给页面。
// 跨组拖拽有两道闸：1) 这里只存在当前组的一个列表，且不声明 Sortable group
// （未声明 group 的列表不接收也不外送元素），切换平台时靠 key 重建实例；
// 2) 页面侧再校验 id 集合与 base_url 必须都属于当前组。
// 排序只走拖拽：右栏原有的上移/下移按钮已移除，避免两套排序入口互相打架。
//
// 整卡可拖（没有独立手柄）：不设 handle、不设 delay，按下即可拖。
// 不要加 delay——delay 期间一旦鼠标移动就会被判成"移动过"而取消拖拽，
// 实际手感变成"必须先停住等一下才能拖"，非常难用。
//
// forceFallback 是必须的：Chrome 默认走原生 HTML5 拖拽，而原生拖拽只能从行内的
// 文本节点起拖 —— 点在行的空白处（如右侧状态徽标左边）就拖不起来，用户实际
// 会遇到"只有文字那一小块能拖"。强制走 Sortable 自己的指针实现后，整行任意位置
// 都能作为拖拽起点。
const dragKeys = ref<Channel[]>([])
watch(
  () => activePlatform.value?.keys,
  (keys) => {
    const next = keys || []
    // 拖拽中途父级刷新时，避免把列表重置回旧顺序（列表长度/id 集合没变就不动）。
    const sameSet =
      next.length === dragKeys.value.length && next.every((k, i) => k.id === dragKeys.value[i]?.id)
    if (sameSet) {
      // 同一批 Key 只是内容变了（启用状态、模型数等）：保留拖拽后的顺序，换成新对象。
      const byId = new Map(next.map((k) => [k.id, k]))
      dragKeys.value = dragKeys.value.map((k) => byId.get(k.id) || k)
      return
    }
    dragKeys.value = [...next]
  },
  { immediate: true },
)
/**
 * 交给拖拽组件渲染的列表。
 *
 * 搜索时传过滤后的**独立副本**：它只用来画，拖拽被禁用，任何回写都不落到
 * dragKeys 上 —— 否则「只拖动了显示出来的一半」会把整组顺序改乱。
 * 不搜索时两者同步指向同一份内容，拖拽照旧整组生效。
 */
const dragVisible = ref<Channel[]>([])
/** 是否正在拖拽：与官方示例一致，拖拽期间关掉 TransitionGroup 的 name。 */
const dragging = ref(false)
/** 松手后 nextTick 再复位（官方示例写法，避免与 Sortable 收尾抢 DOM）。 */
function endDrag() {
  nextTick(() => {
    dragging.value = false
  })
}
watch(
  [() => dragKeys.value, keyword],
  () => {
    dragVisible.value = searching.value ? dragKeys.value.filter(matchesKeyword) : dragKeys.value
  },
  { immediate: true },
)

/** 松手后把新顺序回传页面；搜索期间拖拽已禁用，这里只兜底不改顺序。 */
function onDraggableUpdate(keys: Channel[]) {
  if (searching.value) return
  dragKeys.value = keys
  const baseUrl = activePlatform.value?.baseUrl
  if (!baseUrl) return
  emit('reorder', baseUrl, keys)
}

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

    <!--
      Key 搜索框：固定在列表上方（不在滚动容器里），滚到列表底部也还能改关键词。
      只按名字过滤；拖拽排序要按整组提交，所以搜索期间禁用拖拽，避免「只拖了
      显示出来的一半」这种说不清的结果。
    -->
    <div class="border-b border-border p-2">
      <div class="relative">
        <RiSearchLine
          size="14"
          class="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-muted-foreground"
        />
        <Input
          v-model="keyword"
          class="h-8 pl-7 pr-7 text-sm"
          placeholder="搜索 Key 名称"
          aria-label="搜索 Key 名称"
        />
        <button
          v-if="searching"
          type="button"
          class="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-1 text-muted-foreground hover:bg-muted hover:text-foreground"
          aria-label="清空搜索"
          @click="keyword = ''"
        >
          <RiCloseLine size="14" />
        </button>
      </div>
    </div>

    <!--
      Key 列表：一行一个 Key，选中项高亮（选中配色与「模型状态」页左栏保持一致）。
      整张卡片任意位置可拖拽排序；没有移动的单击仍是选中。
    -->
    <div class="max-h-72 min-h-0 flex-1 overflow-auto p-1.5 md:max-h-none!">
      <VueDraggable
        v-if="dragKeys.length"
        :key="activePlatform?.baseUrl"
        :model-value="dragVisible"
        target=".channel-key-list"
        :disabled="searching"
        :animation="150"
        @start="dragging = true"
        @end="endDrag"
        @update:model-value="onDraggableUpdate"
      >
        <!-- 官方写法：VueDraggable 管行为，TransitionGroup 管列表 DOM 与换位动画 -->
        <TransitionGroup
          type="transition"
          tag="div"
          :name="!dragging ? 'key-sort' : undefined"
          class="key-sort-list channel-key-list"
        >
          <button
            v-for="key in dragVisible"
            :key="key.id"
            type="button"
            class="flex w-full cursor-grab touch-none select-none items-center gap-2 rounded-md px-2 py-2 text-left transition-colors active:cursor-grabbing"
            :class="key.id === activeKeyId ? 'bg-muted' : 'bg-background hover:bg-muted/60'"
            @click="emit('selectKey', key)"
          >
            <span class="min-w-0 flex-1" :title="key.name">
              <span
                class="block truncate text-sm"
                :class="
                  key.id === activeKeyId
                    ? 'font-semibold text-foreground'
                    : 'font-medium text-foreground'
                "
                >{{ key.name }}</span
              >
              <span class="mt-0.5 block truncate text-[11px] text-muted-foreground">{{
                channelModelLabel(key)
              }}</span>
            </span>
            <span
              class="shrink-0 rounded px-1 py-0.5 text-[10px] font-medium"
              :class="TONE_STYLE_SOFT[keyStatus(key).tone]"
            >
              {{ keyStatus(key).label }}
            </span>
          </button>
        </TransitionGroup>
      </VueDraggable>
      <p v-if="!dragKeys.length" class="px-2 py-3 text-sm text-muted-foreground">该平台暂无 Key</p>
      <p v-else-if="!dragVisible.length" class="px-2 py-3 text-sm text-muted-foreground">
        没有名字包含「{{ keyword.trim() }}」的 Key
      </p>
    </div>
  </div>
</template>
