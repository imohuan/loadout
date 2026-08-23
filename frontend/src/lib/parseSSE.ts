/**
 * 解析完整 SSE 流式响应正文，逐 chunk 拆出结构化数据。
 *
 * 支持三种协议格式（与 model-gateway transparent proxy 对齐，参考
 * plugins/vision_v2/stream.go 的 feedChat / feedClaude / feedResponses）：
 *
 * 1. OpenAI chat.completion 流（DeepSeek / 火山等同构）：
 *      data: {"choices":[{"delta":{"content":"我"},"index":0}]}
 *      data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_x","type":"function",
 *            "function":{"name":"mcp__x","arguments":"{\"a\": "}}]},"index":0}]}
 *      data: [DONE]
 *
 * 2. Anthropic claude/messages 流（event: 行 + data 行）：
 *      event: content_block_start
 *      data: {"type":"content_block_start","index":0,"content_block":{"type":"text","id":"..."}}
 *      event: content_block_delta
 *      data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"你"}}
 *      event: content_block_delta
 *      data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"..."}}
 *      event: message_stop
 *      data: {"type":"message_stop"}
 *
 * 3. OpenAI Responses API 流：
 *      data: {"type":"response.output_text.delta","item_id":"...","delta":"你"}
 *      data: {"type":"response.function_call_arguments.delta","item_id":"...","delta":"..."}
 *      data: {"type":"response.output_item.done","item":{"type":"function_call","arguments":"..."}}
 *      data: {"type":"response.completed","response":{"usage":{...}}}
 *
 * 对未知/畸形行：原样落到 chunks[].rawJson（parsed=false），前端按需展示，绝不抛错。
 *
 * 参考：plugins/request-log/service.go responseSnapshot.Body 字段，
 * 每条 SSE chunk 由后端按 `data: <json>\n\n` 逐块拼接到字符串缓冲中（maxStreamBuffer 32MB）。
 * 因此常见分隔符：`\n\n`、`\n`、`\r\n\r\n`、`\r\n`（HTTP chunked edge），
 * 都需能容忍；解析失败的不抛错，列为 parsed=false 但保留 rawLine 供调试。
 *
 * 非流式响应：response_json.body 是完整上游 JSON 响应体字符串（request-log service.go
 * 非流式分支 string(ap.Response.Body)），无 data: 行。解析走 parseNonStreamBody，
 * 按结构探测三格式（choices → chat；content[]/stop_reason/type → claude；output[] → responses），
 * 输出与 parseStreamBody 完全同构的 ParsedStream（合成 1 条 parsed chunk），渲染链共用。
 */

export type StreamFormat = 'chat' | 'claude' | 'responses'

// 单个 chunk 抽取结果（已结构化）
export interface ParsedChunk {
  /** chunk 在正文里的序号（1..N），用户视角 */
  index: number
  /** 整体 JSON.parse 是否成功；false 时 rawJson 仍为字符串原样，前端可降级展示 */
  parsed: boolean
  /** 协议格式标识（时间轴徽标用） */
  format?: StreamFormat
  /** JSON 解析失败的行原文（仅 parsed=false 时使用），便于在时间轴里"原味"展示 */
  rawJson?: string
  /** 解析成功时的字段，OpenAI 系块；其它协议可能部分缺失 */
  fields?: {
    id?: string
    model?: string
    object?: string
    created?: number
    /** 结束原因（chat: finish_reason；claude: stop_reason；responses: status） */
    finishReason?: string | null
    /** 这一 chunk delta.content 的纯文本片段 */
    contentDelta?: string
    /** 这一 chunk delta.reasoning_content 的纯文本片段 */
    reasoningDelta?: string
    /** 这一 chunk delta.refusal 的纯文本片段（OpenAI 拒绝场景） */
    refusalDelta?: string
    /** 这一 chunk delta.role */
    role?: string
    /** choices[0].index 或 claude content_block index / responses output_index */
    choiceIndex?: number
    /** 这一 chunk delta.tool_calls 的增量片段（跨 chunk 拼接） */
    toolCallsDelta?: ToolCallDeltaFragment[]
    /** usage 字段 */
    usage?: StreamUsage
  }
  /** 原始 JSON 字符串（解析成功也保留，方便展开看） */
  rawJsonString?: string
  /** 首字节位置（在拼接正文里的字符偏移）；用于异常排查 */
  charOffset: number
}

