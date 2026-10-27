<script setup lang="ts">
// OpenCodex 集成状态卡片（UnifyAI 同步页面最上方，优先级 0）
//
// 设计依据：官方 CLI 生命周期文档把 opencodex 分成五段——
//   ① 装 CLI   ② ocx init 写 config   ③ 启动代理   ④ ocx sync 注入 Codex   ⑤ 开机/崩溃保护
// 而「启动代理」官方给了三种互斥的方式：
//   ocx start（前台） / ocx service（后台服务，推荐） / ocx codex-shim（按需）
// 这张卡片就是把这五段 + 三种方式铺平，让不熟悉 CLI 的人一眼看出「我卡在哪一步」。
//
// 性能：检测全部走本地文件 + /healthz（0.6s）。绝不在请求路径里调 ocx status（17~21s）。
import { computed, onMounted, ref } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiArrowDownSLine,
  RiArrowRightSLine,
  RiCheckLine,
  RiCloseLine,
  RiExternalLinkLine,
  RiLoader4Line,
  RiPlayLine,
  RiRefreshLine,
  RiStopLine,
  RiTerminalLine,
  RiWrenchLine,
} from '@remixicon/vue'
import { useProcessStore } from '@/stores/processes'
import { startTask, registerTask, clearTask } from '@/composables/useTask'
import {
  fetchOcxStatus,
  INITIAL_OCX_STATUS,
  installOcxService,
  installOcxShim,
  openOcxGui,
  repairOcxService,
  runOcxInit,
  startOcxService,
  stopOcxService,
  syncOcxConfig,
  uninstallOcxShim,
  type OcxAction,
  type OcxStatus,
} from '@/lib/unifyai'

// ---------- 状态 ----------
const loading = ref(false)
const status = ref<OcxStatus>({ ...INITIAL_OCX_STATUS })
const collapsed = ref(true)
const acting = ref<OcxAction | null>(null)

// ---------- 汇总 ----------
const syncReady = computed(
  () =>
    status.value.cliInstalled &&
    status.value.configInitialized &&
    status.value.serviceRunning &&
    status.value.loadoutProvider &&
    status.value.modelCount > 0 &&
    status.value.codexInjected,
)
const failing = computed(() => status.value.capabilities.filter((c) => !c.ok))
const doneCount = computed(() => status.value.capabilities.filter((c) => c.ok).length)
const totalCount = computed(() => status.value.capabilities.length)

// ---------- 刷新 ----------
async function refresh(silent = true) {
  loading.value = true
  try {
    status.value = await fetchOcxStatus()
  } catch (e) {
    if (!silent) toast.error('检测失败', { description: String(e) })
  } finally {
    loading.value = false
  }
}
onMounted(() => refresh())

// ---------- 执行动作（统一走全局进程面板，弹进度框） ----------
const RUNNERS: Record<string, (id: string) => Promise<{ started: boolean; id: string }>> = {
  start: startOcxService,
  stop: stopOcxService,
  gui: openOcxGui,
  sync: syncOcxConfig,
  init: runOcxInit,
  'service-install': installOcxService,
  'service-repair': repairOcxService,
  'shim-install': installOcxShim,
  'shim-uninstall': uninstallOcxShim,
}

const SUCCESS: Record<string, string> = {
  start: '代理已启动',
  stop: '代理已停止（Codex 配置已还原）',
  gui: '已打开 OpenCodex 管理面板',
  sync: 'Codex 配置已同步',
  'service-install': '后台服务已安装',
  'service-repair': '后台服务已修复',
  'shim-install': '按需自启动已安装',
  'shim-uninstall': '按需自启动已卸载',
}

async function run(action: string, successMsg?: string) {
  if (acting.value) return
  const runner = RUNNERS[action]
  if (!runner) {
    toast.error('暂不支持该操作', { description: action })
    return
  }
  const id = `opencodex:${action}:${Date.now()}`
  acting.value = action as OcxAction
  const store = useProcessStore()
  store.openLog(id) // 与 UnifyAI 同步一致的进度弹窗
  registerTask(id, {
    kind: 'opencodex',
    onDone: () => {
      acting.value = null
      toast.success(successMsg || SUCCESS[action] || '完成')
      setTimeout(() => refresh(), 800)
    },
    onError: (e) => {
      acting.value = null
      toast.error('操作失败', { description: String(e) })
    },
  })
  try {
    await startTask({ id, kind: "opencodex", run: () => runner(id) })
  } catch (e) {
    clearTask(id)
    acting.value = null
    toast.error('启动失败', { description: String(e) })
  }
}

function fix(cap: { fixAction?: string }) {
  if (!cap.fixAction) return
  run(cap.fixAction)
}

const startMethod = (m: { action?: string }) => {
  if (m.action) run(m.action)
}

