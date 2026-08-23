# request-log 非流式响应可视化实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让「响应可视化」折叠面板同时支持流式与非流式两种请求：流式按 SSE 逐 chunk 提取，非流式直接解析完整 JSON 响应体的 message，共用同一套渲染链。

**Architecture:** 纯前端改动，后端零改动。核心是在 `frontend/src/lib/parseSSE.ts` 新增 `parseNonStreamBody(body): ParsedStream`，输出结构与 `parseStreamBody` 完全同构（chunks/contentAccum/reasoningAccum/toolCalls/usage/isDone/finishReason），非流式时合成 1 条 parsed chunk（rawJsonString=全文）。`ChatPreviewPanel` 按 `props.stream` 分流解析路径，`showPanel` 从「仅流式」放宽为「有解析产物」。`RequestLogDetailView` 去掉两处 `log.stream` gate，`v-if` 改用 `streamView.visible`，折叠标题改「响应可视化」。

**Tech Stack:** Vue 3 `<script setup>`、TypeScript、shadcn-vue（CDN 全局组件）、零新依赖（不引入 vitest/测试框架，验证走真实环境 + 临时 node 脚本）。

---

## 决策记录（用户已拍板，实现时勿改）

| 维度 | 决策 | 说明 |
|---|---|---|
| 面板标题 | 「流式响应可视化」→ **「响应可视化」** | 两种模式都支持，原名误导；「（已截断）」标记保留 |
| 非流式时间轴 | **合成 1 条完整响应** | chunks=1，统计行显示 `chunks 1 · parsed 1`，时间轴展开可见全文 JSON |
| 解析器位置 | parseSSE.ts 新增 `parseNonStreamBody` | 与 parseStreamBody 同构复用 ParsedStream 类型，不新增文件 |
| 依赖 | 零新依赖 | 不加 vitest；parseNonStreamBody 用临时 node 脚本验证 + dev server 真实页面验证 |
| 后端 | 零改动 | 非流式 `response_json.body` 已是完整上游 JSON 字符串（service.go:264），前端直接解析 |
| 失败/拦截请求 | **恒渲染 + Panel 内容判定（用户拍板）** | 请求参数里必有 messages 历史，不管请求成功/失败/被拦截都要显示面板（至少请求区可见）。View 层不再按 stream/result 过滤，折叠项恒渲染；ChatPreviewPanel 内部按「有请求消息或有响应内容」决定显示预览或空态 |

---

## 背景事实（已核实，代码位置）

1. **后端非流式 body 已是完整 JSON 字符串**：`plugins/request-log/service.go:264` `Body: redactBody(string(ap.Response.Body))` —— 非流式 2xx 把上游响应体原样存字符串；流式才是 SSE 原文（service.go:386-395 `buf.String()`）。→ 前端 `response_json.body` 永远是 string，`extractResponseBody`（parseSSE.ts:640-645）的「对象分支」是防御性的，实际不触发，无需改。
2. **三道 gate 卡死非流式**：
   - `RequestLogDetailView.vue:40`：`log.stream !== true` → `streamView.visible = false`
   - `RequestLogDetailView.vue:256`：`<AccordionItem v-if="log.stream">` —— 非流式连折叠面板都不渲染（这是真正的入口 gate；`streamView.visible` 在模板中未被使用，是历史遗留）
   - `ChatPreviewPanel.vue:277`：`showPanel = computed(() => props.stream === true)` —— 即使面板开了也显示空态