/** 单个 chunk 里 tool call 的增量片段（OpenAI tool_calls / claude input_json_delta / responses function_call 共用） */
export interface ToolCallDeltaFragment {
  /** tool call 序号（同一次调用的所有片段 index 一致；claude 用 content_block index；responses 用 item_id 映射） */
  index: number
  /** 首个片段带 id（call_xxx）；后续片段没有 */
  id?: string
  /** type（通常 "function"） */
  type?: string
  /** function.name（仅首个片段带） */
  name?: string
  /** arguments 增量片段（跨 chunk 拼接成完整 JSON 字符串） */
  arguments?: string
}

/** 聚合完成的工具调用（arguments 已跨 chunk 拼接，并尝试 parse） */
export interface ParsedToolCall {
  index: number
  id: string
  type: string
  name: string
  /** 拼接后的 arguments 原文（JSON 字符串，可能 parse 失败） */
  argumentsRaw: string
  /** JSON.parse(argumentsRaw) 的结果；失败为 null */
  argumentsParsed: unknown
}

export interface StreamUsage {
  prompt_tokens?: number
  completion_tokens?: number
  total_tokens?: number
  /** DeepSeek / 火山扩展：cached / reasoning 相关字段保留为原样 */
  prompt_tokens_details?: { cached_tokens?: number; [k: string]: unknown }
  completion_tokens_details?: { reasoning_tokens?: number; [k: string]: unknown }
  [k: string]: unknown
}

/** parseStreamBody 的返回结构 */
export interface ParsedStream {
  /** 所有 data 行（含 [DONE]），含解析失败的保留行 */
  chunks: ParsedChunk[]
  /** 累加的正文文本（来自 delta.content / text_delta / output_text.delta） */
  contentAccum: string
  /** 累加的思考过程文本（来自 delta.reasoning_content / thinking_delta / reasoning*.delta） */
  reasoningAccum: string
  /** 聚合完成的工具调用列表 */
  toolCalls: ParsedToolCall[]
  /** 协议格式 */
  format: StreamFormat
  /** usage（如有，仅取最后一次出现的） */
  usage?: StreamUsage
  /** 是否正常结束（chat: [DONE]；claude: message_stop；responses: response.completed） */
  isDone: boolean
  /** 结束原因 */
  finishReason?: string | null
  /** chunks 中解析成功的数量 */
  parsedCount: number
}

/**
 * 解析 SSE 正文。失败永不抛错；整体返回 ParsedStream。
 * 自动探测协议格式：data 行里 choices[] → chat；content_block_* → claude；response.* → responses。
 */
export function parseStreamBody(body: unknown): ParsedStream {
  if (typeof body !== 'string' || body.length === 0) {
    return emptyStream('chat')
  }
  const format = detectStreamFormat(body)
  switch (format) {
    case 'claude':
      return parseClaudeStream(body)
    case 'responses':
      return parseResponsesStream(body)
    default:
      return parseChatStream(body)
  }
}

function emptyStream(format: StreamFormat): ParsedStream {
  return {
    chunks: [],
    contentAccum: '',
    reasoningAccum: '',
    toolCalls: [],
    format,
    isDone: false,
    parsedCount: 0,
  }
}

/** 探测协议格式：看首个非空 data 行的 JSON 结构。 */
function detectStreamFormat(body: string): StreamFormat {
  for (const rawLine of body.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (!payload || payload === '[DONE]') continue
    let json: unknown
    try {
      json = JSON.parse(payload)
    } catch {
      continue
    }
    if (!json || typeof json !== 'object') continue
    const type = (json as Record<string, unknown>).type
    if (typeof type === 'string') {
      if (type.startsWith('response.')) return 'responses'
      if (type.includes('content_block') || type === 'message_start' || type === 'message_stop' || type === 'message_delta') return 'claude'
    }
    if ((json as Record<string, unknown>).choices) return 'chat'
  }
  // 兜底：默认 chat（既有行为）
  return 'chat'
}