function methodBadge(m: { key: string; active: boolean; ready: boolean }) {
  if (m.active) return { text: '当前方式' }
  if (!m.ready) return { text: '需先初始化' }
  return { text: '可切换' }
}

const launchLabel: Record<string, string> = {
  service: '后台服务',
  shim: '按需 shim',
  tray: '桌面托盘',
  manual: '手动启动',
  unknown: '未知',
}
</script>

<template>
  <Card class="rounded-md" :class="syncReady ? 'border-emerald-500/30' : 'border-amber-500/40'">
    <!-- 用 CardAction 而不是 flex 布局：shadcn 的 CardHeader 是 grid，
         只有 col-start-2 的 CardAction 才能稳定落在右上角（参考「请求记录」卡）。 -->
    <CardHeader>
      <CardTitle class="flex flex-wrap items-center gap-2 text-base">
          <span
            class="inline-block size-2 shrink-0 rounded-full"
            :class="syncReady ? 'bg-emerald-500' : 'bg-amber-500'"
          />
          Open Codex 集成状态
          <Badge
            v-if="syncReady"
            variant="outline"
            class="border-emerald-500/40 text-emerald-700 dark:text-emerald-300"
            >可用于同步</Badge
          >
          <Badge
            v-else
            variant="outline"
            class="border-amber-500/40 text-amber-700 dark:text-amber-300"
            >{{ failing.length }} 项待处理</Badge
          >
          <span class="text-xs font-normal text-muted-foreground tabular-nums">
            {{ doneCount }}/{{ totalCount }} 就绪
          </span>
      </CardTitle>
      <CardDescription>
        UnifyAI 的模型来自本机 Open Codex 代理。下面按官方生命周期分层：安装 → 初始化 →
        启动 → 接入 → 保护。
      </CardDescription>

      <!-- 右上角：与表格工具栏一致的「重新检测」 -->
      <CardAction class="flex flex-wrap items-center justify-end gap-2">
        <Button variant="ghost" size="sm" :disabled="loading" @click="refresh(false)">
          <RiLoader4Line v-if="loading" size="14" class="animate-spin" />
          <RiRefreshLine v-else size="14" />
          {{ loading ? '检测中…' : '重新检测' }}
        </Button>
        <Button variant="ghost" size="sm" @click="collapsed = !collapsed">
          <RiArrowDownSLine v-if="!collapsed" size="14" />
          <RiArrowRightSLine v-else size="14" />
          {{ collapsed ? '展开详情' : '收起' }}
        </Button>
      </CardAction>
    </CardHeader>

    <CardContent class="space-y-4">
      <!-- 生命周期 7 项（始终显示，一眼看出卡在哪） -->
      <div class="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        <div
          v-for="cap in status.capabilities"
          :key="cap.key"
          class="rounded-md border p-2.5"
          :class="
            cap.ok
              ? 'border-border/60 bg-muted/20'
              : 'border-amber-500/40 bg-amber-500/[0.06]'
          "
        >
          <div class="flex items-start gap-1.5">
            <span
              class="mt-1 inline-flex size-3.5 shrink-0 items-center justify-center rounded-full text-[9px] font-bold text-white"
              :class="cap.ok ? 'bg-emerald-500' : 'bg-amber-500'"
            >
              <RiCheckLine v-if="cap.ok" size="10" />
            </span>
            <div class="min-w-0 flex-1">
              <div class="flex flex-wrap items-baseline gap-x-1.5">
                <span class="text-sm font-medium">{{ cap.label }}</span>
                <span v-if="!cap.ok" class="text-[11px] text-amber-600 dark:text-amber-400"
                  >待修复</span
                >
              </div>
              <p class="mt-0.5 text-[11px] leading-snug text-muted-foreground">
                {{ cap.detail || cap.desc }}
              </p>
              <p
                v-if="!cap.ok && cap.fixHint && !collapsed"
                class="mt-1 text-[11px] leading-snug text-amber-700/90 dark:text-amber-300/90"
              >
                {{ cap.fixHint }}
              </p>
              <Button
                v-if="!cap.ok && cap.fixAction && !collapsed"
                variant="outline"
                size="sm"
                class="mt-1.5 h-6 px-2 text-[11px]"
                :disabled="acting !== null"
                @click="fix(cap)"
              >
                <RiWrenchLine size="11" />
                去修复
              </Button>
            </div>
          </div>
        </div>
      </div>

      <!-- 展开区：启动方式 + 待办 + 诊断 -->
      <template v-if="!collapsed">
        <!-- 三种启动方式 -->
        <div class="space-y-2">
          <div class="flex flex-wrap items-baseline gap-2">
            <p class="text-sm font-medium">启动方式（官方给了三种，选一种即可）</p>
            <span class="text-[11px] text-muted-foreground">
              当前由「{{ launchLabel[status.launchMode] || '未知' }}」拉起
            </span>
          </div>
          <div class="grid gap-2 md:grid-cols-3">
            <div
              v-for="m in status.methods"
              :key="m.key"
              class="flex flex-col rounded-md border p-3"
              :class="
                m.active
                  ? 'border-emerald-500/40 bg-emerald-500/[0.05]'
                  : 'border-border/60 bg-muted/10'
              "
            >
              <div class="flex items-start justify-between gap-2">
                <div class="min-w-0">
                  <p class="text-sm font-medium">{{ m.label }}</p>
                  <code class="mt-0.5 block truncate text-[11px] text-muted-foreground">{{
                    m.command
                  }}</code>
                </div>
                <Badge
                  variant="outline"
                  class="shrink-0 text-[10px]"
                  :class="
                    m.active
                      ? 'border-emerald-500/40 text-emerald-700 dark:text-emerald-300'
                      : 'text-muted-foreground'
                  "
                  >{{ methodBadge(m).text }}</Badge
                >
              </div>
              <p class="mt-1.5 flex-1 text-[11px] leading-snug text-muted-foreground">
                {{ m.desc }}
              </p>
              <div class="mt-2 flex flex-wrap items-center gap-1.5">
                <Button
                  size="sm"
                  :variant="m.active ? 'outline' : 'secondary'"
                  class="h-7 px-2 text-xs"
                  :disabled="!m.ready || acting !== null"
                  @click="startMethod(m)"
                >
                  <RiLoader4Line v-if="acting === m.action" size="12" class="animate-spin" />
                  <RiPlayLine v-else size="12" />
                  {{ m.active ? '重新应用' : m.actionLabel }}
                </Button>
                <!-- 服务专属：修复 / 卸载 -->
                <template v-if="m.key === 'service' && status.serviceInstalled">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-1.5 text-[11px]"
                    :disabled="acting !== null"
                    @click="run('service-repair')"
                  >
                    修复
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-1.5 text-[11px] text-muted-foreground"
                    :disabled="acting !== null"
                    @click="run('service-uninstall')"
                  >
                    卸载
                  </Button>
                </template>
                <template v-if="m.key === 'shim' && status.shimInstalled">
                  <Button
                    variant="ghost"
                    size="sm"
                    class="h-7 px-1.5 text-[11px] text-muted-foreground"
                    :disabled="acting !== null"
                    @click="run('shim-uninstall')"
                  >
                    卸载
                  </Button>
                </template>
              </div>
            </div>
          </div>
        </div>

        <!-- 待处理问题 -->
        <div
          v-if="status.missing?.length"
          class="rounded-md border border-amber-500/30 bg-amber-500/5 p-3"
        >
          <p class="mb-1 text-xs font-medium text-amber-700 dark:text-amber-300">待处理问题：</p>
          <ul class="space-y-1 text-xs text-muted-foreground">
            <li v-for="(msg, i) in status.missing || []" :key="i">· {{ msg }}</li>
          </ul>
        </div>

        <!-- 底部工具行 -->
        <div class="flex flex-wrap items-center gap-2 border-t pt-3">
          <Button variant="outline" size="sm" :disabled="acting !== null" @click="run('gui')">
            <RiLoader4Line v-if="acting === 'gui'" size="13" class="animate-spin" />
            <RiExternalLinkLine v-else size="13" />
            打开管理面板
          </Button>
          <Button
            v-if="status.serviceRunning"
            variant="outline"
            size="sm"
            :disabled="acting !== null"
            @click="run('stop')"
          >
            <RiLoader4Line v-if="acting === 'stop'" size="13" class="animate-spin" />
            <RiStopLine v-else size="13" />
            停止代理
          </Button>
          <Button variant="ghost" size="sm" :disabled="acting !== null" @click="run('sync')">
            <RiRefreshLine size="13" />
            同步 Codex 配置
          </Button>
          <Button
            v-if="status.cliInstalled && !status.configInitialized"
            variant="ghost"
            size="sm"
            :disabled="acting !== null"
            @click="run('init')"
          >
            <RiTerminalLine size="13" />
            运行初始化向导
          </Button>
          <Button v-if="syncReady" variant="ghost" size="sm" @click="collapsed = true">
            <RiCloseLine size="13" />
            收起
          </Button>
        </div>

        <p class="text-[11px] leading-snug text-muted-foreground">
          服务 / 自启 / 停止会弹 UAC 管理员确认。管理面板是 Open Codex 官方 UI，provider 与
          模型都在那里配；本页只做检测和一键命令，不改它的原生界面。
        </p>
      </template>
    </CardContent>
  </Card>
</template>
