<script setup lang="ts">
/**
 * 流式 markdown 主文本块。
 *
 * 设计参考：backup/codex-base-ui/web/src/components/chat/TextBlock.vue
 * 区别：
 * 1. 颜色用项目 shadcn-vue / Tailwind token 风格（默认暗/亮双适配），未沿用 Tailwind v3 gray-*；
 * 2. 容错：marked 解析失败降级为 escapeHtml，避免半截流式输出炸浏览器；
 * 3. highlight.js 仅按需引入 core + 必要语言，避免全量打包。
 */
import { computed } from 'vue'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/common'

const props = withDefaults(
  defineProps<{
    /** 已累加的 markdown 文本（可流式追加） */
    text: string
    /** 是否仍在流式中；true 时尾巴显示一个脉冲光标 */
    streaming?: boolean
    /** 渲染失败时的降级占位（默认「（无内容）」） */
    emptyText?: string
  }>(),
  { streaming: false, emptyText: '（无内容）' },
)

// marked 配置：开 GFM、行内 HTML 不解析、围栏交给 highlight.js。
marked.setOptions({
  gfm: true,
  breaks: true,
})

// 高亮回调：把围栏 code 里的 lang 喂给 highlight.js。
const renderer = new marked.Renderer()
renderer.code = (codeObj) => {
  const code = codeObj.text
  const lang = (codeObj.lang || '').trim().toLowerCase()
  let highlighted: string
  let langClass = ''
  try {
    if (lang && hljs.getLanguage(lang)) {
      highlighted = hljs.highlight(code, { language: lang, ignoreIllegals: true }).value
      langClass = ` language-${lang}`
    } else {
      highlighted = hljs.highlightAuto(code).value
      langClass = ' language-plaintext'
    }
  } catch {
    highlighted = escapeHtml(code)
  }
  return `<pre class="hljs rounded-md bg-zinc-900/95 p-3 overflow-x-auto text-xs leading-relaxed"><code class="hljs${langClass}">${highlighted}</code></pre>`
}
// 把渲染器实例交给 marked（marked v18 接受 Renderer 实例）
marked.use({ renderer })

const ALLOWED_TAGS = [
  'h1',
  'h2',
  'h3',
  'h4',
  'h5',
  'h6',
  'p',
  'br',
  'hr',
  'ul',
  'ol',
  'li',
  'blockquote',
  'pre',
  'code',
  'span',
  'table',
  'thead',
  'tbody',
  'tr',
  'th',
  'td',
  'strong',
  'em',
  'del',
  's',
  'a',
  'img',
  'div',
]
const ALLOWED_ATTR = ['href', 'src', 'alt', 'title', 'class', 'id', 'target', 'rel']

const safeHtml = computed<string>(() => {
  const raw = props.text
  if (!raw || raw.trim().length === 0) return ''
  let html: string
  try {
    html = marked.parse(raw, { async: false }) as string
  } catch {
    // 流式中 markdown 经常围栏未闭合；降级到 escaped text，保留可读性
    html = `<pre class="whitespace-pre-wrap font-mono text-xs leading-relaxed">${escapeHtml(raw)}</pre>`
  }
  // DOMPurify 在某些浏览器/SSR 边界下会抛错，必须兜底
  try {
    return DOMPurify.sanitize(html, {
      ALLOWED_TAGS,
      ALLOWED_ATTR,
      ALLOW_DATA_ATTR: false,
      FORBID_TAGS: ['style', 'script', 'iframe', 'object'],
      FORBID_ATTR: ['onerror', 'onload', 'onclick', 'onmouseover', 'style'],
    })
  } catch {
    return escapeHtml(raw)
  }
})

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
</script>

<template>
  <div class="text-sm leading-relaxed">
    <div
      v-if="safeHtml"
      class="markdown-body prose prose-sm dark:prose-invert max-w-none break-words"
      v-html="safeHtml"
    />
    <p v-else class="text-muted-foreground italic">{{ emptyText }}</p>
    <!-- 流式尾巴：脉冲光标；只渲染一个轻量矩形，避免占用布局 -->
    <span
      v-if="streaming"
      class="ml-0.5 inline-block h-4 w-2 translate-y-0.5 bg-foreground/70 align-text-bottom animate-pulse"
      aria-label="流式中"
    />
  </div>
</template>

<style>
/* 借参考 UI 的 markdown-body 同款紧凑边距，但用项目色 token。 */
.markdown-body {
  -ms-text-size-adjust: 100%;
  -webkit-text-size-adjust: 100%;
  margin: 0;
  font-weight: 400;
  word-wrap: break-word;
}
.markdown-body > *:first-child {
  margin-top: 0 !important;
}
.markdown-body > *:last-child {
  margin-bottom: 0 !important;
}

