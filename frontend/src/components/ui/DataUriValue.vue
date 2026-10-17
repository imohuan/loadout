<script setup lang="ts">
/**
 * DataUriValue — 在可折叠 JSON 视图里优雅展示超长的 base64 data URI。
 *
 * 问题的由来：调试用的请求体里经常带整张图片的 data URI（一张图就是几百 KB），
 * 直接在 JSON 树里铺开会把整个视图撑爆，也没法阅读。这里按 MIME 类型把它压缩成
 * 一行摘要（例如「图片 png · 213 KB」），鼠标悬停时弹一张卡片：
 *   - 图片 / 视频 / 音频：直接渲染预览
 *   - 文本类：显示解码后的开头片段
 *   - 其它：显示大小与 MIME
 * 卡片里仍提供「复制完整 data URI」，需要原文时随时能拿。
 *
 * 只处理标准 data URI（data:<mime>;base64,<payload>）；普通短字符串原样返回。
 */
import { computed, ref } from 'vue'
import { HoverCard, HoverCardContent, HoverCardTrigger } from 'shadcn-vue-cdn'

const props = withDefaults(
  defineProps<{
    /** 完整值（可能是 data URI，也可能是普通文本） */
    value?: string | null
    /** 多长的 base64 才进入摘要模式（太短的没必要折叠） */
    threshold?: number
  }>(),
  { threshold: 512 },
)

/** data URI 拆解：mime + 是否 base64 + 载荷。非 data URI 返回 null。 */
const parsed = computed(() => {
  const v = props.value ?? ''
  const m = /^data:([^;,]+)(;base64)?,([\s\S]*)$/.exec(v)
  if (!m) return null
  return { mime: m[1], base64: !!m[2], payload: m[3] }
})

/** 是否走摘要展示 */
const isLongDataUri = computed(() => {
  const p = parsed.value
  if (!p) return false
  return p.payload.length >= props.threshold
})

/** 预览大类：image / video / audio / text / other */
const kind = computed(() => {
  const mime = parsed.value?.mime ?? ''
  if (mime.startsWith('image/')) return 'image'
  if (mime.startsWith('video/')) return 'video'
  if (mime.startsWith('audio/')) return 'audio'
  if (mime.startsWith('text/') || mime.includes('json') || mime.includes('xml')) return 'text'
  return 'other'
})

/** 解码后字节数（base64 按长度估算，避免为了显示大小去解码整个字符串） */
const decodedBytes = computed(() => {
  const p = parsed.value
  if (!p) return 0
  if (!p.base64) return new Blob([p.payload]).size
  const len = p.payload.length
  const padding = p.payload.endsWith('==') ? 2 : p.payload.endsWith('=') ? 1 : 0
  return Math.max(0, Math.floor((len * 3) / 4) - padding)
})

const sizeLabel = computed(() => {
  const n = decodedBytes.value
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  return (n / 1024 / 1024).toFixed(2) + ' MB'
})

/** 缩略标签，例如「图片 png · 213.4 KB」 */
const summary = computed(() => {
  const p = parsed.value
  if (!p) return ''
  const labelMap: Record<string, string> = { image: '图片', video: '视频', audio: '音频', text: '文本', other: '数据' }
  const label = labelMap[kind.value] ?? '数据'
  const sub = p.mime.split('/')[1] || p.mime
  return label + ' ' + sub + ' · ' + sizeLabel.value
})

/** 完整 data URI（复制用；预览用同一个值） */
const fullValue = computed(() => props.value ?? '')

/** 文本类预览：解码 base64 后取前若干字符 */
const textPreview = computed(() => {
  const p = parsed.value
  if (!p || kind.value !== 'text') return ''
  try {
    if (!p.base64) return p.payload.slice(0, 800)
    const bin = atob(p.payload.slice(0, 4096))
    const bytes = Uint8Array.from(bin, (c) => c.charCodeAt(0))
    const text = new TextDecoder('utf-8', { fatal: false }).decode(bytes)
    return text.slice(0, 800)
  } catch {
    return ''
  }
})

const copied = ref(false)
async function copy() {
  try {
    await navigator.clipboard.writeText(fullValue.value)
    copied.value = true
    setTimeout(() => (copied.value = false), 1500)
  } catch {
    /* 非安全上下文静默失败 */
  }
}
</script>

<template>
  <!-- 普通值 / 短 data URI：原样输出，不打扰阅读 -->
  <span v-if="!isLongDataUri">{{ props.value }}</span>

  <!-- 长 data URI：缩略摘要 + hover 预览 -->
  <HoverCard v-else :open-delay="120" :close-delay="120">
    <HoverCardTrigger as-child>
      <span
        class="cursor-help rounded border border-dashed border-border/70 bg-muted/40 px-1.5 py-0.5 text-[11px] text-muted-foreground"
        >{{ summary }}</span
      >
    </HoverCardTrigger>
    <HoverCardContent align="start" :side-offset="6" class="w-[min(520px,calc(100vw-2rem))] p-0">
      <div class="rounded-md border border-border/60 bg-muted/30 px-3 py-2">
        <div class="mb-2 flex items-center justify-between gap-2">
          <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
            {{ summary }}
          </span>
          <button
            type="button"
            class="rounded border border-border/60 px-1.5 py-0.5 text-[10px] text-foreground hover:bg-muted"
            @click.stop="copy"
          >
            {{ copied ? '已复制' : '复制完整值' }}
          </button>
        </div>

        <!-- 图片直接预览 -->
        <img
          v-if="kind === 'image'"
          :src="fullValue"
          alt="base64 图片预览"
          class="max-h-72 w-auto max-w-full rounded border border-border/50 bg-background object-contain"
        />
        <!-- 视频预览 -->
        <video
          v-else-if="kind === 'video'"
          :src="fullValue"
          controls
          class="max-h-72 w-full rounded border border-border/50 bg-background"
        />
        <!-- 音频预览 -->
        <audio v-else-if="kind === 'audio'" :src="fullValue" controls class="w-full" />
        <!-- 文本预览 -->
        <pre
          v-else-if="kind === 'text'"
          class="max-h-72 overflow-auto whitespace-pre-wrap break-all rounded bg-background/60 p-2 font-mono text-[11px] leading-snug text-foreground/80"
          >{{ textPreview }}</pre
        >
        <!-- 其它类型：只报大小 -->
        <p v-else class="text-xs text-muted-foreground">
          该类型无法直接预览。完整 data URI 可通过右上角按钮复制。
        </p>
      </div>
    </HoverCardContent>
  </HoverCard>
</template>

