import test from 'node:test'
import assert from 'node:assert/strict'
import {
  probeSession,
  confirmSessionLost,
  resetSessionProbe,
  reportUnauthorized,
} from '../src/lib/session.ts'
import { emitter } from '../src/lib/emitter.ts'

function fakeFetch(status: number, body: unknown = {}) {
  const impl = (async () => ({
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
  })) as unknown as typeof fetch
  return impl
}

test('probeSession：200 视为会话仍在，401 视为已登出', async () => {
  resetSessionProbe()
  assert.equal(await probeSession(fakeFetch(200)), 'alive')
  assert.equal(await probeSession(fakeFetch(401)), 'lost')
})

test('probeSession：502 与网络错误都不能判定为登出', async () => {
  const boom = (async () => {
    throw new TypeError('Failed to fetch')
  }) as unknown as typeof fetch
  assert.equal(await probeSession(fakeFetch(502)), 'unknown')
  assert.equal(await probeSession(boom), 'unknown')
})

// 用户实际遇到的场景：模型状态页批量测 Key，上游 Key 失效回 401，
// 但本机会话是好的。确认后不能广播登出。
test('reportUnauthorized：会话仍是好的时不广播 unauthorized', async () => {
  resetSessionProbe()
  let emits = 0
  const handler = () => {
    emits++
  }
  emitter.on('unauthorized', handler)
  try {
    const original = globalThis.fetch
    globalThis.fetch = fakeFetch(200)
    await reportUnauthorized()
    globalThis.fetch = original
    assert.equal(emits, 0)
  } finally {
    emitter.off('unauthorized', handler)
  }
})

test('reportUnauthorized：会话确实失效时才广播 unauthorized', async () => {
  resetSessionProbe()
  let emits = 0
  const handler = () => {
    emits++
  }
  emitter.on('unauthorized', handler)
  try {
    const original = globalThis.fetch
    globalThis.fetch = fakeFetch(401)
    await reportUnauthorized()
    globalThis.fetch = original
    assert.equal(emits, 1)
  } finally {
    emitter.off('unauthorized', handler)
  }
})

test('confirmSessionLost：只有后端明确回 401 才判定登出', async () => {
  resetSessionProbe()
  assert.equal(await confirmSessionLost(fakeFetch(401)), 'lost')
  assert.equal(await confirmSessionLost(fakeFetch(502)), 'lost') // 1.5s 内复用结论
  resetSessionProbe()
  assert.equal(await confirmSessionLost(fakeFetch(502)), 'unknown')
})

test('confirmSessionLost：并发 401 只发一次探测', async () => {
  resetSessionProbe()
  let calls = 0
  const impl = (async () => {
    calls++
    return { ok: true, status: 200, json: async () => ({}) }
  }) as unknown as typeof fetch
  const results = await Promise.all([
    confirmSessionLost(impl),
    confirmSessionLost(impl),
    confirmSessionLost(impl),
  ])
  assert.deepEqual(results, ['alive', 'alive', 'alive'])
  assert.equal(calls, 1)
})
