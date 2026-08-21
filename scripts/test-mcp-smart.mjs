// test-mcp-smart.mjs — 直连后端，模拟 MCP streamable HTTP 客户端测试 /mcp/$smart
// 用法：node scripts/test-mcp-smart.mjs [groupName]
const BASE = process.env.BASE || 'http://127.0.0.1:3000'
const KEY = process.env.KEY || '5715ae45f98604459fa4b5783910d2da18f14209611f6f52f48cb8776e620cd0'
const GROUP = process.argv[2] || ''

const headers = {
  accept: 'application/json, text/event-stream',
  'content-type': 'application/json',
  'mcp-protocol-version': '2025-11-25',
  'x-loadout-key': KEY
}
if (GROUP) headers['x-loadout-group'] = GROUP

async function post(body, sessionId) {
  const h = { ...headers }
  if (sessionId) h['mcp-session-id'] = sessionId
  const res = await fetch(BASE + '/mcp/$smart', { method: 'POST', headers: h, body: JSON.stringify(body) })
  const sid = res.headers.get('mcp-session-id')
  const text = await res.text()
  return { status: res.status, sid, text }
}

function parseData(text) {
  for (const line of text.split('\n')) {
    if (line.startsWith('data:')) return JSON.parse(line.slice(5).trim())
  }
  return null
}

function show(title, r) {
  console.log(`\n===== ${title} (status=${r.status}) =====`)
  console.log(r.text)
}

async function main() {
  let r = await post({ jsonrpc: '2.0', id: 1, method: 'initialize', params: { protocolVersion: '2025-11-25', capabilities: {}, clientInfo: { name: 'test', version: '1.0' } } })
  show('initialize', r)
  const sid = r.sid
  if (!sid) { console.log('!! 未拿到 session-id，认证可能失败'); return }

  r = await post({ jsonrpc: '2.0', id: 2, method: 'tools/list', params: {} }, sid)
  show('tools/list（端点暴露的工具）', r)

  r = await post({ jsonrpc: '2.0', id: 3, method: 'tools/call', params: { name: 'status', arguments: {} } }, sid)
  show(`status 无参${GROUP ? '（group=' + GROUP + '）' : '（默认全部）'}`, r)

  r = await post({ jsonrpc: '2.0', id: 4, method: 'tools/call', params: { name: 'get', arguments: { tools: ['ask_question'] } } }, sid)
  show('get tools=["ask_question"]', r)

  r = await post({ jsonrpc: '2.0', id: 5, method: 'tools/call', params: { name: 'get', arguments: { category: 'ddd' } } }, sid)
  show('get category="ddd"', r)

  // 关闭 session
  await fetch(BASE + '/mcp/$smart', { method: 'DELETE', headers: { ...headers, 'mcp-session-id': sid } }).catch(() => {})
}

main().catch(e => { console.error(e); process.exit(1) })
