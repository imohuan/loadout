<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { RiArrowRightUpLine, RiLinkM, RiPulseLine, RiRobot2Line } from '@remixicon/vue'
import { api } from '@/lib/api'
import type { Overview } from '@/lib/types'
import PageHeader from '@/components/PageHeader.vue'
import StatCard from '@/components/StatCard.vue'
import LoadingBlock from '@/components/LoadingBlock.vue'
import McpStatsPanel from '@/components/McpStatsPanel/McpStatsPanel.vue'
import ModelStatsPanel from '@/components/ModelStatsPanel/ModelStatsPanel.vue'

const router = useRouter()
const overview = ref<Overview>()
const loading = ref(true)
const platformLabel: Record<string, string> = {
  '': '通用 (.agents)',
  codex: 'Codex',
  claudecode: 'Claude Code',
  opencode: 'OpenCode',
}
function presetLabel() {
  if (!overview.value?.active_preset) return '未选择'
  // 优先多平台列表（active_preset_targets），回退旧单值字段（active_preset_target）。
  const targets = overview.value.active_preset_targets?.length
    ? overview.value.active_preset_targets
    : overview.value.active_preset_target
      ? [overview.value.active_preset_target]
      : ['']
  const label = targets.map((t) => platformLabel[t] || t || '通用 (.agents)').join('、')
  return `${overview.value.active_preset}（${label}）`
}
const cards = computed(() =>
  overview.value
    ? [
        { label: '渠道', value: overview.value.channels },
        { label: '插件', value: overview.value.plugins },
        { label: '运行版本', value: overview.value.version },
        { label: '当前预设', value: presetLabel() },
      ]
    : [],
)
onMounted(async () => {
  try {
    overview.value = await api<Overview>('/api/overview')
  } finally {
    loading.value = false
  }
})
</script>

<template>
  <div class="space-y-6">
    <PageHeader
      title="概览"
      description="查看 Loadout 当前运行状态，并快速进入常用管理项。"
    /><LoadingBlock v-if="loading" /><template v-else
      ><div class="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard v-for="card in cards" :key="card.label" v-bind="card" />
      </div>
      <McpStatsPanel />
      <ModelStatsPanel />
      <div class="grid gap-4 lg:grid-cols-2">
        <Card class="rounded-md"
          ><CardHeader
            ><CardTitle class="text-base">开始配置</CardTitle
            ><CardDescription>按使用顺序完成基础设置。</CardDescription></CardHeader
          ><CardContent class="space-y-1"
            ><Button
              variant="ghost"
              class="w-full justify-between"
              @click="router.push('/channels')"
              ><span class="flex items-center gap-2"><RiLinkM size="18" />添加渠道与模型</span
              ><RiArrowRightUpLine size="16" /></Button
            ><Button
              variant="ghost"
              class="w-full justify-between"
              @click="router.push('/aggregates')"
              ><span class="flex items-center gap-2"><RiRobot2Line size="18" />配置聚合模型</span
              ><RiArrowRightUpLine size="16" /></Button
            ><Button
              variant="ghost"
              class="w-full justify-between"
              @click="router.push('/model-status')"
              ><span class="flex items-center gap-2"><RiPulseLine size="18" />检查模型状态</span
              ><RiArrowRightUpLine size="16" /></Button></CardContent></Card
        ><Card class="rounded-md"
          ><CardHeader
            ><CardTitle class="text-base">运行说明</CardTitle
            ><CardDescription>路由规则和状态彼此独立。</CardDescription></CardHeader
          ><CardContent class="space-y-3 text-sm leading-6 text-muted-foreground"
            ><p>渠道的手动开关控制是否参与路由；自动健康状态会在失败后进入冷却或禁用。</p>
            <p>
              同名模型可在多个可用渠道间自动切换。聚合模型则始终按你指定的模型与渠道顺序尝试。
            </p></CardContent
          ></Card
        >
      </div></template
    >
  </div>
</template>
