<script setup lang="ts">
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { toast } from 'vue-sonner'
import {
  RiAddLine,
  RiDeleteBinLine,
  RiDownload2Line,
  RiKey2Line,
  RiLoader4Line,
  RiRefreshLine,
  RiSettings3Line,
  RiTranslate2,
  RiUpload2Line,
} from '@remixicon/vue'
import { useManagementApi } from '@/composables/useManagementApi'
import { useListLoader } from '@/composables/useListLoader'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useConfirm } from '@/composables/useConfirm'
import { startTask, registerTask } from '@/composables/useTask'
import PageHeader from '@/components/PageHeader.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import EmptyState from '@/components/EmptyState.vue'
import VolcQuotaCard from '@/components/VolcQuotaCard.vue'
import ConfigExportDialog from '@/components/config-transfer/ConfigExportDialog.vue'
import ConfigImportDialog from '@/components/config-transfer/ConfigImportDialog.vue'
import TranslateView from '@/views/TranslateView.vue'

const api = useManagementApi()
const { data: keys, loading: keysLoading, refresh: refreshKeys } = useListLoader(api.keys)
const {
  data: plugins,
  loading: pluginsLoading,
  refresh: refreshPlugins,
} = useListLoader(api.plugins)
const {
  data: settingsData,
  loading: settingsLoading,
  refresh: refreshSettings,
} = useListLoader(api.settings)
const { run, isPending } = useAsyncTask()
const { confirmDialog } = useConfirm()
const keyDialog = ref(false)
const exportDialog = ref(false)
const importDialog = ref(false)
const activeTab = ref('runtime')
const newKey = ref('')
const skForm = reactive({ name: '', models: '' })
const passwordForm = reactive({ old: '', new: '' })
const settingsForm = reactive({ active_preset: '', default_model: '', use_global_cmd: false })
watch(
  settingsData,
  (value) => {
    if (value) Object.assign(settingsForm, value)
  },
  { immediate: true },
)
const loading = computed(() => keysLoading.value || pluginsLoading.value || settingsLoading.value)
const translateRef = ref<InstanceType<typeof TranslateView> | null>(null)
async function refresh() {
  // 翻译 Tab 激活时，顶部「刷新」按钮刷新的是翻译来源
  if (activeTab.value === 'translations') {
    await translateRef.value?.refresh()
    return
  }
  await Promise.all([refreshKeys(), refreshPlugins(), refreshSettings()])
}
async function createSkKey() {
  await run(
    'create-key',
    async () => {
      const result = await api.createSkKey({
        name: skForm.name,
        models: skForm.models.split(/[\s,]+/).filter(Boolean),
      })
      newKey.value = result.key
      Object.assign(skForm, { name: '', models: '' })
      keyDialog.value = false
      await refreshKeys()
    },
    '密钥已创建',
  )
}
async function removeSkKey(id: string) {
  if (!(await confirmDialog('删除此模型 API 密钥？'))) return
  await run(
    `key:${id}:remove`,
    async () => {
      await api.deleteSkKey(id)
      await refreshKeys()
    },
    '密钥已删除',
  )
}
async function saveSettings() {
  await run(
    'save-settings',
    async () => {
      await api.saveSettings({ ...settingsForm })
      await refreshSettings()
    },
    '设置已保存',
  )
}
async function changePassword() {
  await run(
    'change-password',
    async () => {
      await api.changePassword({ ...passwordForm })
      Object.assign(passwordForm, { old: '', new: '' })
    },
    '密码已修改',
  )
}
// ===== 依赖更新检查（unifyai / skills 全局包）=====
const depsItems = ref<Array<{ name: string; installed: boolean; current: string; latest: string; needUpdate: boolean; error?: string }>>([])
const depsChecking = ref(false)
// 正在安装/更新的库名（立即置位显示按钮加载，任务结束由 useTask 收尾清空）。
const depsBusy = ref<string | null>(null)

/** 拉取依赖检查状态（后端启动时后台已自动查过一次，读缓存即可） */
async function refreshDeps() {
  try {
    const res = await api.depsStatus()
    depsItems.value = res.items || []
    depsChecking.value = res.checking
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '读取依赖状态失败')
  }
}

/** 手动触发重新检查 */
async function checkDeps() {
  if (depsChecking.value) return
  depsChecking.value = true
  try {
    // 后端同步查询，直接返回最新状态
    const res = await api.depsRefresh()
    depsItems.value = res.items || []
    depsChecking.value = res.checking
  } catch (e) {
    toast.error(e instanceof Error ? e.message : '检查失败')
  } finally {
    depsChecking.value = false
  }
}