.markdown-body h1,
.markdown-body h2,
.markdown-body h3,
.markdown-body h4,
.markdown-body h5,
.markdown-body h6 {
  margin-top: 24px;
  margin-bottom: 16px;
  font-weight: 600;
  line-height: 1.25;
}
.markdown-body h1 {
  padding-bottom: 0.3em;
  font-size: 1.25em;
  border-bottom: 1px solid hsl(var(--border));
}
.markdown-body h2 {
  padding-bottom: 0.3em;
  font-size: 1.1em;
  border-bottom: 1px solid hsl(var(--border));
}
.markdown-body h3 {
  font-size: 1em;
}
.markdown-body h4 {
  font-size: 0.95em;
}
.markdown-body h5 {
  font-size: 0.9em;
}
.markdown-body h6 {
  font-size: 0.85em;
  color: hsl(var(--muted-foreground));
}

.markdown-body p {
  margin: 0 0 10px;
}
.markdown-body blockquote {
  margin: 0 0 16px;
  padding: 0 1em;
  color: hsl(var(--muted-foreground));
  border-left: 0.25em solid hsl(var(--border));
}
.markdown-body blockquote > :first-child {
  margin-top: 0;
}
.markdown-body blockquote > :last-child {
  margin-bottom: 0;
}

.markdown-body ul,
.markdown-body ol {
  margin: 0 0 16px;
  padding-left: 2em;
}
.markdown-body ul {
  list-style-type: disc;
}
.markdown-body ol {
  list-style-type: decimal;
}
.markdown-body ul ul,
.markdown-body ol ul {
  list-style-type: circle;
}
.markdown-body ul ul,
.markdown-body ul ol,
.markdown-body ol ol,
.markdown-body ol ul {
  margin-top: 0;
  margin-bottom: 0;
}
.markdown-body li {
  word-wrap: break-word;
}
.markdown-body li + li {
  margin-top: 0.25em;
}
.markdown-body li > p {
  margin-top: 16px;
}

.markdown-body table {
  border-spacing: 0;
  border-collapse: collapse;
  display: block;
  width: max-content;
  max-width: 100%;
  overflow: auto;
  font-variant: tabular-nums;
  margin-bottom: 16px;
}
.markdown-body table th {
  font-weight: 600;
}
.markdown-body table th,
.markdown-body table td {
  padding: 6px 13px;
  border: 1px solid hsl(var(--border));
}
.markdown-body table tr {
  background-color: transparent;
  border-top: 1px solid hsl(var(--border));
}
.markdown-body table tr:nth-child(2n) {
  background-color: hsl(var(--muted) / 0.5);
}

.markdown-body code,
.markdown-body kbd,
.markdown-body pre,
.markdown-body samp {
  font-family: ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, 'Liberation Mono', monospace;
  font-size: 12px;
}
.markdown-body code,
.markdown-body tt {
  padding: 0.2em 0.4em;
  margin: 0;
  font-size: 85%;
  white-space: break-spaces;
  background-color: hsl(var(--muted));
  border-radius: 6px;
}
.markdown-body pre {
  margin-top: 0;
  margin-bottom: 16px;
  padding: 16px;
  overflow: auto;
  font-size: 85%;
  line-height: 1.45;
  color: #f8f8f2;
  background-color: #1e1e1e;
  border-radius: 6px;
  word-wrap: normal;
}
.markdown-body pre code,
.markdown-body pre tt {
  display: inline;
  padding: 0;
  margin: 0;
  overflow: visible;
  line-height: inherit;
  word-wrap: normal;
  background-color: transparent;
  border: 0;
}

.markdown-body hr {
  box-sizing: content-box;
  height: 1px;
  margin: 12px 0;
  padding: 0;
  background-color: hsl(var(--border));
  border: 0;
}

.markdown-body a {
  color: hsl(var(--primary));
  text-decoration: none;
}
.markdown-body a:hover {
  text-decoration: underline;
}
.markdown-body a:not([href]) {
  color: inherit;
  text-decoration: none;
}

.markdown-body img {
  max-width: 100%;
  border-style: none;
}

.markdown-body details summary {
  cursor: pointer;
}

/* highlight.js github-dark 配色（简化版），与暗背景 pre 协调 */
.markdown-body .hljs {
  color: #c9d1d9;
  background: transparent;
}
.markdown-body .hljs-comment,
.markdown-body .hljs-quote {
  color: #8b949e;
  font-style: italic;
}
.markdown-body .hljs-keyword,
.markdown-body .hljs-selector-tag,
.markdown-body .hljs-section,
.markdown-body .hljs-title,
.markdown-body .hljs-name {
  color: #ff7b72;
}
.markdown-body .hljs-string,
.markdown-body .hljs-attr {
  color: #a5d6ff;
}
.markdown-body .hljs-number,
.markdown-body .hljs-literal,
.markdown-body .hljs-symbol,
.markdown-body .hljs-bullet {
  color: #79c0ff;
}
.markdown-body .hljs-built_in,
.markdown-body .hljs-type {
  color: #ffa657;
}
.markdown-body .hljs-variable,
.markdown-body .hljs-template-variable {
  color: #d2a8ff;
}
.markdown-body .hljs-meta {
  color: #8b949e;
}
</style>