3. **parsed 只走 SSE**：`ChatPreviewPanel.vue:224` `parseStreamBody(extractResponseBody(...))`。非流式完整 JSON 无 `data:` 行 → detectStreamFormat 兜底 chat → chunks=[] → contentAccum='' → responseMessages=[] → 响应区空白。
4. **渲染链复用点（无需改动）**：`ChatPreviewPanel.vue:226-258` responseMessages 把 contentAccum→'text'(done) 或 'stream'、reasoningAccum→thinking 块、toolCalls→tool_use 卡片；`stats`（:262-275）读 chunks/contentChars/reasoningChars/toolCallCount/format/isDone/finishReason/usage —— parseNonStreamBody 填同构字段即可全复用。
5. **时间轴空态兜底**：`StreamChunkTimeline.vue:169` chunks=[] 显示「无 chunk 数据。」不炸；合成 1 条 chunk 时 summary（:29-43）显示 `1 chunks`，展开渲染 rawJsonString 全文（:163-167）。非流式时建议传 `collapsed-title="完整响应 · 展开看原文"`（一行 props，语义更准确，见 Task 2）。
6. **非流式三种格式**（model-gateway transparent proxy 对齐，参考 vision_v2/stream.go 与 parseSSE.ts 头注释）：
   - chat：`{ choices: [{ message: { content, reasoning_content, tool_calls[] }, finish_reason }], usage }`；content 可能是 string 或数组（新版多模态，`[{type:'text',text},...]`）
   - claude：`{ content: [{ type:'text', text } | { type:'thinking', thinking } | { type:'tool_use', id, name, input }], stop_reason, usage }`
   - responses：`{ output: [{ type:'message', content:[{type:'output_text', text}] } | { type:'reasoning', summary:[{type:'summary_text', text}] } | { type:'function_call', id, name, arguments }], status, usage }`
7. **请求区渲染与模式无关**：`ChatPreviewPanel` 的 requestMessages（:70-185）从 request_json 解析，流式/非流式共用，零改动。
8. **done 语义**：非流式 2xx 收尾即完成 → `isDone=true`，contentAccum 走 'text' 分支（id=`msg-resp-content`，无光标闪烁），与流式 [DONE] 后的渲染一致（ChatPreviewPanel.vue:234-239）。
9. **参考先例**：`useModelTest.ts:42-59` extractTestDelta 已有 `choices[0].message.content` 提取（非流式字段），但它是按 mode 的单行提取，与本次聚合语义不同，不直接复用。
10. **图片脱敏**：非流式 content 数组里的 image_url 已被后端 redactBody 换成 `[image: mime, N字节]` 占位文本（service.go:851-870）；与请求区 extractContent（ChatPreviewPanel.vue:44-68）同规则——只拼 text 部分，图片忽略（占位符本身不是 http/data 开头，不会进 images 列表，安全）。

---

## 实现

### Task 1: parseSSE.ts 新增 `parseNonStreamBody`

**Files:**
- Modify: `frontend/src/lib/parseSSE.ts`（文件末尾，`looksLikeSSE` 之后追加；文件头注释补充非流式说明）

**Step 1: 新增导出函数**

```ts
/**
 * 解析非流式（完整 JSON）响应正文，提取消息内容。
 * 与 parseStreamBody 输出结构完全同构，渲染链（ChatPreviewPanel）直接复用。
 * 支持三种协议格式的完整响应（与流式格式探测对齐）：
 *   1. chat:      { choices:[{ message:{ content, reasoning_content, tool_calls }, finish_reason }], usage }
 *   2. claude:    { content:[{ type:'text'|'thinking'|'tool_use' }], stop_reason, usage }
 *   3. responses: { output:[{ type:'message'|'reasoning'|'function_call' }], status, usage }
 * 非流式合成 1 条 parsed chunk（rawJsonString=全文），展开时间轴可见响应原文。
 * 解析失败/空 body：返回 emptyStream（与 parseStreamBody 同款兜底），绝不抛错。
 */
export function parseNonStreamBody(body: string): ParsedStream {
  if (typeof body !== 'string' || body.length === 0) return emptyStream('chat')
  let json: unknown
  try {
    json = JSON.parse(body)
  } catch {
    return emptyStream('chat')
  }
  if (!json || typeof json !== 'object') return emptyStream('chat')
  const obj = json as Record<string, unknown>

  // 格式探测（与 detectStreamFormat 的字段语义对齐）；rawBody 透传给三个解析器保真原文。
  // 【执行修订】claude 判定只看 content 数组 / stop_reason（Anthropic 非流式响应两者恒有），
  // 不认顶层 type 字段——否则 {"type":"error"} 之类错误体也会被误判 claude（node 脚本实测发现，
  // 原 `typeof obj.type === 'string'` 条件过宽，已收紧）。
  if (Array.isArray(obj.output)) return parseNonStreamResponses(obj, body)
  if (Array.isArray(obj.choices)) return parseNonStreamChat(obj, body)
  if (Array.isArray(obj.content) || typeof obj.stop_reason === 'string') {
    return parseNonStreamClaude(obj, body)
  }
  return emptyStream('chat')
}
```

