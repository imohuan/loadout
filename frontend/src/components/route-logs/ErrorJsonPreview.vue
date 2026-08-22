<script setup lang="ts">
// 彩色 JSON 预览：纯展示组件，无外壳（无 label / 折叠按钮 / 复制按钮）。
// 解析 → pretty-print 2 空格 + 行级 token 高亮；非 JSON 走原样纯文本。
// 供 RouteLogErrorCell（hover 卡片 + 内嵌两种模式）共用。
import { computed } from 'vue'

const props = withDefaults(
  defineProps<{
    body: string
    /** true = 更紧凑（小号字、小内边距、矮 max-h） */
    compact?: boolean
    /** 最大高度 class，默认 max-h-80 */
    maxHeightClass?: string
  }>(),
  { compact: false, maxHeightClass: 'max-h-80' },
)

const parsed = computed(() => {
  const raw = props.body
  if (!raw) return null
  const trimmed = raw.trim()
  if (!trimmed) return null
  if (trimmed[0] !== '{' && trimmed[0] !== '[') {
    return { kind: 'raw' as const, text: raw }
  }
  try {
    return { kind: 'json' as const, value: JSON.parse(trimmed) }
  } catch {
    return { kind: 'raw' as const, text: raw }
  }
})

const prettyJson = computed(() => {
  if (!parsed.value || parsed.value.kind !== 'json') return ''
  try {
    return JSON.stringify(parsed.value.value, null, 2)
  } catch {
    return ''
  }
})

// JSON 行级高亮：把 pretty 字符串按行扫，每行用 token 高亮。
// 不引入额外依赖（highlighter.js 太重），手写一个够用的：
// - key（含冒号结尾）：蓝/紫
// - string（双引号包住）：绿
// - number、true/false/null：橙/灰
function highlightJson(text: string): { raw: string; cls?: string }[] {
  const lines = text.split('\n')
  const PATTERN =
    /("(?:\\.|[^"\\])*"\s*:?)|("(?:\\.|[^"\\])*")|\b(-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)\b|\b(true|false)\b|\bnull\b/g
  const tokens: { raw: string; cls?: string }[] = []
  for (const line of lines) {
    let cursor = 0
    let match: RegExpExecArray | null
    PATTERN.lastIndex = 0
    while ((match = PATTERN.exec(line)) !== null) {
      if (match.index > cursor) {
        tokens.push({ raw: line.slice(cursor, match.index) })
      }
      const tok = match[0]
      if (match[1] !== undefined) {
        // key（带冒号）或裸 string 带冒号
        if (tok.trimEnd().endsWith(':')) {
          tokens.push({ raw: tok, cls: 'text-sky-600 dark:text-sky-300' })
        } else {
          tokens.push({ raw: tok, cls: 'text-emerald-600 dark:text-emerald-300' })
        }
      } else if (match[2] !== undefined) {
        tokens.push({ raw: tok, cls: 'text-emerald-600 dark:text-emerald-300' })
      } else if (match[3] !== undefined) {
        tokens.push({ raw: tok, cls: 'text-amber-600 dark:text-amber-300' })
      } else if (match[4] !== undefined) {
        tokens.push({ raw: tok, cls: 'text-orange-600 dark:text-orange-300' })
      } else if (match[0] === 'null') {
        tokens.push({ raw: tok, cls: 'text-slate-500 dark:text-slate-400' })
      } else {
        tokens.push({ raw: tok })
      }
      cursor = match.index + tok.length
    }
    if (cursor < line.length) {
      tokens.push({ raw: line.slice(cursor) })
    }
    tokens.push({ raw: '\n' })
  }
  // 去掉末尾那个孤立的换行
  if (tokens.length > 0 && tokens[tokens.length - 1].raw === '\n') {
    tokens.pop()
  }
  return tokens
}

const highlighted = computed(() => {
  if (!parsed.value || parsed.value.kind !== 'json') return null
  return highlightJson(prettyJson.value)
})
</script>

<template>
  <pre
    v-if="highlighted"
    :class="[
      'overflow-auto whitespace-pre break-all font-mono leading-snug',
      props.compact ? 'text-[11px]' : 'text-xs',
      props.maxHeightClass,
    ]"
  ><template v-for="(tok, i) in highlighted" :key="i"><span v-if="tok.cls" :class="tok.cls">{{ tok.raw }}</span><span v-else>{{ tok.raw }}</span></template></pre>
  <pre
    v-else
    :class="[
      'overflow-auto whitespace-pre-wrap break-all font-mono text-foreground/80 leading-snug',
      props.compact ? 'text-[11px]' : 'text-xs',
      props.maxHeightClass,
    ]"
    >{{ parsed?.kind === 'raw' ? parsed.text : body }}</pre>
</template>
