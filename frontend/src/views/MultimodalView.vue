<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { RiLoader4Line, RiSave3Line } from '@remixicon/vue'
import { api, request } from '@/lib/api'
import { useAsyncTask } from '@/composables/useAsyncTask'
import { useChannels } from '@/composables/useChannels'
import { useAggregates } from '@/composables/useAggregates'
import TargetModelPicker from '@/components/TargetModelPicker.vue'

// 与后端 plugins/multimodal-mcp/config.go 的 ToolConfig / MultimodalConfig 对应。
type ToolKind = 'image' | 'video' | 'audio'

interface ToolConfig {
  kind: ToolKind
  enabled: boolean
  model: string
  defaults: Record<string, any>
}

interface MultimodalConfig {
  enabled: boolean
  tools: ToolConfig[]
}

// 各工具默认参数的可选值。
const imageDetailOptions = [
  { value: 'low', label: 'low（低精度，最快）' },
  { value: 'high', label: 'high（高精度）' },
  { value: 'xhigh', label: 'xhigh（超高精度）' },
]
const audioTaskOptions = [
  { value: 'asr', label: 'asr（普通转写）' },
  { value: 'timed', label: 'timed（带时间戳）' },
  { value: 'diarize', label: 'diarize（多说话人）' },
  { value: 'translate', label: 'translate（翻译）' },
  { value: 'caption', label: 'caption（分析）' },
]

// 工具展示名与说明。
const toolMeta: Record<ToolKind, { title: string; desc: string }> = {
  image: { title: '图片理解', desc: 'understand_image · detail 控制精细度' },
  video: { title: '视频理解', desc: 'understand_video · fps 控制抽帧频率（0.2~5）' },
  audio: { title: '音频理解', desc: 'understand_audio · task 决定识别模式' },
}

const { run, isPending } = useAsyncTask()
const loaded = ref(false)

// 表单状态：默认参数直接以工具 kind 对应的键存进 tool.defaults。
const form = reactive<MultimodalConfig>({
  enabled: false,
  tools: [
    { kind: 'image', enabled: true, model: '', defaults: { detail: 'high' } },
    { kind: 'video', enabled: true, model: '', defaults: { fps: 1 } },
    { kind: 'audio', enabled: true, model: '', defaults: { task: 'asr', language: '', source_lang: '', target_lang: '' } },
  ],
})

function toolOf(kind: ToolKind) {
  return form.tools.find((t) => t.kind === kind)
}

// 模型列表（渠道模型 + 聚合虚拟模型合并），与 TranslateView 一致。
const channels = useChannels()
const aggregates = useAggregates()
const modelOptions = ref<string[]>([])
const modelsLoading = ref(false)
async function loadModels() {
  modelsLoading.value = true
  try {
    const [chs, aggs] = await Promise.all([channels.list(), aggregates.list()])
    const set = new Set<string>()
    for (const ch of chs) {
      for (const m of ch.models || []) set.add(m)
    }
    for (const a of aggs) set.add(a.name)
    modelOptions.value = [...set].sort()
  } finally {
    modelsLoading.value = false
  }
}

async function load() {
  await run('load-multimodal', async () => {
    const cfg = await api<MultimodalConfig>('/api/multimodal/config')
    form.enabled = cfg.enabled
    // 用后端返回的 tools 覆盖默认，确保字段真实一致；defaults 逐 kind 合并以防缺 key。
    form.tools = cfg.tools?.length ? cfg.tools : form.tools
    for (const t of form.tools) t.defaults = t.defaults || {}
    loaded.value = true
  }, '已加载多模态配置')
}

async function save() {
  await run('save-multimodal', async () => {
    await request('/api/multimodal/config', 'PUT', {
      enabled: form.enabled,
      tools: form.tools,
    })
  }, '多模态配置已保存')
}

onMounted(() => {
  void loadModels()
  void load()
})
</script>

