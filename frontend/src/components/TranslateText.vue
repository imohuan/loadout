<script setup lang="ts">
// 可复用翻译文本组件：只读取已翻译结果，绝不主动触发翻译、绝不发网络请求。
// 通过 textKey 从 translate store 的 translatedMap 读取译文展示；
// 译文由 TranslateView 在加载时批量 lookup、翻译时显式写入 translatedMap。
import { computed } from 'vue'
import { useTranslateStore } from '@/stores/translate'

const props = withDefaults(
  defineProps<{
    /** 原文（英文等） */
    source: string
    /** 翻译结果在 store.translatedMap 里的 key；缺省用 source 文本 */
    textKey?: string
    /** 显示模式：覆盖 store 全局配置（translated/original/both） */
    displayMode?: 'translated' | 'original' | 'both'
    /** 只读 lookup 的兜底来源信息 */
    sourceType?: string
    sourceId?: string
    /** 单行截断显示（父容器需提供宽度；与外部 truncate class 等价） */
    singleLine?: boolean
  }>(),
  {},
)

const store = useTranslateStore()

// 译文：优先从 store.translatedMap 读（textKey），否则从本地内存缓存读
const translated = computed(() => {
  if (props.textKey) {
    const fromMap = store.getTranslated(props.textKey)
    if (fromMap) return fromMap
  }
  if (props.source) {
    const cached = store.getCached(props.source, store.targetLang)
    if (cached) return cached
  }
  return null
})

// 显示模式：优先组件 prop，否则用 store 全局配置
const mode = computed(() => props.displayMode ?? store.displayMode)

const showTranslated = computed(() => {
  if (!translated.value) return false
  if (mode.value === 'original') return false
  return true
})
const showOriginal = computed(() => {
  if (!translated.value) return true
  if (mode.value === 'original') return true
  if (mode.value === 'both') return true
  return false
})
</script>

<template>
  <!-- 支持默认 slot 自定义渲染：作用域暴露 source(原文) / translated(译文,可能null) / hasTranslation / original(是否显示原文) -->
  <slot
    :source="source"
    :translated="translated"
    :has-translation="!!translated"
    :original="source"
  >
    <span
      :class="
        singleLine
          ? 'block w-full truncate min-w-0'
          : 'inline-flex min-w-0 flex-wrap items-center gap-1 break-words whitespace-normal'
      "
    >
      <span v-if="showOriginal" class="min-w-0 break-words text-muted-foreground">{{ source }}</span>
      <span v-if="showOriginal && showTranslated && translated" class="text-muted-foreground/50 mx-1">/</span>
      <span v-if="showTranslated && translated" class="min-w-0 break-words font-medium text-foreground">{{ translated }}</span>
    </span>
  </slot>
</template>