**Step 2: 三个格式的内部实现（同一文件内私有函数）**

```ts
// ---- 非流式：chat.completion 完整响应 ----

function parseNonStreamChat(obj: Record<string, unknown>, rawBody: string): ParsedStream {
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let finishReason: string | null | undefined
  let id: string | undefined
  let model: string | undefined
  const toolCalls: ParsedToolCall[] = []

  const choice0 = (Array.isArray(obj.choices) ? obj.choices[0] : undefined) as Record<string, unknown> | undefined
  if (choice0 && typeof choice0 === 'object') {
    const msg = choice0.message as Record<string, unknown> | undefined
    if (msg && typeof msg === 'object') {
      contentAccum += extractNonStreamText(msg.content)
  // 与 parseSSE.ts:336-339 的流式三别名对齐：reasoning_content / reasoning / reasoning_text
  const reasoningRaw =
    (msg.reasoning_content as unknown) ??
    (msg.reasoning as unknown) ??
    (msg.reasoning_text as unknown)
  if (typeof reasoningRaw === 'string') reasoningAccum += reasoningRaw
      if (Array.isArray(msg.tool_calls)) {
        for (const tcRaw of msg.tool_calls) {
          if (!tcRaw || typeof tcRaw !== 'object') continue
          const tc = tcRaw as Record<string, unknown>
          const fn = tc.function as Record<string, unknown> | undefined
          const argsRaw = typeof fn?.arguments === 'string' ? fn.arguments : ''
          let argsParsed: unknown = null
          try {
            argsParsed = argsRaw ? JSON.parse(argsRaw) : null
          } catch {
            argsParsed = null
          }
          toolCalls.push({
            index: toolCalls.length,
            id: typeof tc.id === 'string' ? tc.id : '',
            type: typeof tc.type === 'string' ? tc.type : 'function',
            name: typeof fn?.name === 'string' ? fn.name : 'unknown',
            argumentsRaw: argsRaw,
            argumentsParsed: argsParsed,
          })
        }
      }
    }
    if (typeof choice0.finish_reason === 'string') finishReason = choice0.finish_reason
  }
  if (typeof obj.id === 'string') id = obj.id
  if (typeof obj.model === 'string') model = obj.model
  const u = obj.usage as StreamUsage | undefined
  if (u && typeof u === 'object') usage = u

  const fields: ParsedChunk['fields'] = {}
  if (id !== undefined) fields.id = id
  if (model !== undefined) fields.model = model
  if (contentAccum) fields.contentDelta = contentAccum
  if (reasoningAccum) fields.reasoningDelta = reasoningAccum
  if (finishReason !== undefined) fields.finishReason = finishReason
  if (usage) fields.usage = usage
  if (choice0 && typeof choice0.index === 'number') fields.choiceIndex = choice0.index

  chunks.push({
    index: 1,
    parsed: true,
    format: 'chat',
    rawJsonString: rawBody, // 保真原文（不 JSON.stringify 重排），展开时间轴可见原始响应
    fields,
    charOffset: 0,
  })
  return { chunks, contentAccum, reasoningAccum, toolCalls, format: 'chat', usage, isDone: true, finishReason, parsedCount: 1 }
}

// ---- 非流式：claude/messages 完整响应 ----

function parseNonStreamClaude(obj: Record<string, unknown>, rawBody: string): ParsedStream {
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let finishReason: string | null | undefined
  let id: string | undefined
  let model: string | undefined
  const toolCalls: ParsedToolCall[] = []

  if (Array.isArray(obj.content)) {
    for (const blockRaw of obj.content) {
      if (!blockRaw || typeof blockRaw !== 'object') continue
      const block = blockRaw as Record<string, unknown>
      if (block.type === 'text' && typeof block.text === 'string') {
        contentAccum += block.text
      } else if (block.type === 'thinking' && typeof block.thinking === 'string') {
        reasoningAccum += block.thinking
      } else if (block.type === 'tool_use') {
        // 与 chat/responses 的语义对齐：argumentsParsed 一律由字符串 parse 得出。
        // input 为对象时先序列化再 parse（保一致），parse 失败落 null + 原文保留。
        let inputStr = ''
        try {
          inputStr = input == null ? '' : typeof input === 'string' ? input : JSON.stringify(input)
        } catch {
          inputStr = ''
        }
        let inputParsed: unknown = null
        try {
          inputParsed = inputStr ? JSON.parse(inputStr) : null
        } catch {
          inputParsed = null
        }
        toolCalls.push({
          index: toolCalls.length,
          id: typeof block.id === 'string' ? block.id : '',
          type: 'tool_use',
          name: typeof block.name === 'string' ? block.name : 'unknown',
          argumentsRaw: inputStr,
          argumentsParsed: inputParsed,
        })
      }
    }
  }
  if (typeof obj.stop_reason === 'string') finishReason = obj.stop_reason
  if (typeof obj.id === 'string') id = obj.id
  if (typeof obj.model === 'string') model = obj.model
  const u = obj.usage as StreamUsage | undefined
  if (u && typeof u === 'object') usage = u

  const fields: ParsedChunk['fields'] = {}
  if (id !== undefined) fields.id = id
  if (model !== undefined) fields.model = model
  if (contentAccum) fields.contentDelta = contentAccum
  if (reasoningAccum) fields.reasoningDelta = reasoningAccum
  if (finishReason !== undefined) fields.finishReason = finishReason
  if (usage) fields.usage = usage

  chunks.push({
    index: 1,
    parsed: true,
    format: 'claude',
    rawJsonString: rawBody, // 保真原文
    fields,
    charOffset: 0,
  })
  return { chunks, contentAccum, reasoningAccum, toolCalls, format: 'claude', usage, isDone: true, finishReason, parsedCount: 1 }
}

// ---- 非流式：responses API 完整响应 ----

function parseNonStreamResponses(obj: Record<string, unknown>, rawBody: string): ParsedStream {
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let finishReason: string | null | undefined
  let id: string | undefined
  let model: string | undefined
  const toolCalls: ParsedToolCall[] = []

  if (Array.isArray(obj.output)) {
    for (const itemRaw of obj.output) {
      if (!itemRaw || typeof itemRaw !== 'object') continue
      const item = itemRaw as Record<string, unknown>
      if (item.type === 'message' && Array.isArray(item.content)) {
        for (const part of item.content) {
          if (!part || typeof part !== 'object') continue
          const p = part as Record<string, unknown>
          if (p.type === 'output_text' && typeof p.text === 'string') contentAccum += p.text
        }
      } else if (item.type === 'reasoning' && Array.isArray(item.summary)) {
        for (const seg of item.summary) {
          if (!seg || typeof seg !== 'object') continue
          const s = seg as Record<string, unknown>
          if (typeof s.text === 'string') reasoningAccum += s.text
        }
      } else if (item.type === 'function_call') {
        const argsRaw = typeof item.arguments === 'string' ? item.arguments : ''
        let argsParsed: unknown = null
        try {
          argsParsed = argsRaw ? JSON.parse(argsRaw) : null
        } catch {
          argsParsed = null
        }
        toolCalls.push({
          index: toolCalls.length,
          id: typeof item.id === 'string' ? item.id : '',
          type: 'function_call',
          name: typeof item.name === 'string' ? item.name : 'unknown',
          argumentsRaw: argsRaw,
          argumentsParsed: argsParsed,
        })
      }
    }
  }
  if (typeof obj.status === 'string') finishReason = obj.status
  if (typeof obj.id === 'string') id = obj.id
  if (typeof obj.model === 'string') model = obj.model
  const u = obj.usage as StreamUsage | undefined
  if (u && typeof u === 'object') usage = u

  const fields: ParsedChunk['fields'] = {}
  if (id !== undefined) fields.id = id
  if (model !== undefined) fields.model = model
  if (contentAccum) fields.contentDelta = contentAccum
  if (reasoningAccum) fields.reasoningDelta = reasoningAccum
  if (finishReason !== undefined) fields.finishReason = finishReason
  if (usage) fields.usage = usage

  chunks.push({
    index: 1,
    parsed: true,
    format: 'responses',
    rawJsonString: rawBody, // 保真原文
    fields,
    charOffset: 0,
  })
  return { chunks, contentAccum, reasoningAccum, toolCalls, format: 'responses', usage, isDone: true, finishReason, parsedCount: 1 }
}

/** 从 OpenAI message.content（字符串或多模态数组）提取纯文本（与 ChatPreviewPanel.extractContent 同规则：只拼 text，忽略图片/其它） */
function extractNonStreamText(content: unknown): string {
  if (typeof content === 'string') return content
  if (Array.isArray(content)) {
    let text = ''
    for (const part of content) {
      if (!part || typeof part !== 'object') continue
      const p = part as Record<string, unknown>
      if (p.type === 'text' && typeof p.text === 'string') text += p.text
    }
    return text
  }
  return ''
}
```

