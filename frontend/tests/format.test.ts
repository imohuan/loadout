import { formatShortCN } from '../src/lib/format.ts'
import test from 'node:test'
import assert from 'node:assert/strict'

test('formatShortCN 输出 MM-DD HH:mm，并按北京时间换算', () => {
  // 后端存 UTC：2026-09-27T02:04:22Z = 北京时间 10:04
  assert.equal(formatShortCN('2026-09-27T02:04:22.5000341Z'), '09-27 10:04')
  // 跨日：UTC 21:30 = 北京时间次日 05:30
  assert.equal(formatShortCN('2026-09-27T21:30:00Z'), '09-28 05:30')
})

test('formatShortCN 对空值与非法值返回占位符', () => {
  assert.equal(formatShortCN(), '-')
  assert.equal(formatShortCN(''), '-')
  assert.equal(formatShortCN('不是时间'), '-')
})
