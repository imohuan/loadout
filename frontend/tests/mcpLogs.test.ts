import test from 'node:test'
import assert from 'node:assert/strict'
import {
  parseLogLine,
  decodeMsg,
  decodeJson,
  findPair,
  splitCompleteLines,
  stripLeadingPartialLine,
  normalizeBody,
  type LogLine,
} from '../src/lib/mcpLogs.ts'

// frame 造一条帧日志行（body 用与后端一致的 msg="..." 形式）。
function frame(kind: 'FRAME_IN' | 'FRAME_OUT', msg: unknown, ts = 'T'): LogLine {
  const body = 'msg=' + JSON.stringify(JSON.stringify(msg))
  return { ts, kind, body }
}

test('parseLogLine 拆出时间戳、类型与正文', () => {
  const line = parseLogLine('2026-10-18T10:34:23.028+08:00 [CONNECT] transport="stdio"')
  assert.equal(line.ts, '2026-10-18T10:34:23.028+08:00')
  assert.equal(line.kind, 'CONNECT')
  assert.equal(line.body, 'transport="stdio"')
})

// 真实事故：chrome-devtools 的 tools/list 响应约 32KB，界面只显示到一半。
// 这里用一条超长帧锁住「完整内容不被中途丢掉」。
test('decodeJson 能完整还原超长帧（32KB 级）', () => {
  const tools = Array.from({ length: 60 }, (_, i) => ({
    name: 'tool_' + i,
    description: 'd'.repeat(400),
  }))
  const msg = { jsonrpc: '2.0', id: 138, result: { tools } }
  const line = frame('FRAME_IN', msg)
  assert.ok(line.body.length > 20000, '构造的帧应足够长')
  const parsed = decodeJson(line) as { result: { tools: unknown[] } }
  assert.equal(parsed.result.tools.length, tools.length)
  assert.deepEqual(parsed.result.tools[59], tools[59])
})

test('decodeMsg 不会把非 msg 行改坏', () => {
  assert.equal(decodeMsg('line="hello"'), 'line="hello"')
})

// Windows 换行：行尾的 \r 会让帧解析失败（界面上退化成原始文本、标题显示 []）。
test('带 \\r 的行也能正确解析与解码', () => {
  const line = parseLogLine('2026-10-18T16:23:10.321+08:00 [FRAME_IN] msg="{\\"id\\":1}"\r')
  assert.equal(line.kind, 'FRAME_IN')
  assert.deepEqual(decodeJson(line), { id: 1 })
})

test('findPair 取最近的同 id 配对（重连后 id 会重复）', () => {
  const oldReq = frame('FRAME_OUT', { id: 7, method: 'tools/call' }, 'T1')
  const oldRes = frame('FRAME_IN', { id: 7, result: { n: 1 } }, 'T2')
  const pad = frame('FRAME_IN', { id: 99, method: 'ping' }, 'T3')
  const newReq = frame('FRAME_OUT', { id: 7, method: 'tools/call' }, 'T4')
  const newRes = frame('FRAME_IN', { id: 7, result: { n: 2 } }, 'T5')
  const lines = [oldReq, oldRes, pad, newReq, newRes]

  const p1 = findPair(lines, 3)
  assert.ok(p1)
  assert.equal(p1!.response.ts, 'T5')

  const p2 = findPair(lines, 4)
  assert.ok(p2)
  assert.equal(p2!.request.ts, 'T4')
})

test('splitCompleteLines 保留未写完的半行', () => {
  const r1 = splitCompleteLines('', 'a\nb\npar')
  assert.equal(r1.complete, 'a\nb\n')
  assert.equal(r1.rest, 'par')
  const r2 = splitCompleteLines(r1.rest, 'tial\nc\n')
  assert.equal(r2.complete, 'partial\nc\n')
  assert.equal(r2.rest, '')
})

test('stripLeadingPartialLine 丢掉首屏的残行', () => {
  assert.equal(stripLeadingPartialLine('half-line\nfull\n', false), 'full\n')
  // 从段中间开始读时，首段一律视为残行丢掉（无法安全判断它是否完整）。
  assert.equal(stripLeadingPartialLine('full\n', false), '')
  assert.equal(stripLeadingPartialLine('half', false), '')
  assert.equal(stripLeadingPartialLine('full\n', true), 'full\n')
})

// 用户反馈：日志里满是 \" 这类转义，看着没意义。normalizeBody 去掉 msg="..."
// 外壳并还原转义，让列表与弹窗都直接显示可读 JSON。
test('normalizeBody 去掉 msg="..." 外壳与转义', () => {
  const raw = 'msg="{\\"jsonrpc\\":\\"2.0\\",\\"id\\":20,\\"method\\":\\"tools/call\\"}"'
  const out = normalizeBody(raw)
  assert.equal(out, '{"jsonrpc":"2.0","id":20,"method":"tools/call"}')
  assert.deepEqual(JSON.parse(out), {
    jsonrpc: '2.0',
    id: 20,
    method: 'tools/call',
  })
})

test('normalizeBody 处理 line= 与纯文本', () => {
  assert.equal(normalizeBody('line="hello world"'), 'hello world')
  assert.equal(normalizeBody('transport="stdio" cmd="npx"'), 'transport="stdio" cmd="npx"')
})

