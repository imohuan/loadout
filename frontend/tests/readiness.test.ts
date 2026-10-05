import test from 'node:test'
import assert from 'node:assert/strict'
import { probeBackend, waitForBackend } from '../src/lib/readiness.ts'

test('waitForBackend 前几次失败后重试，成功即 resolve，并按节拍上报进度', async () => {
  let calls = 0
  const ticks: { elapsed: number; slow: boolean }[] = []
  let clock = 0

  await waitForBackend({
    probe: async () => {
      calls++
      return calls >= 3
    },
    sleep: async (ms) => {
      clock += ms
    },
    now: () => clock,
    intervalMs: 100,
    slowAfterMs: 50,
    onTick: (elapsed, slow) => ticks.push({ elapsed, slow }),
  })

  assert.equal(calls, 3)
  assert.deepEqual(ticks, [
    { elapsed: 0, slow: false },
    { elapsed: 100, slow: true },
  ])
})

test('waitForBackend 首次成功时不 sleep、不上报', async () => {
  let slept = 0
  const ticks: number[] = []

  await waitForBackend({
    probe: async () => true,
    sleep: async () => {
      slept++
    },
    onTick: (elapsed) => ticks.push(elapsed),
  })

  assert.equal(slept, 0)
  assert.deepEqual(ticks, [])
})

test('waitForBackend 不因 probe 抛错而中断', async () => {
  let calls = 0
  await waitForBackend({
    probe: async () => {
      calls++
      if (calls < 2) throw new Error('boom')
      return true
    },
    sleep: async () => {},
  })
  assert.equal(calls, 2)
})

test('probeBackend：2xx 视为就绪，非 2xx 与网络错误视为未就绪', async () => {
  assert.equal(
    await probeBackend(50, (async () => ({ ok: true })) as unknown as typeof fetch),
    true,
  )
  assert.equal(
    await probeBackend(50, (async () => ({ ok: false })) as unknown as typeof fetch),
    false,
  )
  assert.equal(
    await probeBackend(50, (async () => {
      throw new Error('connection refused')
    }) as unknown as typeof fetch),
    false,
  )
})