// ---- 通用工具调用聚合器（三种格式共用） ----

class ToolCallAggregator {
  private byIndex = new Map<number, ParsedToolCall>()
  private order: number[] = []

  addFragment(frag: ToolCallDeltaFragment) {
    let agg = this.byIndex.get(frag.index)
    if (!agg) {
      agg = { index: frag.index, id: '', type: 'function', name: '', argumentsRaw: '', argumentsParsed: null }
      this.byIndex.set(frag.index, agg)
      this.order.push(frag.index)
    }
    if (frag.id) agg.id = frag.id
    if (frag.type) agg.type = frag.type
    if (frag.name) agg.name = frag.name
    if (frag.arguments) {
      agg.argumentsRaw += frag.arguments
      // 每次增量后重新 parse：拼到一半恰好合法、之后又追加导致最终非法的中间态，
      // 必须把 argumentsParsed 归 null，避免 UI 展示陈旧对象与 argumentsRaw 不一致。
      try {
        agg.argumentsParsed = JSON.parse(agg.argumentsRaw)
      } catch {
        agg.argumentsParsed = null
      }
    }
  }

  /** 覆盖某个调用（responses output_item.done 会给完整 arguments；claude content_block_stop 收尾） */
  setFull(index: number, full: Partial<Pick<ParsedToolCall, 'id' | 'name' | 'argumentsRaw'>>) {
    let agg = this.byIndex.get(index)
    if (!agg) {
      agg = { index, id: '', type: 'function', name: '', argumentsRaw: '', argumentsParsed: null }
      this.byIndex.set(index, agg)
      this.order.push(index)
    }
    if (full.id) agg.id = full.id
    if (full.name) agg.name = full.name
    if (full.argumentsRaw != null) {
      agg.argumentsRaw = full.argumentsRaw
      try {
        agg.argumentsParsed = JSON.parse(full.argumentsRaw)
      } catch {
        /* 保持原文 */
      }
    }
  }

  flush(): ParsedToolCall[] {
    const list = [...this.byIndex.values()].sort((a, b) => a.index - b.index)
    this.byIndex.clear()
    this.order = []
    return list
  }
}

// ---- OpenAI chat.completion 流 ----

function parseChatStream(body: string): ParsedStream {
  const lines = body.split(/\r?\n/)
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let isDone = false
  let finishReason: string | null | undefined
  let parsedCount = 0
  let cursor = 0
  const agg = new ToolCallAggregator()

  for (const rawLine of lines) {
    const line = rawLine.trim()
    if (!line) {
      cursor += rawLine.length + 1
      continue
    }
    cursor += rawLine.length + 1

    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (payload === '[DONE]') {
      isDone = true
      chunks.push({ index: chunks.length + 1, parsed: false, format: 'chat', rawJson: '[DONE]', charOffset: cursor - line.length - 1 })
      continue
    }

    let parsedJson: unknown
    try {
      parsedJson = JSON.parse(payload)
    } catch {
      chunks.push({ index: chunks.length + 1, parsed: false, format: 'chat', rawJson: payload, charOffset: cursor - line.length - 1 })
      continue
    }

    const fields = extractChatChunkFields(parsedJson)
    if (!fields) {
      chunks.push({ index: chunks.length + 1, parsed: true, format: 'chat', rawJsonString: payload, charOffset: cursor - line.length - 1 })
      continue
    }

    parsedCount++
    if (fields.contentDelta) contentAccum += fields.contentDelta
    if (fields.reasoningDelta) reasoningAccum += fields.reasoningDelta
    if (fields.usage) usage = fields.usage
    if (fields.finishReason != null) finishReason = fields.finishReason
    if (fields.toolCallsDelta?.length) {
      for (const frag of fields.toolCallsDelta) agg.addFragment(frag)
    }

    chunks.push({ index: chunks.length + 1, parsed: true, format: 'chat', rawJsonString: payload, fields, charOffset: cursor - line.length - 1 })
  }

  return { chunks, contentAccum, reasoningAccum, toolCalls: agg.flush(), format: 'chat', usage, isDone, finishReason, parsedCount }
}

