import { ApiError, request } from '@/lib/api'
import { emitter } from '@/lib/emitter'
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
    if (chunk.type === 'response.output_text.delta' && typeof chunk.delta === 'string') return chunk.delta
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
    const response = await fetch('/api/test/chat', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...target, model, messages, stream: true }),
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

    const reader = response.body?.getReader()
    if (!reader) return { request_id: requestId, status: response.status }

    const decoder = new TextDecoder()
    let buffer = ''
    let currentEvent = ''
    // 处理 buffer 中所有完整行（以 \n 结尾）。SSE 事件行 / data 行在此解析。
    const processLines = () => {
      let index: number
      while ((index = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, index).trim()
        buffer = buffer.slice(index + 1)
        // 记录当前 SSE 事件名（event: xxx），空行重置。
        if (line.startsWith('event:')) {
          currentEvent = line.slice(6).trim()
          continue
        }
        if (!line.startsWith('data:')) {
          if (!line) currentEvent = ''
          continue
        }
        const payload = line.slice(5).trim()
        if (!payload) continue
        // route_log 事件：后端回带的访问摘要，覆盖 header 摘要（含最终 tokens）。
        if (currentEvent === 'route_log') {
          const log = decodeTestLogSummary(payload)
          if (log) {
            summary = log
            onSummary?.(log)
          }
          currentEvent = ''
          continue
        }
        if (payload === '[DONE]') continue
        try {
          const chunk = JSON.parse(payload)
          const delta = extractTestDelta(chunk, target.suffix_mode)
          if (delta) onDelta(delta)
        } catch {
          // 忽略无法解析的块（如 keep-alive 注释）
        }
      }
    }
    for (;;) {
      const { done, value } = await reader.read()
      if (done) {
        // 最后一个 chunk 可能在多字节 UTF-8 字符中间被截断：flush 解码器内部残留后补扫。
        buffer += decoder.decode()
        processLines()
        break
      }
      buffer += decoder.decode(value, { stream: true })
      processLines()
    }
    return { request_id: requestId, status: response.status }
  }

  return { listModels, chat }
}