**Step 3: 文件头注释补充**（parseSSE.ts:1-35 的 JSDoc 末尾追加一段「非流式」说明，指出 parseNonStreamBody 的存在与语义，保持文档与实际能力一致）。

**Step 4: 验证（临时 node 脚本，零依赖）**

将上面函数体复制为 `tmp/parse_nonstream_check.mjs` 的 JS 版本，跑三格式样例 + 异常样例：

```js
// 样例1 chat（含 tool_calls + reasoning_content）
// 样例2 claude（text + thinking + tool_use）
// 样例3 responses（message + reasoning + function_call）
// 样例4 空 body / 非 JSON / 错误响应 {"error":{...}} → 期望 emptyStream，不抛错
```

Run: `node tmp/parse_nonstream_check.mjs`
Expected: 四个样例的 contentAccum/reasoningAccum/toolCalls/usage/isDone 符合预期，异常样例返回空结构。

验证通过后删除临时脚本（不提交）。

**Step 5: 提交**

```bash
git add frontend/src/lib/parseSSE.ts
git commit -m "feat(request-log): 新增 parseNonStreamBody 支持非流式响应解析"
```

---

### Task 2: ChatPreviewPanel.vue 双模式分流

**Files:**
- Modify: `frontend/src/components/stream/ChatPreviewPanel.vue:1-12`（头注释）、`:16`（import）、`:224`（parsed）、`:277`（showPanel）、`:293-376`（模板空态文案）、`:364-365`（时间轴 props）

