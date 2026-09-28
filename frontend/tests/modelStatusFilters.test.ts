import test from 'node:test'
import assert from 'node:assert/strict'
import { filterPlatforms, matchKey, keyStatusBrief } from '../src/lib/modelStatus.ts'
import type { ChannelStatus, ModelStatus } from '../src/lib/types.ts'

function model(name: string, available = true): ModelStatus {
  return {
    model: name,
    manual_enabled: true,
    health_status: available ? 'available' : 'cooling',
    effective_available: available,
  }
}

function channel(
  id: string,
  baseUrl: string,
  opts: { name?: string; platform?: string; models?: ModelStatus[]; available?: boolean } = {},
): ChannelStatus {
  return {
    channel: {
      id,
      name: opts.name || id,
      channel_name: opts.platform || '平台甲',
      base_url: baseUrl,
    },
    manual_enabled: true,
    health_status: 'available',
    effective_available: opts.available !== false,
    models: opts.models || [],
  }
}

test('filterPlatforms 按关键词匹配平台名、Key 名与模型名', () => {
  const items = [
    channel('k1', 'https://a/v1', { platform: 'workbuddy', models: [model('deepseek-v4')] }),
    channel('k2', 'https://b/v1', { platform: '像素星空', models: [model('gpt-image-1')] }),
  ]
  assert.equal(filterPlatforms(items, { keyword: 'workbuddy' }).length, 1)
  assert.equal(filterPlatforms(items, { keyword: 'k2' }).length, 1)
  assert.equal(filterPlatforms(items, { keyword: 'deepseek*' }).length, 1)
  assert.equal(filterPlatforms(items, { keyword: '不存在' }).length, 0)
})

test('filterPlatforms health=issue 只留部分异常或不可用的平台', () => {
  const items = [
    channel('k1', 'https://a/v1', { models: [model('m1'), model('m2')] }),
    channel('k2', 'https://b/v1', { models: [model('m3'), model('m4', false)] }),
    channel('k3', 'https://c/v1', { models: [model('m5', false)] }),
  ]
  const issue = filterPlatforms(items, { health: 'issue' })
  assert.deepEqual(issue.map((p) => p.baseUrl).sort(), ['https://b/v1', 'https://c/v1'])
  const ok = filterPlatforms(items, { health: 'ok' })
  assert.deepEqual(
    ok.map((p) => p.baseUrl),
    ['https://a/v1'],
  )
})

test('filterPlatforms 不传条件时返回全部平台', () => {
  const items = [channel('k1', 'https://a/v1'), channel('k2', 'https://b/v1')]
  assert.equal(filterPlatforms(items, {}).length, 2)
})

test('matchKey 可按 Key 名或其下模型名筛选', () => {
  const key = channel('acct-1', 'https://a/v1', { name: 'acct-1', models: [model('glm-5.2')] })
  assert.equal(matchKey(key), true)
  assert.equal(matchKey(key, 'acct'), true)
  assert.equal(matchKey(key, 'glm'), true)
  assert.equal(matchKey(key, 'nope'), false)
})

test('filterPlatforms：关键词命中平台名时保留整组模型；只命中模型名时标出命中的模型', () => {
  const items = [
    channel('k1', 'https://a/v1', {
      platform: 'workbuddy',
      models: [model('deepseek-v4'), model('glm-5.2')],
    }),
    channel('k2', 'https://b/v1', { platform: '像素星空', models: [model('gpt-image-1')] }),
  ]
  // 平台名命中 → 整组都是相关的，不裁剪 chip
  const byName = filterPlatforms(items, { keyword: 'workbuddy' })
  assert.equal(byName[0].matchedModels, undefined)
  // 只命中模型名 → chip 只列命中的模型，方便一眼看出是哪些
  const byModel = filterPlatforms(items, { keyword: 'glm*' })
  assert.deepEqual(byModel[0].matchedModels, ['glm-5.2'])
  assert.equal(byModel.length, 1)
})

test('keyStatusBrief：手动关闭优先，可用/冷却/禁用各有短标签与色调', () => {
  const off = channel('k', 'https://a/v1')
  off.manual_enabled = false
  assert.deepEqual(keyStatusBrief(off), { label: '手动关闭', tone: 'off' })

  const ok = channel('k', 'https://a/v1')
  assert.deepEqual(keyStatusBrief(ok), { label: '可用', tone: 'ok' })

  const cooling = channel('k', 'https://a/v1', { available: false })
  cooling.health_status = 'cooling'
  assert.equal(keyStatusBrief(cooling).tone, 'warn')

  const banned = channel('k', 'https://a/v1', { available: false })
  banned.health_status = 'disabled'
  banned.failure_class = 'rule_disable_key_never'
  assert.deepEqual(keyStatusBrief(banned), { label: '已禁用', tone: 'bad' })
})

test('keyStatusBrief：额度用尽类冷却给出「额度用尽」而不是泛化的「冷却中」', () => {
  const quota = channel('k', 'https://a/v1', { available: false })
  quota.health_status = 'cooling'
  quota.failure_class = 'rule_disable_key_daily'
  assert.deepEqual(keyStatusBrief(quota), { label: '额度用尽', tone: 'warn' })
})