/**
 * 从 OpenAI chat chunk JSON 抽取关键字段。返回 null 表示不像标准 chunk。
 * 注意：choices 可能为多 choice（罕见）；正文只累加 choice[0]，其余 choice 增量
 * 落入 fields.contentDelta 供时间轴查看（contentAccum 不混拼）。
 */
function extractChatChunkFields(json: unknown): ParsedChunk['fields'] | null {
  if (!json || typeof json !== 'object') return null
  const obj = json as Record<string, unknown>
  const choices = obj.choices
  if (!Array.isArray(choices) && obj.usage == null) return null

  let contentDelta: string | undefined
  let reasoningDelta: string | undefined
  let refusalDelta: string | undefined
  let role: string | undefined
  let finishReason: string | null | undefined
  let choiceIndex: number | undefined
  let toolCallsDelta: ToolCallDeltaFragment[] | undefined

  if (Array.isArray(choices) && choices.length > 0) {
    // 取首个 choice 的增量做正文累积（多 choice 罕见，其余片段不混拼）
    const first = choices[0] as Record<string, unknown>
    if (typeof first.index === 'number') choiceIndex = first.index
    const delta = first.delta as Record<string, unknown> | undefined
    if (delta && typeof delta === 'object') {
      if (typeof delta.content === 'string') contentDelta = delta.content
      if (typeof delta.role === 'string') role = delta.role
      if (typeof delta.refusal === 'string') refusalDelta = delta.refusal
      // reasoning_content 兼容多种命名：reasoning_content / reasoning / reasoning_text
      const reasoningRaw =
        (delta.reasoning_content as unknown) ??
        (delta.reasoning as unknown) ??
        (delta.reasoning_text as unknown)
      if (typeof reasoningRaw === 'string') reasoningDelta = reasoningRaw
      // 新版 tool_calls（增量流）
      if (Array.isArray(delta.tool_calls)) {
        toolCallsDelta = delta.tool_calls
          .filter((tc): tc is Record<string, unknown> => !!tc && typeof tc === 'object')
          .map((tc) => {
            const fn = tc.function as Record<string, unknown> | undefined
            const frag: ToolCallDeltaFragment = { index: 0 }
            if (typeof tc.index === 'number') frag.index = tc.index
            if (typeof tc.id === 'string') frag.id = tc.id
            if (typeof tc.type === 'string') frag.type = tc.type
            if (fn && typeof fn.name === 'string') frag.name = fn.name
            if (fn && typeof fn.arguments === 'string') frag.arguments = fn.arguments
            return frag
          })
      }
      // 旧版 function_call（非 tool_calls）：arguments 也是跨 chunk 拼接的
      const fnCall = delta.function_call as Record<string, unknown> | undefined
      if (fnCall && typeof fnCall === 'object') {
        if (toolCallsDelta === undefined) toolCallsDelta = []
        toolCallsDelta.push({
          index: 0,
          name: typeof fnCall.name === 'string' ? fnCall.name : undefined,
          arguments: typeof fnCall.arguments === 'string' ? fnCall.arguments : undefined,
        })
      }
    }
    if (typeof first.finish_reason === 'string') finishReason = first.finish_reason
  }

  const usage = obj.usage as StreamUsage | undefined
  const fields: ParsedChunk['fields'] = {}
  if (typeof obj.id === 'string') fields.id = obj.id
  if (typeof obj.model === 'string') fields.model = obj.model
  if (typeof obj.object === 'string') fields.object = obj.object
  if (typeof obj.created === 'number') fields.created = obj.created
  if (contentDelta !== undefined) fields.contentDelta = contentDelta
  if (reasoningDelta !== undefined) fields.reasoningDelta = reasoningDelta
  if (refusalDelta !== undefined) fields.refusalDelta = refusalDelta
  if (role !== undefined) fields.role = role
  if (finishReason !== undefined) fields.finishReason = finishReason
  if (choiceIndex !== undefined) fields.choiceIndex = choiceIndex
  if (toolCallsDelta?.length) fields.toolCallsDelta = toolCallsDelta
  if (usage) fields.usage = usage
  return fields
}

