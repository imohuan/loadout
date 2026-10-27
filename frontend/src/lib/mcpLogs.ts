// MCP 会话日志的纯函数：解析行格式、解码 msg、JSON-RPC 配对、增量切行。
// 抽成独立模块是为了可测试：这些逻辑都在「界面看着对、其实丢数据」的边界上
// （超长行截断、跨块半行、同 id 重连重复配对），必须用测试锁住。

export interface LogLine {
  ts: string
  kind: string
  body: string
}

// parseLogLine 拆一行：`2026-08-24T18:35:25.645+08:00 [FRAME_IN] msg="..."`。
// 兼容 Windows 换行：Go 写文件虽用 \n，但复制/编辑过的日志可能带 \r。
export function parseLogLine(line: string): LogLine {
  const clean = line.replace(/\r$/, '')
  const m = clean.match(/^(\S+) \[([^\]]+)\] (.*)$/)
  if (!m) return { ts: '', kind: '', body: line }
  return { ts: m[1], kind: m[2], body: m[3] }
}

export const isFrameLine = (line: LogLine) =>
  line.kind === 'FRAME_IN' || line.kind === 'FRAME_OUT'

// decodeMsg 还原 msg="..." 里被转义的字符串；其他字段原样返回。
export function decodeMsg(body: string): string {
  const clean = body.replace(/\r$/, '')
  const m = clean.match(/^msg="([\s\S]*)"$/)
  if (!m) return clean
  try {
    return JSON.parse(`"${m[1]}"`)
  } catch {
    return m[1]
  }
}

// decodeJson 把一行日志正文解析成 JSON；不是帧行或解析失败返回 null。
export function decodeJson(line: LogLine): unknown {
  if (!isFrameLine(line)) return null
  try {
    return JSON.parse(decodeMsg(line.body))
  } catch {
    return null
  }
}

export function frameId(msg: unknown): string {
  if (!msg || typeof msg !== 'object') return ''
  const id = (msg as { id?: unknown }).id
  if (id === undefined || id === null) return ''
  return String(id)
}

export function frameKind(msg: unknown): 'request' | 'response' | 'notification' | '' {
  if (!msg || typeof msg !== 'object') return ''
  const obj = msg as Record<string, unknown>
  if (typeof obj.method === 'string') return 'request'
  if ('result' in obj || 'error' in obj) return 'response'
  return 'notification'
}

// findPair 在同一份已加载日志里找 JSON-RPC 配对（请求 ↔ 响应）。
// JSON-RPC 的 id 在重连后会重新从 1 开始，全量日志里同一 id 会出现多次，
// 因此从相邻行向两侧逐层扩展，先遇到谁就用谁（最近优先），不能只按 id 找。
export function findPair(
  lines: LogLine[],
  index: number,
): { request: LogLine; response: LogLine } | null {
  const line = lines[index]
  if (!line) return null
  const msg = decodeJson(line)
  const id = frameId(msg)
  const kind = frameKind(msg)
  if (!id || (kind !== 'request' && kind !== 'response')) return null
  const dir = kind === 'request' ? 1 : -1
  let back = index - 1
  let forward = index + 1
  while (back >= 0 || forward < lines.length) {
    const order = dir > 0 ? [forward, back] : [back, forward]
    for (const i of order) {
      if (i < 0 || i >= lines.length) continue
      const other = lines[i]
      if (!isFrameLine(other)) continue
      const otherMsg = decodeJson(other)
      if (frameId(otherMsg) !== id) continue
      const otherKind = frameKind(otherMsg)
      if (kind === 'request' && otherKind === 'response') {
        return { request: line, response: other }
      }
      if (kind === 'response' && otherKind === 'request') {
        return { request: other, response: line }
      }
    }
    back--
    forward++
  }
  return null
}

// splitCompleteLines 把增量数据切成「完整行」：lastIndexOf('\n') 之后的部分
// 是尚未写完的半行，交给调用方留到下次拼接。日志行都以 \n 结束，所以
// 半行绝不是「合法的最后一行」，直接渲染就会显示成被截断的 JSON。
export function splitCompleteLines(
  pending: string,
  chunk: string,
): { complete: string; rest: string } {
  const text = pending + chunk
  const cut = text.lastIndexOf('\n')
  if (cut < 0) return { complete: '', rest: text }
  return { complete: text.slice(0, cut + 1), rest: text.slice(cut + 1) }
}

// stripLeadingPartialLine 丢掉首屏从段中间开始读时的第一段残行（没有行首）。
export function stripLeadingPartialLine(text: string, fromStart: boolean): string {
  if (fromStart) return text
  const nl = text.indexOf('\n')
  return nl >= 0 ? text.slice(nl + 1) : ''
}

// normalizeBody 把一行日志正文还原成「可读的原始内容」：
//   msg="{\"jsonrpc\":\"2.0\"}"  →  {"jsonrpc":"2.0"}
//   line="plain text"             →  plain text
// 也就是去掉外层 key="..." 的引号与转义。日志本来就是给人看的，
// 保留 Go 的转义只是噪音；还原后 JSON 仍然合法，AxJsonViewer 可以直接渲染。
export function normalizeBody(body: string): string {
  const clean = body.replace(/\r$/, '')
  const m = clean.match(/^[A-Za-z_][\w]*="([\s\S]*)"$/)
  if (!m) return clean
  // 整行必须只有一个 k="..."：若取值里还有未转义的引号（如
  // `transport="stdio" cmd="npx"`），说明这是多字段行，不能整体拆开。
  if (/(^|[^\\])"/.test(m[1])) return clean
  try {
    return JSON.parse(`"${m[1]}"`)
  } catch {
    return m[1]
  }
}


