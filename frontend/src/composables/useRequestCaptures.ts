import { api, request } from '@/lib/api'
import type { CaptureDetail, CapturePage } from '@/lib/types'

export interface CaptureFilters {
  request_id?: string
  model?: string
  /** 路径子串，如 chat/completions */
  path?: string
  /** 时间范围：datetime-local 原值（后端按本地时区解释） */
  from?: string
  to?: string
  /** 是否流式：undefined = 全部 */
  stream?: boolean
}

/**
 * 请求抓包捕获（request-capture 插件）。
 *
 * 与 useRequestLogs 的区别：这里的报文是**未脱敏**的原始数据（Authorization 原文、
 * 图片 base64 全留），所以所有接口都要求登录态，详情页也只能在后台看。
 */
export function useRequestCaptures() {
  function list(filters: CaptureFilters, pagination?: { page?: number; pageSize?: number }) {
    const search = new URLSearchParams()
    if (filters.request_id) search.set('request_id', filters.request_id)
    if (filters.model) search.set('model', filters.model)
    if (filters.path) search.set('path', filters.path)
    if (filters.from) search.set('from', filters.from)
    if (filters.to) search.set('to', filters.to)
    if (filters.stream !== undefined) search.set('stream', filters.stream ? '1' : '0')
    if (pagination?.page) search.set('page', String(pagination.page))
    if (pagination?.pageSize) search.set('pageSize', String(pagination.pageSize))
    return api<CapturePage>(`/api/request-captures${search.size ? `?${search}` : ''}`)
  }
  const detail = (id: string) =>
    api<CaptureDetail>(`/api/request-captures/${encodeURIComponent(id)}`)
  const remove = (id: string) =>
    request<{ ok: boolean }>(`/api/request-captures/${encodeURIComponent(id)}`, 'DELETE')
  const clear = () => request<{ ok: boolean; affected: number }>('/api/request-captures', 'DELETE')
  /**
   * replayScript：把抓到的原始请求体生成一个可直接运行的 Node.js 复现脚本，
   * 返回脚本绝对路径（落在 <数据目录>/replay-scripts/<id>.mjs）。
   * 与完整请求日志的「填入测试」是同一个生成器，区别只在这里的 body 未经插件改写。
   */
  const replayScript = (id: string) =>
    request<{ ok: boolean; path: string; bytes: number }>(
      `/api/request-captures/${encodeURIComponent(id)}/replay-script`,
      'POST',
    )
  return { list, detail, remove, clear, replayScript }
}