<template>
  <div class="space-y-4">
    <!-- 端点总开关 -->
    <Card class="rounded-md">
      <CardHeader>
        <CardTitle class="text-base">多模态 MCP 端点</CardTitle>
        <CardDescription>
          开启后暴露内置端点 <code class="rounded bg-muted px-1 py-0.5 font-mono text-xs">POST /mcp/multimodal</code>，导出 3 个工具。
        </CardDescription>
      </CardHeader>
      <CardContent>
        <div class="flex items-center gap-2">
          <Switch id="multimodal-enabled" v-model="form.enabled" />
          <Label for="multimodal-enabled" class="cursor-pointer text-sm">启用多模态端点</Label>
        </div>
      </CardContent>
    </Card>

    <!-- 3 个工具配置 -->
    <Card v-for="kind in ['image', 'video', 'audio'] as ToolKind[]" :key="kind" class="rounded-md">
      <CardHeader class="flex flex-row items-start justify-between gap-3 space-y-0">
        <div>
          <CardTitle class="text-base">{{ toolMeta[kind].title }}</CardTitle>
          <CardDescription>{{ toolMeta[kind].desc }}</CardDescription>
        </div>
        <div class="flex items-center gap-2">
          <Switch :id="`tool-enabled-${kind}`" v-model="toolOf(kind)!.enabled" />
          <Label :for="`tool-enabled-${kind}`" class="cursor-pointer text-sm">启用</Label>
        </div>
      </CardHeader>
      <CardContent class="grid gap-4 md:grid-cols-2 lg:grid-cols-3">
        <!-- 内置模型名 -->
        <div class="space-y-1">
          <Label :for="`tool-model-${kind}`">内置模型名</Label>
          <TargetModelPicker
            v-model="toolOf(kind)!.model"
            :models="modelOptions"
            :multiple="false"
            :allow-custom="false"
            :loading="modelsLoading"
          />
        </div>

        <!-- 图片：detail -->
        <div v-if="kind === 'image'" class="space-y-1">
          <Label :for="`tool-detail-${kind}`">默认 detail</Label>
          <Select v-model="toolOf('image')!.defaults.detail">
            <SelectTrigger :id="`tool-detail-${kind}`">
              <SelectValue placeholder="选择精细度" />
            </SelectTrigger>
            <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
              <SelectGroup>
                <SelectItem v-for="opt in imageDetailOptions" :key="opt.value" :value="opt.value">
                  {{ opt.label }}
                </SelectItem>
              </SelectGroup>
            </SelectContent>
          </Select>
        </div>

        <!-- 视频：fps -->
        <div v-if="kind === 'video'" class="space-y-1">
          <Label :for="`tool-fps-${kind}`">默认 fps</Label>
          <Input
            :id="`tool-fps-${kind}`"
            v-model.number="toolOf('video')!.defaults.fps"
            type="number"
            min="0.2"
            max="5"
            step="0.1"
          />
          <p class="text-xs text-muted-foreground">抽帧频率，范围 0.2~5</p>
        </div>

        <!-- 音频：task + 语种 -->
        <template v-if="kind === 'audio'">
          <div class="space-y-1">
            <Label :for="`tool-task-${kind}`">默认 task</Label>
            <Select v-model="toolOf('audio')!.defaults.task">
              <SelectTrigger :id="`tool-task-${kind}`">
                <SelectValue placeholder="选择识别模式" />
              </SelectTrigger>
              <SelectContent position="popper" side="bottom" align="start" :side-offset="2">
                <SelectGroup>
                  <SelectItem v-for="opt in audioTaskOptions" :key="opt.value" :value="opt.value">
                    {{ opt.label }}
                  </SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
          </div>
          <div class="space-y-1">
            <Label :for="`tool-language-${kind}`">language</Label>
            <Input :id="`tool-language-${kind}`" v-model="toolOf('audio')!.defaults.language" placeholder="如 zh" />
          </div>
          <div class="space-y-1">
            <Label :for="`tool-source-lang-${kind}`">source_lang</Label>
            <Input :id="`tool-source-lang-${kind}`" v-model="toolOf('audio')!.defaults.source_lang" placeholder="源语言" />
          </div>
          <div class="space-y-1">
            <Label :for="`tool-target-lang-${kind}`">target_lang</Label>
            <Input :id="`tool-target-lang-${kind}`" v-model="toolOf('audio')!.defaults.target_lang" placeholder="目标语言" />
          </div>
        </template>
      </CardContent>
    </Card>

    <!-- 保存 / 加载 -->
    <div class="flex items-center gap-2">
      <Button :disabled="isPending('save-multimodal')" @click="save">
        <RiLoader4Line v-if="isPending('save-multimodal')" class="animate-spin" size="16" />
        <RiSave3Line v-else size="16" />保存
      </Button>
      <Button variant="outline" :disabled="isPending('load-multimodal')" @click="load">
        <RiLoader4Line v-if="isPending('load-multimodal')" class="animate-spin" size="16" />
        重新加载
      </Button>
      <span v-if="!loaded" class="text-sm text-muted-foreground">正在加载配置…</span>
    </div>
  </div>
</template>
