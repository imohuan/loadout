/**
 * 转发日志页「自动刷新」开关的本地持久化。
 *
 * 存在 localStorage 而不是后端：这是纯前端的显示偏好（是否让列表自己跳数字），
 * 与后端配置无关；也顺手避开了「前端选项要落库」的额外接口。
 *
 * 默认开启：老用户升级前后行为一致（第一页仍然 3 秒一跳），不会觉得功能被砍了。
 */
const KEY = 'loadout:route-logs-auto-refresh'

/** 读开关；localStorage 不可用或值非法时返回默认值 true（开启）。 */
export function readAutoRefreshEnabled(): boolean {
  try {
    const raw = localStorage.getItem(KEY)
    if (raw === null) return true
    return raw !== 'false'
  } catch {
    return true
  }
}

/** 写开关；localStorage 不可用（隐私模式/超额）时静默，不影响页面内已生效的状态。 */
export function writeAutoRefreshEnabled(enabled: boolean) {
  try {
    localStorage.setItem(KEY, enabled ? 'true' : 'false')
  } catch {
    // 静默：写不进去只是下次打开恢复默认，不影响本次会话
  }
}
