import { api, request } from '@/lib/api'
import type {
  VolcQuotaConfig,
  VolcQuotaRefreshResult,
  VolcQuotaStatusResponse,
} from '@/lib/types'

// useVolcQuota 火山引擎免费额度插件的管理接口封装。
export function useVolcQuota() {
  /** 一次拉取全部配置 + 每条配置下的免费模型 + 使用记录 */
  const status = () => api<VolcQuotaStatusResponse>('/api/volc-quota/status')
  /** 整体覆盖配置（PUT 原子事务）；secret_key 留空 = 保留既有密文 */
  const save = (configs: VolcQuotaConfig[]) =>
    request<void>('/api/volc-quota/config', 'PUT', { configs })
  /** 手动刷新额度：channelId 缺省 = 全量刷新 */
  const refresh = (channelId?: string) =>
    request<VolcQuotaRefreshResult>(
      '/api/volc-quota/refresh',
      'POST',
      channelId ? { channel_id: channelId } : {},
    )
  return { status, save, refresh }
}
