import test from 'node:test'
import assert from 'node:assert/strict'
import { keyEnabledState, keySwitchState, keyStatusBrief } from '../src/lib/modelStatus.ts'
import type { ChannelStatus } from '../src/lib/types.ts'

function channel(
  opts: {
    manual?: boolean
    status?: string
    available?: boolean
    cls?: string
    reason?: string
  } = {},
): ChannelStatus {
  return {
    channel: { id: 'k', name: 'acct', channel_name: '平台甲', base_url: 'https://a/v1' },
    manual_enabled: opts.manual !== false,
    health_status: opts.status || 'available',
    effective_available: opts.available !== false,
    failure_class: opts.cls,
    reason: opts.reason,
    models: [],
  }
}

test('keySwitchState：手动关闭 → off，用户可以直接拨开关', () => {
  // 手动关闭的 Key effective_available 一定是 false；这里显式写出来，
  // 让「关 = off 且可拨」这个用例不依赖 helper 的默认值。
  const key: ChannelStatus = {
    ...channel({ manual: false }),
    effective_available: false,
  }
  const s = keySwitchState(key)
  assert.equal(s.value, false)
  assert.equal(s.disabled, false)
})

test('keyEnabledState：只给「开启 / 关闭」二元状态，与故障成因分离', () => {
  assert.deepEqual(keyEnabledState(channel()), { label: '开启', tone: 'ok' })
  assert.deepEqual(keyEnabledState({ ...channel({ manual: false }), effective_available: false }), {
    label: '关闭',
    tone: 'off',
  })
  assert.deepEqual(
    keyEnabledState({ ...channel({ available: false }), effective_available: false }),
    { label: '关闭', tone: 'off' },
  )
})

test('keySwitchState：可用 → on，可关闭', () => {
  const s = keySwitchState(channel())
  assert.equal(s.value, true)
  assert.equal(s.disabled, false)
})

test('keySwitchState：自动熔断（额度用尽/冷却/禁用）→ off 且禁用开关，必须走恢复', () => {
  const cases = [
    channel({ available: false, status: 'cooling', cls: 'rule_disable_key_daily' }),
    channel({ available: false, status: 'cooling', cls: 'free_quota_exhausted' }),
    channel({ available: false, status: 'cooling', cls: 'rate_limit' }),
    channel({ available: false, status: 'disabled', cls: 'auth' }),
  ]
  for (const c of cases) {
    const s = keySwitchState(c)
    assert.equal(s.value, false, JSON.stringify(c))
    assert.equal(s.disabled, true, JSON.stringify(c))
  }
})

test('keyStatusBrief：手动关闭的 Key 显示「手动关闭」，不是错误原因', () => {
  assert.deepEqual(keyStatusBrief(channel({ manual: false })), { label: '手动关闭', tone: 'off' })
})
