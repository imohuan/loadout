import type { RouteLog } from '@/lib/types'

// 模型测试页的日志缓存：只保存「测试请求」产生的转发日志，存浏览器 localStorage。
// 页面加载时从本地恢复（秒开、不依赖全量日志接口），发送结束后用 detail 接口
// 拉后端真实记录覆盖本地占位，保证 attempts / error_message 与后端一致。
const LOGS_KEY = 'loadout:test-route-logs'
const MAX_LOGS = 100

export function loadTestRouteLogs(): RouteLog[] | null {
  try {
    const raw = localStorage.getItem(LOGS_KEY)
    if (!raw) return null
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed : null
  } catch {
    return null
  }
}

// 插入/更新一条测试日志：按 request_id 去重，按 started_at 倒序，最多保留 MAX_LOGS 条。
export function saveTestRouteLog(entry: RouteLog) {
  try {
    const current = loadTestRouteLogs() || []
    const next = [entry, ...current.filter((log) => log.request_id !== entry.request_id)]
      .sort((a, b) => (a.started_at < b.started_at ? 1 : -1))
      .slice(0, MAX_LOGS)
    localStorage.setItem(LOGS_KEY, JSON.stringify(next))
  } catch {
    // localStorage 不可用或超限：静默，不影响主流程
  }
}

// 清空本地缓存的测试日志（含页面内的日志列表由调用方自行清空）。
export function clearTestRouteLogs() {
  try {
    localStorage.removeItem(LOGS_KEY)
  } catch {
    // 静默
  }
}
