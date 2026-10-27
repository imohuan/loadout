import test from 'node:test'
import assert from 'node:assert/strict'
import { dedupeProcesses, upsertProcess } from '../src/lib/processes.ts'
import type { ProcessInfo } from '../src/lib/types.ts'

function proc(id: string, name: string, status: ProcessInfo['status'], endedAt?: string): ProcessInfo {
  return {
    id,
    name,
    kind: 'dep',
    cmd: 'npm install -g skills@latest',
    pid: 100,
    status,
    startedAt: '2026-10-27T12:39:46+08:00',
    endedAt,
    memBytes: 0,
    log: [],
  }
}

test('dedupeProcesses：旧历史与新运行进程同 ID 时保留运行中的新进程', () => {
  const list = [
    proc('dep-install:skills', '检查已装 skills', 'done', '2026-10-27T12:39:49+08:00'),
    proc('dep-install:skills', '安装 skills', 'running'),
  ]
  const got = dedupeProcesses(list)
  assert.equal(got.length, 1)
  assert.equal(got[0].status, 'running')
  assert.equal(got[0].name, '安装 skills')
})

test('dedupeProcesses：同一 ID 多条历史时保留最新一条', () => {
  const got = dedupeProcesses([
    proc('task', 'old', 'done', '2026-10-27T12:39:49+08:00'),
    proc('task', 'new', 'done', '2026-10-27T12:40:49+08:00'),
  ])
  assert.equal(got.length, 1)
  assert.equal(got[0].name, 'new')
})

test('upsertProcess：更新时移除所有同 ID 项，不再只改第一条', () => {
  const list = [
    proc('dep-install:skills', '旧历史', 'done', '2026-10-27T12:39:49+08:00'),
    proc('other', '其他任务', 'done', '2026-10-27T12:40:00+08:00'),
  ]
  const got = upsertProcess(list, proc('dep-install:skills', '安装 skills', 'running'))
  assert.equal(got.filter((p) => p.id === 'dep-install:skills').length, 1)
  assert.equal(got.find((p) => p.id === 'dep-install:skills')?.status, 'running')
  assert.equal(got.find((p) => p.id === 'other')?.name, '其他任务')
})

test('upsertProcess：更新已有项时保持原来的列表位置', () => {
  const old = proc('dep-install:skills', '安装 skills', 'running')
  const got = upsertProcess(
    [proc('first', '第一个', 'done', '2026-10-27T12:40:00+08:00'), old],
    { ...old, memBytes: 1234 },
  )
  assert.deepEqual(
    got.map((p) => p.id),
    ['first', 'dep-install:skills'],
  )
  assert.equal(got[1].memBytes, 1234)
})
