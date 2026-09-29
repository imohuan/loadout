import test from 'node:test'
import assert from 'node:assert/strict'
import {
  channelEnabled,
  channelModelCount,
  channelModelLabel,
  summarizeChannelPlatforms,
} from '../src/lib/channels.ts'
import type { Channel } from '../src/lib/types.ts'

function ch(id: string, baseUrl: string, opts: Partial<Channel> = {}): Channel {
  return {
    id,
    name: id,
    base_url: baseUrl,
    manual_enabled: true,
    models: [],
    ...opts,
  }
}

test('summarizeChannelPlatforms 按 base_url 分组，忽略尾斜杠差异', () => {
  const out = summarizeChannelPlatforms([
    ch('k1', 'https://a.example/v1'),
    ch('k2', 'https://a.example/v1/'),
    ch('k3', 'https://b.example/v1'),
  ])
  assert.equal(out.length, 2)
  assert.equal(out[0].keys.length, 2)
  assert.equal(out[1].keys.length, 1)
})

test('平台名优先 channel_name，其次首个 Key 名，最后回落 base_url', () => {
  const named = summarizeChannelPlatforms([ch('k1', 'https://a/v1', { channel_name: '平台甲' })])
  assert.equal(named[0].name, '平台甲')
  const fallback = summarizeChannelPlatforms([ch('acct', 'https://a/v1')])
  assert.equal(fallback[0].name, 'acct')
})

test('平台启用状态：全部启用 / 部分启用 / 全部禁用', () => {
  const all = summarizeChannelPlatforms([ch('k1', 'https://a/v1'), ch('k2', 'https://a/v1')])
  assert.equal(all[0].enabledState, 'all')
  assert.equal(all[0].enabledKeyCount, 2)

  const partial = summarizeChannelPlatforms([
    ch('k1', 'https://a/v1'),
    ch('k2', 'https://a/v1', { manual_enabled: false }),
  ])
  assert.equal(partial[0].enabledState, 'partial')
  assert.equal(partial[0].enabledKeyCount, 1)

  const none = summarizeChannelPlatforms([ch('k1', 'https://a/v1', { manual_enabled: false })])
  assert.equal(none[0].enabledState, 'none')
})

test('channelEnabled 兼容旧字段 enabled，默认启用', () => {
  assert.equal(channelEnabled(ch('k', 'https://a/v1')), true)
  assert.equal(channelEnabled(ch('k', 'https://a/v1', { manual_enabled: false })), false)
  assert.equal(
    channelEnabled(ch('k', 'https://a/v1', { manual_enabled: undefined, enabled: false })),
    false,
  )
})

test('模型数按跨 Key 并集去重', () => {
  const out = summarizeChannelPlatforms([
    ch('k1', 'https://a/v1', { models: ['m1', 'm2'] }),
    ch('k2', 'https://a/v1', { models: ['m2', 'm3'] }),
  ])
  assert.equal(out[0].modelCount, 3)
  assert.deepEqual(out[0].models, ['m1', 'm2', 'm3'])
})

test('channelModelCount：探测失败返回 -1，否则返回模型数', () => {
  assert.equal(channelModelCount(ch('k', 'https://a/v1', { models: ['m1'] })), 1)
  assert.equal(channelModelCount(ch('k', 'https://a/v1')), 0)
  assert.equal(channelModelCount(ch('k', 'https://a/v1', { models_error: '探测失败' })), -1)
})

test('任一 Key 探测失败时平台标记 probedFailed（模型清单不可信）', () => {
  const out = summarizeChannelPlatforms([
    ch('k1', 'https://a/v1', { models: ['m1'] }),
    ch('k2', 'https://a/v1', { models_error: 'timeout' }),
  ])
  assert.equal(out[0].probedFailed, true)
  const ok = summarizeChannelPlatforms([ch('k1', 'https://a/v1', { models: ['m1'] })])
  assert.equal(ok[0].probedFailed, false)
})

test('空列表返回空数组', () => {
  assert.deepEqual(summarizeChannelPlatforms([]), [])
})

test('channelModelLabel：探测失败但有缓存模型时，给出模型数并标注失败', () => {
  assert.equal(channelModelLabel(ch('k', 'https://a/v1', { models: ['m1', 'm2'] })), '2 个模型')
  assert.equal(
    channelModelLabel(ch('k', 'https://a/v1', { models: ['m1'], models_error: '404' })),
    '1 个模型（上次探测失败）',
  )
  assert.equal(channelModelLabel(ch('k', 'https://a/v1', { models_error: '404' })), '模型探测失败')
  assert.equal(channelModelLabel(ch('k', 'https://a/v1')), '0 个模型')
})
