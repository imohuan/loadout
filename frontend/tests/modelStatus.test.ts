import test from 'node:test'
import assert from 'node:assert/strict'
import {
  summarizePlatform,
  groupByBaseURL,
  textMatches,
  wildcardToRegex,
  filterPlatforms,
  matchKey,
} from '../src/lib/modelStatus.ts'
import type { ChannelStatus, ModelStatus } from '../src/lib/types.ts'

function model(name: string, opts: Partial<ModelStatus> = {}): ModelStatus {
  return {
    model: name,
    manual_enabled: true,
    health_status: 'available',
    effective_available: true,
    ...opts,
  }
}

function channel(
  id: string,
  opts: Partial<ChannelStatus> & { models?: ModelStatus[] } = {},
): ChannelStatus {
  return {
    channel: {
      id,
      name: id,
      channel_name: 'platform',
      base_url: 'https://a.example/v1',
    },
    manual_enabled: true,
    health_status: 'available',
    effective_available: true,
    models: [],
    ...opts,
  }
}

test('groupByBaseURL 把同一个 base_url 的 Key 归到一组，忽略尾斜杠差异', () => {
  const groups = groupByBaseURL([
    channel('k1', { channel: { id: 'k1', name: 'k1', base_url: 'https://a.example/v1' } }),
    channel('k2', { channel: { id: 'k2', name: 'k2', base_url: 'https://a.example/v1/' } }),
    channel('k3', { channel: { id: 'k3', name: 'k3', base_url: 'https://b.example/v1' } }),
  ])
  assert.equal(groups.length, 2)
  assert.equal(groups[0].keys.length, 2)
  assert.equal(groups[1].keys.length, 1)
})

test('summarizePlatform 统计 Key 数、可用 Key 数、模型总数与可用模型数', () => {
  const s = summarizePlatform([
    channel('k1', { models: [model('m1'), model('m2')] }),
    channel('k2', { models: [model('m3')], effective_available: false }),
  ])
  assert.equal(s.keyCount, 2)
  assert.equal(s.availableKeyCount, 1)
  assert.equal(s.modelCount, 3)
  assert.equal(s.availableModelCount, 3)
})

test('summarizePlatform 按模型名去重：同名模型只要有一条 Key 可用就算可用', () => {
  const s = summarizePlatform([
    channel('k1', {
      models: [model('shared', { effective_available: false, health_status: 'cooling' })],
    }),
    channel('k2', { models: [model('shared'), model('only-here')] }),
  ])
  assert.equal(s.modelCount, 2)
  assert.equal(s.availableModelCount, 2)
  const shared = s.models.find((m) => m.model === 'shared')
  assert.equal(shared?.available, true)
})

test('summarizePlatform 同名模型在所有 Key 上都不可用才算不可用', () => {
  const s = summarizePlatform([
    channel('k1', { models: [model('dead', { effective_available: false })] }),
    channel('k2', { models: [model('dead', { effective_available: false })] }),
  ])
  assert.equal(s.availableModelCount, 0)
  assert.equal(s.models[0].available, false)
})

test('summarizePlatform 色调：全可用=ok、部分可用=warn、一个都不可用=bad', () => {
  const all = summarizePlatform([channel('k1', { models: [model('m1')] })])
  assert.equal(all.tone, 'ok')

  const some = summarizePlatform([
    channel('k1', { models: [model('m1'), model('m2', { effective_available: false })] }),
  ])
  assert.equal(some.tone, 'warn')

  const none = summarizePlatform([
    channel('k1', { models: [model('m1', { effective_available: false })] }),
  ])
  assert.equal(none.tone, 'bad')
})

test('summarizePlatform 所有 Key 都被手动关闭时色调为 off（优先于 bad）', () => {
  const s = summarizePlatform([
    channel('k1', {
      manual_enabled: false,
      effective_available: false,
      models: [model('m1', { effective_available: false })],
    }),
  ])
  assert.equal(s.allManualOff, true)
  assert.equal(s.tone, 'off')
})

test('summarizePlatform 取渠道名作为平台名，缺失时回落到首个 Key 名', () => {
  const named = summarizePlatform([
    channel('k1', {
      channel: { id: 'k1', name: 'acct', channel_name: '平台甲', base_url: 'https://a/v1' },
    }),
  ])
  assert.equal(named.name, '平台甲')
  const fallback = summarizePlatform([
    channel('k1', { channel: { id: 'k1', name: 'acct', base_url: 'https://a/v1' } }),
  ])
  assert.equal(fallback.name, 'acct')
})

test('summarizePlatform 空列表不崩溃', () => {
  const s = summarizePlatform([])
  assert.equal(s.keyCount, 0)
  assert.equal(s.modelCount, 0)
  assert.equal(s.models.length, 0)
})

test('wildcardToRegex 把 * 当通配符，其余字符按字面量匹配', () => {
  assert.equal(wildcardToRegex('deepseek*').test('deepseek-v3'), true)
  assert.equal(wildcardToRegex('deepseek*').test('gpt-4'), false)
  // 正则元字符必须转义，不能被当成语法
  assert.equal(wildcardToRegex('a.b').test('axb'), false)
  assert.equal(wildcardToRegex('a.b').test('a.b'), true)
})

test('textMatches 无关键词时全部命中，有关键词时走包含匹配（大小写不敏感）', () => {
  assert.equal(textMatches('anything'), true)
  assert.equal(textMatches('anything', '  '), true)
  assert.equal(textMatches('DeepSeek-V4', 'deepseek'), true)
  assert.equal(textMatches('gpt-4', 'deepseek'), false)
  assert.equal(textMatches('deepseek-v4-flash', 'deepseek*flash'), true)
})
