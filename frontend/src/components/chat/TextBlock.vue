<template>
  <div class="text-sm text-gray-800 leading-relaxed">
    <template v-for="(node, i) in blocks" :key="i">
      <CodeBlock
        v-if="node.type === 'code'"
        :code="node.text"
        :lang="node.lang"
      />
      <div
        v-else
        class="markdown-body prose prose-sm max-w-none break-words"
        v-html="node.html"
      />
    </template>
    <span
      v-if="isStreaming"
      class="inline-block w-2 h-4 bg-gray-600 ml-0.5 align-text-bottom animate-pulse"
    ></span>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue';
import { marked } from 'marked';
import DOMPurify from 'dompurify';
import CodeBlock from './CodeBlock.vue';
import type { NormalizedMessage } from '@/lib/chatTypes';

const props = defineProps<{
  message: NormalizedMessage;
}>();

type Block =
  | { type: 'code'; text: string; lang: string }
  | { type: 'html'; html: string };

const blocks = computed<Block[]>(() => {
  const raw = props.message.content;
  if (!raw) return [];
  const tokens = marked.lexer(raw);
  const out: Block[] = [];
  let buffer: string[] = [];

  function flushBuffer() {
    if (buffer.length === 0) return;
    const md = buffer.join('\n\n');
    const html = marked.parse(md, { async: false }) as string;
    out.push({ type: 'html', html: DOMPurify.sanitize(html, {
      ALLOWED_TAGS: [
        'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
        'p', 'br', 'hr',
        'ul', 'ol', 'li',
        'blockquote', 'pre', 'code', 'span',
        'table', 'thead', 'tbody', 'tr', 'th', 'td',
        'strong', 'em', 'del', 's',
        'a', 'img',
        'div',
        'input',
      ],
      ALLOWED_ATTR: ['href', 'src', 'alt', 'title', 'class', 'id', 'checked', 'disabled', 'type'],
    }) });
    buffer = [];
  }

  for (const tok of tokens) {
    if (tok.type === 'code') {
      flushBuffer();
      out.push({ type: 'code', text: tok.text, lang: tok.lang || '' });
    } else {
      buffer.push(tok.raw);
    }
  }
  flushBuffer();
  return out;
});

const isStreaming = computed(() => !props.message.id?.startsWith('msg-') && props.message.kind !== 'text');
</script>

<style>
.markdown-body {
  -ms-text-size-adjust: 100%;
  -webkit-text-size-adjust: 100%;
  margin: 0;
  font-weight: 400;
  word-wrap: break-word;
}

.markdown-body > *:first-child { margin-top: 0 !important; }
.markdown-body > *:last-child  { margin-bottom: 0 !important; }

.markdown-body h1, .markdown-body h2, .markdown-body h3,
.markdown-body h4, .markdown-body h5, .markdown-body h6 {
  margin-top: 24px;
  margin-bottom: 16px;
  font-weight: 600;
  line-height: 1.25;
}

.markdown-body h1 {
  padding-bottom: 0.3em;
  font-size: 1.25em;
  border-bottom: 1px solid #e5e7eb;
}
.markdown-body h2 {
  padding-bottom: 0.3em;
  font-size: 1.1em;
  border-bottom: 1px solid #e5e7eb;
}
.markdown-body h3 { font-size: 1em; }
.markdown-body h4 { font-size: 0.9em; }
.markdown-body h5 { font-size: 0.85em; }
.markdown-body h6 {
  font-size: 0.8em;
  color: #6b7280;
}

.markdown-body p { margin: 0 0 10px; }
.markdown-body blockquote {
  margin: 0 0 16px;
  padding: 0 1em;
  color: #6b7280;
  border-left: 0.25em solid #d1d5db;
}
.markdown-body blockquote > :first-child { margin-top: 0; }
.markdown-body blockquote > :last-child  { margin-bottom: 0; }

.markdown-body ul, .markdown-body ol {
  margin: 0 0 16px;
  padding-left: 2em;
}
.markdown-body ul { list-style-type: disc; }
.markdown-body ol { list-style-type: decimal; }
.markdown-body ul ul,
.markdown-body ol ul { list-style-type: circle; }
.markdown-body ul ul, .markdown-body ul ol,
.markdown-body ol ol, .markdown-body ol ul {
  margin-top: 0;
  margin-bottom: 0;
}
.markdown-body li { word-wrap: break-word; }
.markdown-body li + li { margin-top: 0.25em; }
.markdown-body li > p { margin-top: 16px; }