**Step 1: import 增加 parseNonStreamBody**

```ts
import { parseStreamBody, parseNonStreamBody, extractResponseBody } from '@/lib/parseSSE'
```

**Step 2: parsed 按 stream 分流**（替换 :224）

```ts
const parsed = computed(() => {
  const body = extractResponseBody(props.responseJson)
  return body ? (props.stream ? parseStreamBody(body) : parseNonStreamBody(body)) : emptyParsed()
})
```

其中 `emptyParsed()` 返回 `{ chunks: [], contentAccum: '', reasoningAccum: '', toolCalls: [], format: 'chat' as const, isDone: false, parsedCount: 0 }`（与 parseStreamBody 空输入行为一致；不可用 `parseStreamBody(undefined)` 直接代替，因为 undefined 时两种解析路径语义都要兜底——更简单写法：`return props.stream ? parseStreamBody(body) : parseNonStreamBody(body)`，两者对 undefined/空串都返回 emptyStream，见 Step 3 简化）。

> 简化确认：`parseStreamBody` 与 `parseNonStreamBody` 对 `undefined`/空串都返回 emptyStream（parseSSE.ts:141-143 / Task 1 Step 1 首行），所以 `parsed` 直接写：
> ```ts
> const parsed = computed(() =>
>   props.stream ? parseStreamBody(extractResponseBody(props.responseJson)) : parseNonStreamBody(extractResponseBody(props.responseJson) ?? '')
> )
> ```
> 保持最小 diff，不引入 emptyParsed 辅助。

**Step 3: showPanel 放宽**（替换 :277）

```ts
const showPanel = computed(() => {
  const p = parsed.value
  const hasResponse = !!p.contentAccum || !!p.reasoningAccum || p.toolCalls.length > 0
  // 请求区 messages 恒有可视化价值（用户拍板）：请求失败/被拦截时响应可能为空，
  // 但请求参数里的对话历史仍要展示。两者任一有内容即渲染预览，否则走空态。
  return requestMessages.value.length > 0 || hasResponse
})
```