/** 安装/更新全局包（后台 procreg 任务）。加载态与结束收尾统一由 useTask 管理。 */
async function installDep(name: string) {
  if (depsBusy.value) return
  const taskId = `dep-install:${name}`
  // 注册收尾：安装结束（done）刷新依赖状态并清空按钮加载态。
  registerTask(taskId, {
    kind: 'dep',
    onDone: () => {
      depsBusy.value = null
      void refreshDeps()
    },
    onError: (err) => {
      depsBusy.value = null
      void refreshDeps()
      toast.error(`安装/更新 ${name} 失败`, { description: String(err) })
    },
  })
  depsBusy.value = name
  try {
    await startTask({ id: taskId, kind: 'dep', run: () => api.depsInstall(name, taskId) })
  } catch (e) {
    depsBusy.value = null
    toast.error(e instanceof Error ? e.message : `启动安装 ${name} 失败`)
  }
}

// 全局指令开关自动保存：仅在设置数据真正加载完成（后端值已回填到 settingsForm）后，
// 才开始监听 use_global_cmd，从根上避免「进入设置页 → 异步回填 use_global_cmd」误触发保存。
let stopGlobalCmdWatch: (() => void) | null = null
watch(
  settingsData,
  (value) => {
    if (!value || stopGlobalCmdWatch) return
    stopGlobalCmdWatch = watch(
      () => settingsForm.use_global_cmd,
      () => saveSettings(),
    )
  },
)
onMounted(() => {
  refreshDeps()
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader title="设置" description="集中管理运行时默认值、模型密钥和插件状态。"
      ><template #actions
        ><Button variant="outline" :disabled="loading || isPending('refresh')" @click="refresh">
          <RiLoader4Line v-if="isPending('refresh')" class="animate-spin" size="16" /><RiRefreshLine
            v-else
            size="16"
          />刷新 </Button
        ><Button
          v-if="activeTab === 'credentials'"
          :disabled="loading || isPending('create-key')"
          @click="keyDialog = true"
        >
          <RiLoader4Line v-if="isPending('create-key')" class="animate-spin" size="16" /><RiAddLine
            v-else
            size="16"
          />创建模型 API 密钥 </Button
        ><Button variant="outline" @click="exportDialog = true">
          <RiDownload2Line size="16" />导出配置 </Button
        ><Button @click="importDialog = true">
          <RiUpload2Line size="16" />导入配置
        </Button></template
      ></PageHeader
    >
    <LoadingBlock v-if="loading" />
    <template v-else>
      <Alert v-if="newKey">
        <AlertTitle>新密钥仅显示一次</AlertTitle>
        <AlertDescription class="mt-2 flex flex-wrap items-center gap-2"
          ><code class="max-w-full break-all rounded bg-muted px-2 py-1 text-xs select-all">{{
            newKey
          }}</code
          ><Button size="sm" variant="outline" @click="newKey = ''">关闭</Button></AlertDescription
        >
      </Alert>
      <Tabs v-model="activeTab" class="space-y-4">
        <TabsList class="inline-flex h-auto w-fit max-w-full flex-wrap justify-start gap-1">
          <TabsTrigger value="runtime"> <RiSettings3Line size="16" />运行设置 </TabsTrigger>
          <TabsTrigger value="credentials"> <RiKey2Line size="16" />模型密钥 </TabsTrigger>
          <TabsTrigger value="translations"> <RiTranslate2 size="16" />翻译 </TabsTrigger>
          <TabsTrigger value="plugins">插件</TabsTrigger>
        </TabsList>
        <TabsContent value="runtime" class="space-y-4">
          <VolcQuotaCard />
          <div class="grid gap-4 lg:grid-cols-2">
            <Card class="rounded-md hidden" >
              <CardHeader>
                <CardTitle class="text-base">运行时设置</CardTitle>
              </CardHeader>
              <CardContent>
                <form class="space-y-3" @submit.prevent="saveSettings">
                  <div class="space-y-1">
                    <Label for="default-model">默认模型</Label
                    ><Input
                      id="default-model"
                      v-model="settingsForm.default_model"
                      placeholder="deepseek-chat"
                    />
                  </div>
                  <div class="space-y-1">
                    <Label for="active-preset">当前预设</Label
                    ><Input
                      id="active-preset"
                      v-model="settingsForm.active_preset"
                      placeholder="编程向"
                    />
                  </div>
                  <Button type="submit" :disabled="isPending('save-settings')">
                    <RiLoader4Line
                      v-if="isPending('save-settings')"
                      class="animate-spin"
                      size="16"
                    />保存设置
                  </Button>
                </form>
              </CardContent>
            </Card>
            <Card class="rounded-md">
              <CardHeader>
                <CardTitle class="text-base">依赖更新</CardTitle>
                <CardDescription>检查 unifyai / skills 全局包是否需要更新</CardDescription>
              </CardHeader>
              <CardContent class="space-y-3">
                <div class="flex items-center justify-between gap-3">
                  <div class="flex items-center gap-2">
                    <Switch id="use-global-cmd" v-model="settingsForm.use_global_cmd" />
                    <Label for="use-global-cmd" class="cursor-pointer text-sm"
                      >使用全局指令（关闭则用 npx）</Label
                    >
                  </div>
                  <Button
                    variant="outline"
                    size="sm"
                    :disabled="depsChecking"
                    @click="checkDeps"
                  >
                    <RiLoader4Line v-if="depsChecking" class="animate-spin" size="14" />
                    <RiRefreshLine v-else size="14" />
                    {{ depsChecking ? '检查中…' : '刷新状态' }}
                  </Button>
                </div>
                <div class="space-y-2">
                  <div
                    v-if="!depsItems.length && !depsChecking"
                    class="rounded-md border p-3 text-sm text-muted-foreground"
                  >
                    暂无数据，点击「刷新状态」检查
                  </div>
                  <div
                    v-for="dep in depsItems"
                    :key="dep.name"
                    class="flex items-center justify-between gap-2 rounded-md border p-3"
                  >
                    <div class="flex min-w-0 items-center gap-2">
                      <span class="font-mono text-sm font-medium">{{ dep.name }}</span>
                      <span
                        v-if="dep.error"
                        class="truncate text-xs text-destructive"
                        :title="dep.error"
                        >检查失败</span
                      >
                      <span v-else-if="!dep.installed" class="text-xs text-muted-foreground"
                        >未安装</span
                      >
                      <span v-else-if="dep.needUpdate" class="text-xs text-amber-600"
                        >{{ dep.current }} → {{ dep.latest }}</span
                      >
                      <span v-else class="text-xs text-emerald-600"
                        >已是最新 {{ dep.latest }}</span
                      >
                    </div>
                    <Button
                      v-if="!dep.installed || dep.needUpdate"
                      size="sm"
                      :disabled="!!depsBusy"
                      class="shrink-0"
                      @click="installDep(dep.name)"
                    >
                      <RiLoader4Line
                        v-if="depsBusy === dep.name"
                        class="animate-spin"
                        size="14"
                      />
                      <template v-else>{{ dep.installed ? '更新' : '安装' }}</template>
                      <span v-if="depsBusy === dep.name" class="ml-1">{{
                        dep.installed ? '更新中…' : '安装中…'
                      }}</span>
                    </Button>
                  </div>
                </div>
              </CardContent>
            </Card>
            <Card class="rounded-md">
              <CardHeader>
                <CardTitle class="text-base">修改密码</CardTitle>
              </CardHeader>
              <CardContent>
                <form class="space-y-3" @submit.prevent="changePassword">
                  <div class="space-y-1">
                    <Label for="old-password">旧密码</Label
                    ><Input id="old-password" v-model="passwordForm.old" type="password" required />
                  </div>
                  <div class="space-y-1">
                    <Label for="new-password">新密码</Label
                    ><Input id="new-password" v-model="passwordForm.new" type="password" required />
                  </div>
                  <Button type="submit" :disabled="isPending('change-password')">
                    <RiLoader4Line
                      v-if="isPending('change-password')"
                      class="animate-spin"
                      size="16"
                    />修改密码
                  </Button>
                </form>
              </CardContent>
            </Card>
          </div>
        </TabsContent>
        <TabsContent value="credentials" class="space-y-4">
          <TooltipProvider>
            <Card class="rounded-md">
              <CardHeader>
                <CardTitle class="text-base">模型 API 密钥</CardTitle>
              </CardHeader>
              <CardContent class="p-0">
                <div v-if="keys?.sk_keys?.length" class="overflow-x-auto">
                  <Table>
                    <TableHeader>
                      <TableRow>
                        <TableHead>名称</TableHead>
                        <TableHead>前缀</TableHead>
                        <TableHead>模型</TableHead>
                        <TableHead class="text-right">操作</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      <TableRow v-for="key in keys?.sk_keys || []" :key="key.id">
                        <TableCell>{{ key.name }}</TableCell>
                        <TableCell class="font-mono text-xs">{{ key.prefix }}</TableCell>
                        <TableCell class="text-sm text-muted-foreground">{{
                          key.models?.join(', ') || '*'
                        }}</TableCell>
                        <TableCell class="text-right">
                          <Tooltip>
                            <TooltipTrigger as-child
                              ><Button
                                variant="ghost"
                                size="icon"
                                aria-label="删除密钥"
                                :disabled="isPending(`key:${key.id}:remove`)"
                                @click="removeSkKey(key.id)"
                              >
                                <RiLoader4Line
                                  v-if="isPending(`key:${key.id}:remove`)"
                                  class="animate-spin"
                                  size="16"
                                /><RiDeleteBinLine v-else size="16" /> </Button
                            ></TooltipTrigger>
                            <TooltipContent>删除密钥</TooltipContent>
                          </Tooltip>
                        </TableCell>
                      </TableRow>
                    </TableBody>
                  </Table>
                </div>
                <EmptyState
                  v-else
                  title="没有模型 API 密钥"
                  description="创建密钥后可供 OpenAI 兼容接口调用。"
                />
              </CardContent>
            </Card>
          </TooltipProvider>
        </TabsContent>
        <TabsContent value="translations">
          <TranslateView ref="translateRef" embedded />
        </TabsContent>
        <TabsContent value="plugins">
          <Card class="rounded-md">
            <CardHeader>
              <CardTitle class="text-base"
                >插件自检（{{ plugins?.plugins?.length || 0 }} 个）</CardTitle
              >
              <CardDescription>展示插件注册的检查项和问题。</CardDescription>
            </CardHeader>
            <CardContent class="p-0">
              <div v-if="plugins?.plugins?.length" class="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>插件</TableHead>
                      <TableHead>检查项</TableHead>
                      <TableHead>结果</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody
                    ><template v-for="plugin in plugins?.plugins || []" :key="plugin.plugin">
                      <TableRow v-for="check in plugin.checks" :key="check.name">
                        <TableCell class="font-medium">{{ plugin.plugin }}</TableCell>
                        <TableCell>{{ check.name }}</TableCell>
                        <TableCell>
                          <div v-if="check.issues.length" class="space-y-1">
                            <Badge
                              v-for="issue in check.issues"
                              :key="issue.message"
                              :variant="issue.level === 'error' ? 'destructive' : 'secondary'"
                              >{{ issue.level }}: {{ issue.message }}</Badge
                            >
                          </div>
                          <Badge v-else>通过</Badge>
                        </TableCell>
                      </TableRow>
                    </template></TableBody
                  >
                </Table>
              </div>
              <EmptyState
                v-else
                title="暂无插件信息"
                description="后端装配插件后会显示自检结果。"
              />
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </template>
    <Dialog v-model:open="keyDialog">
      <DialogContent>
        <DialogHeader>
          <DialogTitle>创建模型 API 密钥</DialogTitle>
          <DialogDescription>限制此密钥可调用的模型，留空表示不限。</DialogDescription>
        </DialogHeader>
        <form class="space-y-3" @submit.prevent="createSkKey">
          <div class="space-y-1">
            <Label for="sk-name">名称</Label
            ><Input id="sk-name" v-model="skForm.name" required placeholder="本机调用" />
          </div>
          <div class="space-y-1">
            <Label for="sk-models">允许模型</Label
            ><Input id="sk-models" v-model="skForm.models" placeholder="逗号或空格分隔" />
          </div>
          <DialogFooter
            ><Button type="submit" :disabled="isPending('create-key')">
              <RiLoader4Line
                v-if="isPending('create-key')"
                class="animate-spin"
                size="16"
              />创建密钥 </Button
            ><Button type="button" variant="outline" @click="keyDialog = false"
              >取消</Button
            ></DialogFooter
          >
        </form>
      </DialogContent>
    </Dialog>
    <ConfigExportDialog :open="exportDialog" @update:open="exportDialog = $event" />
    <ConfigImportDialog :open="importDialog" @update:open="importDialog = $event" />
  </div>
</template>
