import type { ProcessInfo } from './types'

/**
 * 进程列表去重：同一个 id 只保留最新的那一条。
 *
 * 为什么需要：安装任务使用固定 id（如 dep-install:skills）。上一轮的记录
 * 已经进入历史，新任务又以同一 id 启动；快照会同时带出「旧历史 + 新运行中」。
 * 前端 store 若按 id 只改第一条，左侧就继续显示旧状态，直到手动刷新。
 *
 * 选择规则：
 *   1. 运行中的优先（它才是当前任务）；
 *   2. 都是历史时取结束时间更晚的；结束时间相同取开始时间更晚的。
 */
export function dedupeProcesses(list: ProcessInfo[]): ProcessInfo[] {
  const chosen = new Map<string, ProcessInfo>()
  for (const p of list) {
    const prev = chosen.get(p.id)
    if (!prev || isNewer(p, prev)) chosen.set(p.id, p)
  }
  // 保持输入顺序（后端已按结束/开始时间排好），只删除被覆盖的旧项。
  return list.filter((p) => chosen.get(p.id) === p)
}

function isNewer(a: ProcessInfo, b: ProcessInfo): boolean {
  if (a.status === 'running' && b.status !== 'running') return true
  if (a.status !== 'running' && b.status === 'running') return false
  const ae = a.endedAt ? Date.parse(a.endedAt) : 0
  const be = b.endedAt ? Date.parse(b.endedAt) : 0
  if (ae !== be) return ae > be
  return Date.parse(a.startedAt) >= Date.parse(b.startedAt)
}

/**
 * 按 id 更新或插入一条进程。
 *
 * 必须替换全部同 id 项（不能只改 findIndex 的第一条）：否则残留的旧记录
 * 会让「已经在跑」与「已结束」同时出现在列表里，状态翻来覆去。
 */
export function upsertProcess(list: ProcessInfo[], p: ProcessInfo): ProcessInfo[] {
  const index = list.findIndex((x) => x.id === p.id)
  const rest = list.filter((x) => x.id !== p.id)
  if (index < 0) return [p, ...rest]
  // 保留原来那一行的位置，避免每次内存刷新都把进程移到最上面。
  const before = rest.slice(0, index)
  const after = rest.slice(index)
  return [...before, p, ...after]
}
