import { ApiError, request } from '@/lib/api'
import { emitter } from '@/lib/emitter'

// 模型测试目标：既可用 channel_id 复用已存渠道（后端解密密钥，不回传明文），
// 也可直接传临时 base_url + api_key（不落盘、不走前端直连）。
// 选中渠道时自定义 api_key 优先于渠道存储的 key；suffix_mode 决定上游路径后缀。
export interface TestTarget {
  channel_id?: string
  base_url?: string
  api_key?: string
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

export function useModelTest() {
  // 走后台代理获取上游模型列表，规避上游 CORS 限制。
  const listModels = (target: TestTarget) =>
    request<TestModelsResult>('/api/test/models', 'POST', target)

  // 走后台代理发起流式 chat 请求，逐块回调增量文本。signal 可选，用于中止。
  const chat = async (
    target: TestTarget,
    model: string,
    messages: TestMessage[],
    onDelta: (text: string) => void,
    signal?: AbortSignal,
  ): Promise<TestChatResult> => {
    const response = await fetch('/api/test/chat', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...target, model, messages, stream: true }),
      signal,
    })
    const requestId = response.headers.get('X-Request-Id') || ''
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
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      let index: number
      while ((index = buffer.indexOf('\n')) >= 0) {
        const line = buffer.slice(0, index).trim()
        buffer = buffer.slice(index + 1)
        if (!line.startsWith('data:')) continue
        const payload = line.slice(5).trim()
        if (!payload || payload === '[DONE]') continue
        try {
          const chunk = JSON.parse(payload)
          const delta = extractTestDelta(chunk, target.suffix_mode)
          if (delta) onDelta(delta)
        } catch {
          // 忽略无法解析的块（如 keep-alive 注释）
        }
      }
    }
    return { request_id: requestId, status: response.status }
  }

  return { listModels, chat }
}
