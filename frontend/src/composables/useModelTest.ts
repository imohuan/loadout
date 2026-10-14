import { ApiError, request } from '@/lib/api'
import { emitter } from '@/lib/emitter'
import { extractErrorSummary } from '@/lib/errorExtract'
import type { RouteLog } from '@/lib/types'

// 模型测试目标：既可用 channel_id 复用已存渠道（后端解密密钥，不回传明文），
// 也可直接传临时 base_url + api_key（不落盘、不走前端直连）。
// 「Loadout 自带」模式：sk_key_hash 传自建 SK key 的哈希，后端解析明文后调用自家网关。
// 选中渠道时自定义 api_key 优先于渠道存储的 key；suffix_mode 决定上游路径后缀。
export interface TestTarget {
  channel_id?: string
  base_url?: string
  api_key?: string
  sk_key_hash?: string
  suffix_mode?: string
  // 是否流式请求上游（决定 /api/test/chat 走 SSE 还是整包返回）。
  stream?: boolean
  /** 走本机 /v1 网关而不是直连上游：只有这条路会写转发日志并刷新渠道健康状态。 */
  via_gateway?: boolean
}

export interface TestMessage {
  role: string
  content: unknown
}

export interface TestModelsResult {
  models: string[]
  error?: string
}

export interface TestChatResult {
  request_id: string
  status: number
}

/** 单次「能不能用」探测的结果：失败不抛异常，把上游原文带回来给人看。 */
export interface TestProbeResult {
  ok: boolean
  /** HTTP 状态码；0 = 请求根本没发出去（网络层失败）。 */
  status: number
  error?: string
}

export interface TestChatOptions {
  onDelta: (text: string) => void
  onSummary?: (summary: RouteLog) => void
  signal?: AbortSignal
}

// extractTestDelta 按后缀模式从单个 SSE 块提取增量文本：
// chat → choices[0].delta.content / reasoning_content；gpt（/responses）→
// response.output_text.delta 事件的 delta；claude（/messages）→
// content_block_delta + text_delta 的 delta.text。
export function extractTestDelta(chunk: Record<string, any>, mode?: string): string {
  if (mode === 'gpt') {
    if (chunk.type === 'response.output_text.delta' && typeof chunk.delta === 'string')
      return chunk.delta
    return ''
  }
  if (mode === 'claude') {
    if (chunk.type === 'content_block_delta' && chunk.delta?.type === 'text_delta') {
      return typeof chunk.delta.text === 'string' ? chunk.delta.text : ''
    }
    return ''
  }
  return (
    chunk.choices?.[0]?.delta?.content ??
    chunk.choices?.[0]?.delta?.reasoning_content ??
    chunk.choices?.[0]?.message?.content ??
    ''
  )
}

/**
 * SSE 逐行扫描器：把字节流切成「完整行」再交给回调，负责拼接被截断的半行。
 *
 * chat 与 probe 两处都要读上游 SSE，行缓冲与 UTF-8 多字节截断的处理完全一样，
 * 抽出来共用 —— 各写一遍，迟早出现「一处修了 bug、另一处忘了改」。
 * 回调返回 false 表示不再需要后续内容（调用方自行 cancel reader）。
 */
function createSSELineScanner(onLine: (line: string) => void) {
  const decoder = new TextDecoder()
  let buffer = ''
  let raw = ''
  return {
    /** 喂入一块刚读到的字节，扫出其中所有完整行。 */
    push(value: Uint8Array, stream = true) {
      const text = decoder.decode(value, { stream })
      raw += text
      buffer += text
      const lines = buffer.split('\n')
      // 最后一行可能是被截断的半行，留到下一轮再拼。
      buffer = lines.pop() ?? ''
      for (const line of lines) onLine(line)
    },
    /** 流结束：flush 解码器残留（多字节字符可能刚好跨在最后一个 chunk 上）。 */
    flush() {
      const text = decoder.decode()
      raw += text
      buffer += text
      const lines = buffer.split('\n')
      buffer = ''
      for (const line of lines) onLine(line)
    },
    /**
     * 到目前为止收到的原文。
     *
     * 有的站即使要流式，报错时也回一整包 JSON（没有 SSE 的 data: 前缀），
     * 那种响应扫不出一行 data，只能靠原文兜底挖错误。
     */
    rawText() {
      return raw
    },
  }
}

// base64ToUtf8 兼容含中文的 base64 载荷：atob 得到二进制字符串后按 UTF-8 解码。
function base64ToUtf8(encoded: string): string {
  const binary = atob(encoded)
  const bytes = Uint8Array.from(binary, (char) => char.charCodeAt(0))
  return new TextDecoder('utf-8').decode(bytes)
}

// decodeTestLogSummary 解码后端回带的访问摘要（header X-Test-Log 或 SSE route_log 事件）。
// 返回 null 表示载荷非法（不是本协议的摘要），调用方应静默忽略。
export function decodeTestLogSummary(encoded: string): RouteLog | null {
  try {
    const parsed = JSON.parse(base64ToUtf8(encoded))
    return parsed?.request_id ? (parsed as RouteLog) : null
  } catch {
    return null
  }
}