// ---- Anthropic claude/messages 流（event: 行 + data 行） ----

function parseClaudeStream(body: string): ParsedStream {
  const lines = body.split(/\r?\n/)
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let isDone = false
  let finishReason: string | null | undefined
  let parsedCount = 0
  let cursor = 0
  const agg = new ToolCallAggregator()
  // claude content_block index → tool call 的 index 映射（content_block_start 建，stop 收）
  const blockTool = new Map<number, { id: string; name: string }>()

  for (const rawLine of lines) {
    const line = rawLine.trim()
    if (!line) {
      cursor += rawLine.length + 1
      continue
    }
    cursor += rawLine.length + 1

    // claude 有 event: 前缀行（语义由 data 行里的 type 决定，event: 仅用于阅读）
    if (line.startsWith('event:')) continue
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (!payload) continue

    let json: unknown
    try {
      json = JSON.parse(payload)
    } catch {
      chunks.push({ index: chunks.length + 1, parsed: false, format: 'claude', rawJson: payload, charOffset: cursor - line.length - 1 })
      continue
    }
    if (!json || typeof json !== 'object') continue
    const ev = json as Record<string, unknown>
    const type = typeof ev.type === 'string' ? ev.type : ''

    let fields: ParsedChunk['fields'] = {}
    const idx = typeof ev.index === 'number' ? ev.index : undefined
    if (idx !== undefined) fields.choiceIndex = idx

    switch (type) {
      case 'content_block_start': {
        const cb = ev.content_block as Record<string, unknown> | undefined
        const cbType = cb?.type
        if (cbType === 'tool_use') {
          const bid = idx ?? 0
          const id = typeof cb?.id === 'string' ? cb.id : ''
          const name = typeof cb?.name === 'string' ? cb.name : ''
          blockTool.set(bid, { id, name })
          agg.addFragment({ index: bid, id, name, type: 'tool_use', arguments: '' })
        }
        break
      }
      case 'content_block_delta': {
        const d = ev.delta as Record<string, unknown> | undefined
        const dType = d?.type
        if (dType === 'text_delta' && typeof d?.text === 'string') {
          fields.contentDelta = d.text
          contentAccum += d.text
        } else if (dType === 'thinking_delta' && typeof d?.thinking === 'string') {
          fields.reasoningDelta = d.thinking
          reasoningAccum += d.thinking
        } else if (dType === 'input_json_delta' && typeof d?.partial_json === 'string') {
          // tool_use 的 arguments 增量
          const bid = idx ?? 0
          agg.addFragment({ index: bid, arguments: d.partial_json })
          fields.toolCallsDelta = [{ index: bid, arguments: d.partial_json }]
        }
        break
      }
      case 'content_block_stop': {
        // 工具块结束：收尾该 index 的调用（保留累积的 arguments）
        break
      }
      case 'message_delta': {
        const d = ev.delta as Record<string, unknown> | undefined
        if (d && typeof d.stop_reason === 'string') {
          finishReason = d.stop_reason
          fields.finishReason = d.stop_reason
        }
        const u = ev.usage as StreamUsage | undefined
        if (u) {
          usage = u
          fields.usage = u
        }
        break
      }
      case 'message_start': {
        const m = ev.message as Record<string, unknown> | undefined
        if (typeof m?.id === 'string') fields.id = m.id
        if (typeof m?.model === 'string') fields.model = m.model
        const u = m?.usage as StreamUsage | undefined
        if (u) usage = u
        break
      }
      case 'message_stop': {
        isDone = true
        fields.finishReason = finishReason ?? 'end_turn'
        break
      }
      default:
        // ping 等忽略，但保留 rawJson 供时间轴
        chunks.push({ index: chunks.length + 1, parsed: true, format: 'claude', rawJsonString: payload, fields, charOffset: cursor - line.length - 1 })
        continue
    }

    parsedCount++
    chunks.push({ index: chunks.length + 1, parsed: true, format: 'claude', rawJsonString: payload, fields, charOffset: cursor - line.length - 1 })
  }

  return { chunks, contentAccum, reasoningAccum, toolCalls: agg.flush(), format: 'claude', usage, isDone, finishReason, parsedCount }
}

