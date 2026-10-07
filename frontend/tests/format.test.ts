import { formatShortCN, formatTokens } from '../src/lib/format.ts'
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

test('formatTokens 按 K/M/B/T 进位，避免长数字串', () => {
  assert.equal(formatTokens(0), '0')
  assert.equal(formatTokens(999), '999')
  assert.equal(formatTokens(1000), '1K')
  assert.equal(formatTokens(1500), '1.5K')
  assert.equal(formatTokens(12_400), '12.4K')
  assert.equal(formatTokens(999_999), '1M')
  assert.equal(formatTokens(1_000_000), '1M')
  assert.equal(formatTokens(1_500_000), '1.5M')
  assert.equal(formatTokens(118_742_500), '118.74M')
  assert.equal(formatTokens(1_032_650_700), '1.03B')
  assert.equal(formatTokens(17_548_489_800), '17.55B')
  assert.equal(formatTokens(1_000_000_000_000), '1T')
  assert.equal(formatTokens(1_500_000_000_000), '1.5T')
})

test('formatTokens 对负数、空值与非法值稳定输出', () => {
  assert.equal(formatTokens(-1500), '-1.5K')
  assert.equal(formatTokens(), '0')
  assert.equal(formatTokens(Number.NaN), '0')
  assert.equal(formatTokens(Number.POSITIVE_INFINITY), '0')
})
