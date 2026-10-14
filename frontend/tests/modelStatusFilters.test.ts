import test from 'node:test'
import assert from 'node:assert/strict'
import {
  filterPlatforms,
  matchKey,
  keyStatusBrief,
  keyGroup,
  sortKeysByGroup,
  filterKeysByGroup,
} from '../src/lib/modelStatus.ts'
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

test('keyGroup：可用 / 手动关闭 / 异常 / 不可用 各自归位', () => {
  const ok = channel('k', 'https://a/v1')
  assert.equal(keyGroup(ok), 'ok')

  const manual = channel('k', 'https://a/v1', { available: false })
  manual.manual_enabled = false
  assert.equal(keyGroup(manual), 'manual')

  // 冷却 / 额度用尽：会随时间自愈，算「其他异常」，排在中间
  const cooling = channel('k', 'https://a/v1', { available: false })
  cooling.health_status = 'cooling'
  assert.equal(keyGroup(cooling), 'issue')

  // 鉴权失效 / 账号禁用：要人工介入，算「不可用」，排最下面
  const banned = channel('k', 'https://a/v1', { available: false })
  banned.health_status = 'disabled'
  banned.failure_class = 'rule_disable_key_never'
  assert.equal(keyGroup(banned), 'bad')
})

test('sortKeysByGroup：可用的排最前、不可用的排最后、手动关闭与异常居中', () => {
  const banned = channel('bad', 'https://a/v1', { available: false })
  banned.health_status = 'disabled'
  banned.failure_class = 'rule_disable_key_never'

  const cooling = channel('issue', 'https://a/v1', { available: false })
  cooling.health_status = 'cooling'

  const manual = channel('manual', 'https://a/v1', { available: false })
  manual.manual_enabled = false

  const ok = channel('ok', 'https://a/v1')

  const sorted = sortKeysByGroup([banned, cooling, manual, ok])
  assert.deepEqual(
    sorted.map((k) => k.channel.id),
    ['ok', 'manual', 'issue', 'bad'],
  )
})

test('sortKeysByGroup：同组内保持原顺序（后端优先级），且不改动入参数组', () => {
  const items = [
    channel('bad-1', 'https://a/v1', { available: false }),
    channel('ok-1', 'https://a/v1'),
    channel('ok-2', 'https://a/v1'),
  ]
  items[0].health_status = 'disabled'
  items[0].failure_class = 'rule_disable_key_never'

  const sorted = sortKeysByGroup(items)
  assert.deepEqual(
    sorted.map((k) => k.channel.id),
    ['ok-1', 'ok-2', 'bad-1'],
  )
  // 入参数组顺序不变：调用方可能在别处还按原顺序渲染
  assert.deepEqual(
    items.map((k) => k.channel.id),
    ['bad-1', 'ok-1', 'ok-2'],
  )
})

test('filterKeysByGroup：列表显示与批量测试用同一份结果', () => {
  const banned = channel('bad-1', 'https://a/v1', { available: false })
  banned.health_status = 'disabled'
  banned.failure_class = 'rule_disable_key_never'

  const cooling = channel('issue-1', 'https://a/v1', { available: false })
  cooling.health_status = 'cooling'

  const manual = channel('manual-1', 'https://a/v1', { available: false })
  manual.manual_enabled = false

  const items = [banned, cooling, manual, channel('ok-1', 'https://a/v1')]

  // 全部：排序后原样返回
  assert.deepEqual(
    filterKeysByGroup(items, 'all').map((k) => k.channel.id),
    ['ok-1', 'manual-1', 'issue-1', 'bad-1'],
  )
  // 单组：只剩该组的 Key，顺序仍按分组排
  assert.deepEqual(
    filterKeysByGroup(items, 'bad').map((k) => k.channel.id),
    ['bad-1'],
  )
  assert.deepEqual(
    filterKeysByGroup(items, 'ok').map((k) => k.channel.id),
    ['ok-1'],
  )
  // 没有该组时返回空数组（筛选后「N 项」归零，按钮要据此提示）
  assert.deepEqual(filterKeysByGroup([items[3]], 'bad'), [])
})
