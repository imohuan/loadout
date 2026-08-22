<script setup lang="ts">
// 原始上游错误响应展示：折叠块 + JSON 高亮。
// 后端 error_body 截断到 8KB，可能是合法 JSON（最常见）、HTML、纯文本。
// - 解析为 JSON：自动 pretty-print（2 空格）+ 关键词高亮（key、string、number、null）。
// - 解析失败：原样展示，外面用 <pre>。
// 默认折叠（响应可能很长），点「查看原始响应」展开；超过 600 字符使用 toggle，
// 短于 600 字符直接展开避免冗余。
import { computed, ref } from 'vue'

const props = withDefaults(
  defineProps<{
    body: string
    label?: string
    /** true=attempt 行内展示，更紧凑（小号字、内边距小、不换 label） */
    compact?: boolean
  }>(),
  { label: '上游原始响应', compact: false },
)

// 长度阈值：短于等于此值的 body 直接展开（折叠按钮省一次点击）。
// 上游错误 JSON 通常 ≥ 200B，太短的（"Internal Server Error" 这种）也直接展示。
const COLLAPSE_THRESHOLD = 600

const parsed = computed(() => {
  const raw = props.body
  if (!raw) return null
  const trimmed = raw.trim()
  if (!trimmed) return null
  // 仅在明显是 JSON（以 { 或 [ 开头）时尝试解析；HTML/纯文本直接走 raw 分支，
  // 避免 JSON.parse 错误把 HTML/纯文本也吞到 raw 分支处理。
  if (trimmed[0] !== '{' && trimmed[0] !== '[') {
    return { kind: 'raw' as const, text: raw }
  }
  try {
    const value = JSON.parse(trimmed)
    return { kind: 'json' as const, value }
  } catch {
    return { kind: 'raw' as const, text: raw }
  }
})

// pretty JSON：2 空格缩进，最后不挂多余尾巴；用 JSON.stringify 避免依赖编辑器格式化。
const prettyJson = computed(() => {
  if (!parsed.value || parsed.value.kind !== 'json') return ''
  try {
    return JSON.stringify(parsed.value.value, null, 2)
  } catch {
    return ''
  }
})

const shouldCollapse = computed(() => props.body.length > COLLAPSE_THRESHOLD)
const expanded = ref(false)
const showFull = computed(() => !shouldCollapse.value || expanded.value)

// 截断预览：展开前的折叠态只展示前 600 字符 + 省略号，避免列表详情行把屏幕撑高。
const preview = computed(() => {
  if (!shouldCollapse.value) return ''
  return props.body.length > COLLAPSE_THRESHOLD
    ? props.body.slice(0, COLLAPSE_THRESHOLD) + '...'
    : props.body
})

// 复制到剪贴板：方便贴到 bug 报告里。
async function copyBody() {
  if (!props.body) return
  try {
    await navigator.clipboard.writeText(props.body)
    copied.value = true
    setTimeout(() => {
      copied.value = false
    }, 1500)
  } catch {
    // 老浏览器 / 非安全上下文：Clipboard API 不可用时静默忽略，不打扰用户。
  }
}
const copied = ref(false)

// JSON 行级高亮：把 pretty 字符串按行扫，每行用 token 高亮。
// 不引入额外依赖（highlighter.js 太重），手写一个够用的：
// - key（含冒号结尾）：蓝/紫
// - string（双引号包住）：绿
// - number、true/false/null：橙/灰
function highlightJson(text: string): { raw: string; cls?: string }[] {
  // 用 regex 拆分而非字符级渲染，每行单独处理，颜色块以 token 为单位。
  // match 顺序：string > number > boolean > null > key（key 仅在 ":" 前的 string）
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
  <div
    :class="[
      'rounded-md border border-border/60 bg-muted/40',
      props.compact ? 'mt-1 px-2 py-1.5 text-[11px]' : 'mt-2 px-3 py-2 text-xs',
    ]"
  >
    <div class="flex items-center justify-between gap-2">
      <span class="text-[10px] font-medium uppercase tracking-wide text-muted-foreground">
        {{ props.label }}
      </span>
      <div class="flex items-center gap-2 text-[10px]">
        <span class="text-muted-foreground">{{ props.body.length }} 字符</span>
        <button
          v-if="shouldCollapse"
          type="button"
          class="rounded border border-border/60 px-1.5 py-0.5 text-foreground hover:bg-muted"
          @click="expanded = !expanded"
        >
          {{ expanded ? '折叠' : '展开' }}
        </button>
        <button
          type="button"
          class="rounded border border-border/60 px-1.5 py-0.5 text-foreground hover:bg-muted"
          @click="copyBody"
        >
          {{ copied ? '已复制' : '复制' }}
        </button>
      </div>
    </div>
    <!-- JSON 分支：高亮渲染 -->
    <pre
      v-if="highlighted"
      :class="[
        'mt-1 max-h-80 overflow-auto whitespace-pre break-all font-mono leading-snug',
        props.compact ? 'max-h-40' : '',
      ]"
    ><template v-for="(tok, i) in highlighted" :key="i"><span v-if="tok.cls" :class="tok.cls">{{ tok.raw }}</span><span v-else>{{ tok.raw }}</span></template></pre>
    <!-- 折叠预览：仅短于阈值时跳过 -->
    <pre
      v-else-if="parsed?.kind === 'raw' && !showFull"
      class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap break-all font-mono text-foreground/80 leading-snug"
    >{{ preview }}</pre>
    <pre
      v-else-if="parsed?.kind === 'raw'"
      class="mt-1 max-h-80 overflow-auto whitespace-pre-wrap break-all font-mono text-foreground/80 leading-snug"
    >{{ parsed.text }}</pre>
  </div>
</template>
