import test from 'node:test'
import assert from 'node:assert/strict'
import { summarizeUpstreamError } from '../src/lib/errorExtract.ts'

test('summarizeUpstreamError：HTML 错误页只留标题，不把整页标签铺进界面', () => {
  const html =
    '<html>\r\n<head><title>401 Authorization Required</title></head>\r\n<body>\r\n<center><h1>401 Authorization Required</h1></center>\r\n<hr><center>openresty</center>\r\n<p><em>Powered by <a href="https://apisix.apache.org/">APISIX</a>.</em></p>\r\n</body>\r\n</html>'
  const msg = summarizeUpstreamError(html, 502)
  assert.ok(msg.includes('401 Authorization Required'), msg)
  assert.ok(!msg.includes('<html'), msg)
})

test('summarizeUpstreamError：纯文本原样保留，空响应给出状态码', () => {
  assert.equal(summarizeUpstreamError('upstream broken', 500), 'upstream broken')
  assert.equal(summarizeUpstreamError('   ', 502), '请求失败（502）')
})