export function useModelTest() {
  // 走后台代理获取上游模型列表，规避上游 CORS 限制。
  const listModels = (target: TestTarget) =>
    request<TestModelsResult>('/api/test/models', 'POST', target)

  // 走后台代理发起流式 chat 请求，逐块回调增量文本。
  // 访问摘要（RouteLog 同形）通过两条通道回带：响应头 X-Test-Log（非流式/错误路径）
  // 与 SSE 末尾 route_log 事件（流式成功路径）。两者都解析后回调 onSummary，
  // 后者覆盖前者（流式结束时携带最终 tokens）。测试请求不写后端日志，前端
  // 「请求记录」面板依赖该摘要直显。
  const chat = async (
    target: TestTarget,
    model: string,
    messages: TestMessage[],
    options: TestChatOptions,
  ): Promise<TestChatResult> => {
    const { onDelta, onSummary, signal } = options
    // stream 由调用方（buildTarget）决定，默认流式；非流式时后端整包返回 JSON。
    const stream = target.stream !== false
    const response = await fetch('/api/test/chat', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...target, stream, model, messages }),
      signal,
    })
    const requestId = response.headers.get('X-Request-Id') || ''
    // 响应头摘要（非流式/错误路径）：在状态判断前解析，throw 前也能拿到。
    const headerLog = response.headers.get('X-Test-Log')
    let summary: RouteLog | null = headerLog ? decodeTestLogSummary(headerLog) : null
    if (summary) onSummary?.(summary)
    if (response.status === 401) emitter.emit('unauthorized')
    if (!response.ok) {
      const body = await response.json().catch(() => ({}))
      throw new ApiError(
        body.error?.message || body.message || `请求失败（${response.status}）`,
        response.status,
      )
    }

    // 非流式：整段 JSON 一次读完，取完整文本回调一次（extractTestDelta 兼容 message.content）。
    if (!stream) {
      const data = await response.json().catch(() => ({}))
      const delta = extractTestDelta(data, target.suffix_mode)
      if (delta) onDelta(delta)
      return { request_id: requestId, status: response.status }
    }

    const reader = response.body?.getReader()
    if (!reader) return { request_id: requestId, status: response.status }

    let currentEvent = ''
    // SSE 行解析：event 行记录事件名，data 行取载荷（route_log 是摘要、[DONE] 是结束）。
    const scanner = createSSELineScanner((raw) => {
      const line = raw.trim()
      if (line.startsWith('event:')) {
        currentEvent = line.slice(6).trim()
        return
      }
      if (!line.startsWith('data:')) {
        if (!line) currentEvent = ''
        return
      }
      const payload = line.slice(5).trim()
      if (!payload) return
      // route_log 事件：后端回带的访问摘要，覆盖 header 摘要（含最终 tokens）。
      if (currentEvent === 'route_log') {
        const log = decodeTestLogSummary(payload)
        if (log) {
          summary = log
          onSummary?.(log)
        }
        currentEvent = ''
        return
      }
      if (payload === '[DONE]') return
      try {
        const chunk = JSON.parse(payload)
        const delta = extractTestDelta(chunk, target.suffix_mode)
        if (delta) onDelta(delta)
      } catch {
        // 忽略无法解析的块（如 keep-alive 注释）
      }
    })
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        // 最后一个 chunk 可能在多字节 UTF-8 字符中间被截断：flush 解码器内部残留后补扫。
        scanner.flush()
        break
      }
      scanner.push(value)
    }
    return { request_id: requestId, status: response.status }
  }

  /**
   * 流读完却一个字都没拿到时的说明。
   *
   * 复用 lib/errorExtract 的提取器而不是自己再写一套：它按优先级递归遍历整个
   * 错误对象树（msg → message → …）、并深度遍历任意嵌套 key，比只认固定几个
   * key 的临时版本可靠得多 —— 上游错误结构五花八门，写死 key 迟早漏。
   *
   * 三种情况分开说，否则用户只能看到一句没用的「失败」：
   *   - 上游在流里回了错误（只是没走 SSE 格式）→ 把那句错误拿出来；
   *   - 回了一堆不是 JSON 的东西 → 截一段原文（可能是网关的 HTML 错误页）；
   *   - 干干净净地什么都没有 → 这才真的是「没返回内容」。
   */
  const describeEmptyStream = (raw: string, status: number): string => {
    const message = extractErrorSummary(raw, null, 300)
    if (message) return message
    const text = raw.trim()
    if (text) return text.length > 300 ? `${text.slice(0, 300)}…` : text
    return `上游没有返回内容（HTTP ${status}）`
  }

  /**
   * probe 是「这个 Key 现在能不能用」的最小判断：发一条流式 ping，
   * 收到内容即判定可用，随后把流读完再返回。
   *
   * 用流式而不是非流式：不少中转站只实现了流式转发，发非流式会被上游直接
   * 回「Non-stream chat request is currently not supported」，那测出来的是
   * 「这个站不支持非流式」，而不是「这个 Key 能不能用」。
   *
   * 与 chat 的区别还有两点，都是为「批量测试」服务的：
   *   1. 不抛异常 —— 一批里有失败是常态，抛出会把整批带崩；
   *   2. 把上游错误原文带回来 —— 测试失败要在左栏列表里显示给人看，只留个
   *      「请求失败（429）」等于没说，用户看不出是限流、欠费还是 Key 失效。
   */
  const probe = async (
    target: TestTarget,
    model: string,
    messages: TestMessage[],
    timeoutMs = 40000,
    /** 拿到第一个内容块之后，最多再等这么久收尾。 */
    graceAfterFirstChunkMs = 5000,
  ): Promise<TestProbeResult> => {
    const controller = new AbortController()
    // 两道时限，都为了「别让慢上游拖死整批」：
    //   1. 首字节/首个内容块：等 timeoutMs（上游在思考，可能真的慢）；
    //   2. 拿到内容之后：只再等 graceAfterFirstChunkMs。
    // 第 2 条是必须的：只留一道总时限的话，一个说个不停的上游会把这道时限耗光，
    // 明明已经确认可用，却被自己掐断记成「等待响应超过 40 秒」。
    let timer = setTimeout(() => controller.abort(), timeoutMs)
    const armGrace = () => {
      clearTimeout(timer)
      timer = setTimeout(() => controller.abort(), graceAfterFirstChunkMs)
    }
    // 是否已经确认这次能出结果（收到过内容块）。
    // 放在 try 之外：catch 里要靠它区分「上游太慢」和「上游话太多被宽限期掐断」。
    let sawContent = false
    try {
      const response = await fetch('/api/test/chat', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...target, stream: true, model, messages }),
        signal: controller.signal,
      })
      if (response.status === 401) emitter.emit('unauthorized')
      if (!response.ok) {
        const body = await response.json().catch(() => ({}))
        return {
          ok: false,
          status: response.status,
          error:
            extractErrorSummary(JSON.stringify(body), null, 300) ||
            `请求失败（${response.status}）`,
        }
      }
      const reader = response.body?.getReader()
      if (!reader) return { ok: false, status: response.status, error: '上游没有返回响应体' }

      let currentEvent = ''
      // 流里夹带的错误原文：上游中途报错时记下来，等流结束一并汇报。
      let streamError = ''
      const scanner = createSSELineScanner((raw) => {
        const line = raw.trim()
        if (line.startsWith('event:')) {
          currentEvent = line.slice(6).trim()
          return
        }
        if (!line.startsWith('data:')) {
          if (!line) currentEvent = ''
          return
        }
        const payload = line.slice(5).trim()
        if (!payload || payload === '[DONE]') return
        // route_log 是后端在流末尾追加的摘要，不是上游内容，跳过。
        if (currentEvent === 'route_log') {
          currentEvent = ''
          return
        }
        try {
          const chunk = JSON.parse(payload)
          const message = chunk?.error?.message
          if (typeof message === 'string' && message.trim()) {
            streamError = message.trim()
            return
          }
        } catch {
          // 不是 JSON 的 data 行（少见的心跳/纯文本），同样说明上游活着。
        }
        // 记下「已经出结果」，但继续把流读完 —— 提前断开会让网关把这条
        // 转发日志标成 stream_interrupted，看起来像失败，而 Key 其实是好的。
        // 首个内容块到达即改挂宽限期计时：已经能出结果了，不必陪上游把话说完。
        if (!sawContent) armGrace()
        sawContent = true
      })
      try {
        for (;;) {
          const { done, value } = await reader.read()
          if (done) break
          scanner.push(value)
        }
        scanner.flush()
      } finally {
        await reader.cancel().catch(() => {})
      }
      if (streamError) return { ok: false, status: response.status, error: streamError }
      if (sawContent) return { ok: true, status: response.status }
      return {
        ok: false,
        status: response.status,
        error: describeEmptyStream(scanner.rawText(), response.status),
      }
    } catch (error) {
      // 超时与网络层失败（离线、被反代截断）：同样算这次测试没通过。
      const aborted = error instanceof Error && error.name === 'AbortError'
      if (aborted) {
        // 已经拿到过内容：说明这个 Key 能出结果，只是上游话太多，算通过。
        if (sawContent) return { ok: true, status: 0 }
        return {
          ok: false,
          status: 0,
          error: `等待响应超过 ${Math.round(timeoutMs / 1000)} 秒`,
        }
      }
      return {
        ok: false,
        status: 0,
        error: error instanceof Error ? error.message : '网络请求失败',
      }
    } finally {
      clearTimeout(timer)
    }
  }

  return { listModels, chat, probe }
}