// ---- OpenAI Responses API 流 ----

function parseResponsesStream(body: string): ParsedStream {
  const lines = body.split(/\r?\n/)
  const chunks: ParsedChunk[] = []
  let contentAccum = ''
  let reasoningAccum = ''
  let usage: StreamUsage | undefined
  let isDone = false
  let finishReason: string | null | undefined
  let parsedCount = 0
  let cursor = 0
  const agg = new ToolCallAggregator()
  // item_id → 聚合 index 的映射（function_call 用 item_id 关联增量）
  const itemToIndex = new Map<string, number>()
  let nextItemIdx = 1000 // 避免与 claude 语义混用；responses 无 content_block index，自增

  for (const rawLine of lines) {
    const line = rawLine.trim()
    if (!line) {
      cursor += rawLine.length + 1
      continue
    }
    cursor += rawLine.length + 1
    // responses 也可能带 event: 行（response.output_text.delta 等），data 行才有效载荷
    if (line.startsWith('event:')) continue
    if (!line.startsWith('data:')) continue
    const payload = line.slice(5).trim()
    if (!payload) continue

    let json: unknown
    try {
      json = JSON.parse(payload)
    } catch {
      chunks.push({ index: chunks.length + 1, parsed: false, format: 'responses', rawJson: payload, charOffset: cursor - line.length - 1 })
      continue
    }
    if (!json || typeof json !== 'object') continue
    const ev = json as Record<string, unknown>
    const type = typeof ev.type === 'string' ? ev.type : ''

    let fields: ParsedChunk['fields'] = {}

    if (type === 'response.output_text.delta') {
      if (typeof ev.delta === 'string') {
        fields.contentDelta = ev.delta
        contentAccum += ev.delta
      }
    } else if (type === 'response.output_text.done') {
      if (typeof ev.text === 'string') {
        fields.contentDelta = ev.text
      }
    } else if (type === 'response.reasoning_summary_text.delta' || type === 'response.reasoning_text.delta') {
      if (typeof ev.delta === 'string') {
        fields.reasoningDelta = ev.delta
        reasoningAccum += ev.delta
      }
    } else if (type === 'response.reasoning_summary_text.done' || type === 'response.reasoning_text.done') {
      // done 事件带全文（summary_text/text），delta 已逐片累加过；这里只落到
      // fields 供时间轴展示，不重复累加（与 output_text.done 语义一致）。
      const seg = ev.summary_text ?? ev.text
      if (typeof seg === 'string') {
        fields.reasoningDelta = seg
      }
    } else if (type === 'response.output_item.added') {
      const item = ev.item as Record<string, unknown> | undefined
      if (item?.type === 'function_call') {
        const idx = nextItemIdx++
        // 注意：added/done 事件的 id 在 item.id 里（顶层无 item_id）；
        // 仅 function_call_arguments.delta 顶层带 item_id（两者等值）。
        const key = typeof item.id === 'string' ? item.id : ''
        itemToIndex.set(key, idx)
        agg.addFragment({
          index: idx,
          id: typeof item.id === 'string' ? item.id : undefined,
          name: typeof item.name === 'string' ? item.name : undefined,
          type: 'function_call',
          arguments: typeof item.arguments === 'string' ? item.arguments : undefined,
        })
      }
    } else if (type === 'response.function_call_arguments.delta') {
      const idx = itemToIndex.get(String(ev.item_id ?? ''))
      if (idx != null && typeof ev.delta === 'string') {
        agg.addFragment({ index: idx, arguments: ev.delta })
      }
    } else if (type === 'response.output_item.done') {
      const item = ev.item as Record<string, unknown> | undefined
      if (item?.type === 'function_call') {
        const key = typeof item.id === 'string' ? item.id : ''
        const idx = itemToIndex.get(key)
        if (idx != null) {
          agg.setFull(idx, {
            id: typeof item.id === 'string' ? item.id : undefined,
            name: typeof item.name === 'string' ? item.name : undefined,
            argumentsRaw: typeof item.arguments === 'string' ? item.arguments : undefined,
          })
        }
      }
    } else if (type === 'response.completed') {
      isDone = true
      const resp = ev.response as Record<string, unknown> | undefined
      const u = resp?.usage as StreamUsage | undefined
      if (u) {
        usage = u
        fields.usage = u
      }
      finishReason = typeof resp?.status === 'string' ? resp.status : 'completed'
      fields.finishReason = finishReason
    } else if (type === 'response.created' || type === 'response.in_progress') {
      const resp = ev.response as Record<string, unknown> | undefined
      if (typeof resp?.id === 'string') fields.id = resp.id
      if (typeof resp?.model === 'string') fields.model = resp.model
    } else if (type === 'response.failed') {
      isDone = true
      finishReason = 'failed'
      fields.finishReason = 'failed'
    } else {
      // 其它事件类型（response.output_item.added 非 function_call 等）保留原文
      chunks.push({ index: chunks.length + 1, parsed: true, format: 'responses', rawJsonString: payload, fields, charOffset: cursor - line.length - 1 })
      continue
    }

    parsedCount++
    chunks.push({ index: chunks.length + 1, parsed: true, format: 'responses', rawJsonString: payload, fields, charOffset: cursor - line.length - 1 })
  }

  return { chunks, contentAccum, reasoningAccum, toolCalls: agg.flush(), format: 'responses', usage, isDone, finishReason, parsedCount }
}