（requestMessages 定义于 :70，showPanel 在其后引用，computed 依赖 computed，无时序问题。`props.stream === true` 的旧逻辑删除——流式/非流式/失败/拦截统一由内容判定。）

**Step 4: 时间轴非流式标题**（:364-365）

```tsx
<StreamChunkTimeline
  class="border-t border-border pt-2"
  :chunks="parsed.chunks"
  :collapsed-title="props.stream ? undefined : '完整响应 · 展开看原文'"
  :expanded-title="props.stream ? undefined : '完整响应 · 展开看原文'"
/>
```

（props.stream 为 true 时传 undefined → 走组件默认 `1 chunks · 逐 chunk 时间轴`；非流式时语义更准。）

**Step 5: 空态文案与头注释更新**

- :374 文案 `'当前日志非流式或 SSE 正文不可解析，未生成对话预览。'` → `'当前日志缺少可渲染的请求消息与响应内容，未生成对话预览。'`（覆盖三种场景：请求无 messages、响应不可解析、两者皆无；模板里 `emptyText` prop 优先的机制不变）
- :2-12 头注释补充：组件职责从「流式」扩为「流式 + 非流式双模式」，数据流第二行说明按 `props.stream` 分流。

**Step 6: 提交**

```bash
git add frontend/src/components/stream/ChatPreviewPanel.vue
git commit -m "feat(request-log): ChatPreviewPanel 支持非流式响应可视化"
```

---

### Task 3: RequestLogDetailView.vue 去掉 stream gate

**Files:**
- Modify: `frontend/src/views/RequestLogDetailView.vue:31-58`（streamView computed）、`:256`（AccordionItem v-if）、`:261`（标题）

**Step 1: streamView 简化**（去掉 :40 的 `log.stream !== true` 早退与 `visible` 字段）

折叠项改为恒渲染后，`visible` 字段不再被模板消费（:262-267 只用 truncated/body），移除避免死代码。computed 只保留面板 props 需要的字段：

```ts
const streamView = computed<{
  body: string | undefined
  truncated: boolean
  statusCode?: number
  isDone: boolean
}>(() => {
  const r = log.value?.response_json as ResponseSnapshot | undefined
  const body = extractResponseBody(r)
  const truncated = !!(r && typeof r === 'object' && (r as { truncated?: unknown }).truncated)
  const statusCode =
    r && typeof r === 'object' && typeof (r as { status_code?: unknown }).status_code === 'number'
      ? ((r as { status_code: number }).status_code)
      : undefined
  const isDone = log.value?.result === 'success' || log.value?.result === 'failed'
  return { body, truncated, statusCode, isDone }
})
```

（`:31` 的注释 `stream response_json → 可视化面板用的结构体` 同步改为 `response_json → 可视化面板用的结构体`，因为两种模式共用。）

**Step 2: 模板 gate 移除**（:256）

```tsx
<AccordionItem value="stream">
```

外层 `<template v-else>`（:166）已保证 `log` 存在，折叠项恒渲染。内容判定全部下沉到 ChatPreviewPanel（showPanel）：有请求 messages 或有响应内容 → 预览；否则空态。失败/被拦截请求的请求区对话历史照常可见（用户拍板）。

**Step 3: 标题改名**（:261）

`流式响应可视化` → `响应可视化`

「（已截断）」「（无内容）」的 conditional 提示（:262-267）保留不变。

**Step 4: 提交**

```bash
git add frontend/src/views/RequestLogDetailView.vue
git commit -m "feat(request-log): 详情页响应可视化支持非流式"
```

---

### Task 4: 真实环境验证

**Files:**
- 无代码改动；造数 + 手工验证

**Step 1: 造非流式日志数据**

后端与 request-log.db 就绪后，直接向 request-log.db 的 `request_logs` 表 INSERT 三条非流式记录（`stream=0, result='success'`，body 放三格式完整 JSON 样例，request_json 用一条含 user/assistant 消息的样例），再留一条既有流式记录做回归。造数用 sqlite3 CLI 或后端已有 API 均可（造数 SQL 见 tmp 目录临时脚本，验证后删除）。