.markdown-body ol ol,
.markdown-body ul ol { list-style-type: lower-roman; }
.markdown-body ul ul ol,
.markdown-body ul ol ol,
.markdown-body ol ul ol,
.markdown-body ol ol ol { list-style-type: lower-alpha; }

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
.markdown-body table th { font-weight: 600; }
.markdown-body table th, .markdown-body table td {
  padding: 6px 13px;
  border: 1px solid #d1d5db;
}
.markdown-body table tr {
  background-color: #ffffff;
  border-top: 1px solid #e5e7eb;
}
.markdown-body table tr:nth-child(2n) {
  background-color: #f6f8fa;
}
.markdown-body table td > :last-child { margin-bottom: 0; }

.markdown-body img {
  max-width: 100%;
  border-style: none;
}
.markdown-body img[align=right] { padding-left: 20px; }
.markdown-body img[align=left]  { padding-right: 20px; }

.markdown-body code, .markdown-body kbd,
.markdown-body pre, .markdown-body samp {
  font-family: ui-monospace, SFMono-Regular, SF Mono, Menlo, Consolas, "Liberation Mono", monospace;
  font-size: 12px;
}
.markdown-body code, .markdown-body tt {
  padding: 0.2em 0.4em;
  margin: 0;
  font-size: 85%;
  white-space: break-spaces;
  background-color: #f6f8fa;
  border-radius: 6px;
}
.markdown-body code br, .markdown-body tt br { display: none; }
.markdown-body del code { text-decoration: inherit; }
.markdown-body samp { font-size: 85%; }
.markdown-body pre code { font-size: 100%; }
.markdown-body pre > code {
  padding: 0;
  margin: 0;
  word-break: normal;
  white-space: pre;
  background: transparent;
  border: 0;
}

.markdown-body pre {
  margin-top: 0;
  margin-bottom: 16px;
  padding: 16px;
  overflow: auto;
  font-size: 85%;
  line-height: 1.45;
  color: #1f2328;
  background-color: #f6f8fa;
  border-radius: 6px;
  word-wrap: normal;
}
.markdown-body pre code, .markdown-body pre tt {
  display: inline;
  padding: 0;
  margin: 0;
  overflow: visible;
  line-height: inherit;
  word-wrap: normal;
  background-color: transparent;
  border: 0;
}

.markdown-body kbd {
  display: inline-block;
  padding: 3px 5px;
  font-size: 11px;
  line-height: 10px;
  color: #1f2328;
  vertical-align: middle;
  background-color: #f6f8fa;
  border: solid 1px rgba(175, 184, 193, 0.2);
  border-bottom-color: #afb8c133;
  border-radius: 6px;
  box-shadow: inset 0 -1px 0 #afb8c133;
}

.markdown-body hr {
  box-sizing: content-box;
  height: 1px;
  margin: 12px 0;
  padding: 0;
  background-color: #d1d9e0;
  border: 0;
}

.markdown-body a {
  color: #0969da;
  text-decoration: none;
}
.markdown-body a:hover { text-decoration: underline; }
.markdown-body a:not([href]) { color: inherit; text-decoration: none; }

.markdown-body b, .markdown-body strong { font-weight: 600; }
.markdown-body dfn { font-style: italic; }
.markdown-body mark {
  background-color: #fff8c5;
  color: #1f2328;
}
.markdown-body small { font-size: 90%; }
.markdown-body sub, .markdown-body sup {
  font-size: 75%;
  line-height: 0;
  position: relative;
  vertical-align: baseline;
}
.markdown-body sub { bottom: -0.25em; }
.markdown-body sup { top: -0.5em; }
.markdown-body abbr[title] {
  border-bottom: none;
  text-decoration: underline dotted;
}
.markdown-body figure { margin: 1em 40px; }
.markdown-body figcaption { display: block; }

.markdown-body del, .markdown-body s { text-decoration: line-through; }

.markdown-body details summary { cursor: pointer; }
</style>