/**
 * 工具函数：从整个 response_json（不一定是 stream）里抽 body 字符串。
 * 适配 plugins/request-log 的 responseSnapshot 结构：
 *   { status_code, headers, body?, truncated? }
 * 对非 SSE 响应（直接 JSON 对象），body 可能不是字符串而是对象——返回 undefined。
 */
export function extractResponseBody(responseJson: unknown): string | undefined {
  if (!responseJson || typeof responseJson !== 'object') return undefined
  const body = (responseJson as Record<string, unknown>).body
  if (typeof body === 'string') return body
  return undefined
}

/**
 * 是否看起来是 SSE 正文（首条非空行以 data: 开头）。
 * 仅用于折叠面板启用判断——更严格的解析走 parseStreamBody。
 */
export function looksLikeSSE(body: string): boolean {
  const first = body.split(/\r?\n/).find((l) => l.trim().length > 0)
  return !!first && first.trim().startsWith('data:')
}

/**
 * 解析非流式（完整 JSON）响应正文，提取消息内容。
 * 与 parseStreamBody 输出结构完全同构，渲染链（ChatPreviewPanel）直接复用。
 * 支持三种协议格式的完整响应（与流式格式探测对齐）：
 *   1. chat:      { choices:[{ message:{ content, reasoning_content, tool_calls }, finish_reason }], usage }
 *   2. claude:    { content:[{ type:'text'|'thinking'|'tool_use' }], stop_reason, usage }
 *   3. responses: { output:[{ type:'message'|'reasoning'|'function_call' }], status, usage }
 * 非流式合成 1 条 parsed chunk（rawJsonString=全文原文），展开时间轴可见响应原文。
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
  // claude 判定只看 content 数组 / stop_reason（Anthropic 非流式响应两者恒有），
  // 不认顶层 type 字段——否则 {"type":"error"} 之类错误体也会被误判 claude。
  if (Array.isArray(obj.output)) return parseNonStreamResponses(obj, body)
  if (Array.isArray(obj.choices)) return parseNonStreamChat(obj, body)
  if (Array.isArray(obj.content) || typeof obj.stop_reason === 'string') {
    return parseNonStreamClaude(obj, body)
  }
  return emptyStream('chat')
}

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
      // 与流式 extractChatChunkFields 的三别名对齐：reasoning_content / reasoning / reasoning_text
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
        const input = block.input
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