**Step 2: 启动后端 + dev server**

- 后端：`go run ./apps/server`（默认 :3000）
- 前端：`cd frontend && pnpm dev`（注意 WorkBuddy 下 pnpm 需 `NODE_OPTIONS="--use-system-ca"` 前缀；依赖已就绪则无需 re-optimize 等待，避免 5 分钟内断 dev server）

**Step 3: 页面验证清单**

| 场景 | 预期 |
|---|---|
| 非流式 chat（含 tool_calls + reasoning_content） | 「响应可视化」面板出现；响应区显示正文气泡 + thinking 块 + 工具调用卡片；统计行 chunks 1 · parsed 1；时间轴折叠标题「完整响应 · 展开看原文」，展开可见完整 JSON |
| 非流式 claude（text + thinking + tool_use） | 同上，协议徽标显示 claude |
| 非流式 responses（message + reasoning + function_call） | 同上，协议徽标显示 responses |
| 流式请求（回归） | 行为与改动前一致：chunks 逐条、时间轴「N chunks · 逐 chunk 时间轴」、流式中断/截断标记不变 |
| 失败请求（非流式 4xx/5xx，body 为错误 JSON） | 折叠项渲染；请求区显示 messages 对话历史；响应区无消息（hasResponse=false）；不出现空态（hasRequest=true） |
| 被拦截（无 response_json，request_json 有 messages） | 折叠项渲染；请求区对话历史可见；响应区不显示 |
| 无 messages 且无响应（极端：request_json 解析不出 messages） | 折叠项渲染但显示空态「当前日志缺少可渲染的请求消息与响应内容…」 |

**Step 4: 收尾**

验证通过后，清理 tmp 造数脚本；`git status` 确认无残留文件。

---

## 风险与边界（实现时注意）

1. **chat 多 choice**：非流式只取 `choices[0]`（与流式 contentAccum 只累加 choice[0] 的语义对齐，parseSSE.ts:309-311）。多 choice 罕见，其余 choice 不进正文。
2. **claude 探测歧义（已修复）**：`parseNonStreamBody` 探测顺序 output → choices → content/stop_reason。**执行时发现**：原 `typeof obj.type === 'string'` 条件会把 `{"type":"error"}` 之类错误体误判 claude 并合成空 chunk——已收紧为只看 content 数组/stop_reason（Anthropic 非流式响应两者恒有），错误体正确落 emptyStream。
3. **content 数组里的图片**：只拼 text，图片占位符 `[image: ...]` 不会进正文（与请求区 extractContent 规则一致）；image_url 的 http/data 开头判断在 extractContent 里，但 parseNonStreamBody 不提取图片（响应区图片无展示载体，保持现状）。
4. **arguments 半 JSON**：非流式 tool_calls 的 arguments 是完整 JSON 字符串（非增量），parse 失败（罕见）→ argumentsParsed=null、argumentsRaw 保留原文，ToolCallCard 展示原文，不炸。
5. **超大响应**：非流式响应体原本就会整个落库（无 32MB 截断，那是流式 buffer 的机制），前端解析一次 JSON.parse 内存成本与 AxJsonViewer 渲染一致，无新增风险。
6. **emptyStream 复用**：parseNonStreamBody 失败路径复用现有 `emptyStream('chat')`（parseSSE.ts:155-165），不新增空结构代码。

---

## 验收标准

1. `parseNonStreamBody` 三格式（chat/claude/responses）正确提取 content/reasoning/tool_calls/usage/finish_reason，失败路径返回 emptyStream 不抛错（node 脚本验证）。
2. 详情页非流式请求显示「响应可视化」面板（标题已改名），对话气泡/思考块/工具卡片/统计行/时间轴全部正常；时间轴合成 1 条且展开可见原文（保真，非重排）。
3. 流式请求行为零回归（chunks 时间轴、截断标记、空态）。
4. 失败/被拦截请求：折叠项恒渲染，请求区 messages 对话历史可见（用户拍板）；request 无 messages 且响应不可解析时才显示空态。
5. 零新依赖；无死代码（streamView.visible 已移除）；git 提交记录按 Task 粒度拆分。
