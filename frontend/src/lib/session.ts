// 401 的二次确认：接口回 401 不等于「你真的被登出了」。
//
// 曾经踩过的坑：模型状态页批量测 Key 时，上游（workbuddy 过期 token）回 401，
// 后台代理原样透传，前端的 fetch 包装只按状态码判断，于是把用户踢到登录页。
// 刷新一下又好了 —— 因为会话从头到尾都是好的。
//
// 所以收到 401 后先问后端一句「会话还在吗」：只有后端明确回 401 才算登出；
// 网络错误、502、后端正在重启都属于「不确定」，绝不能据此清登录态。

import { emitter } from './emitter.ts'

export type SessionProbe = 'alive' | 'lost' | 'unknown'

// SESSION_PROBE_PATH 验证会话用的端点：需要登录、只读、开销极小。
export const SESSION_PROBE_PATH = '/api/overview'

// SESSION_PROBE_CACHE_MS 确认结果缓存时长。批量测试可能同时回好几个 401，
// 这段时间内复用同一次结论，避免每个失败请求都多打一次后端。
export const SESSION_PROBE_CACHE_MS = 1500

let lastProbe: SessionProbe | null = null
let lastProbeAt = 0
let inflight: Promise<SessionProbe> | null = null

/** probeSession 直接问一次后端会话是否仍有效（不经过会发 unauthorized 事件的 api 包装）。 */
export async function probeSession(fetchImpl: typeof fetch = fetch): Promise<SessionProbe> {
  try {
    const resp = await fetchImpl(SESSION_PROBE_PATH, {
      credentials: 'same-origin',
      cache: 'no-store',
    })
    if (resp.status === 401) return 'lost'
    if (resp.ok) return 'alive'
    // 5xx / 其他错误：后端自身有问题，无法判断登录态。
    return 'unknown'
  } catch {
    // 网络中断、后端重启中：同样不能判定为登出。
    return 'unknown'
  }
}

/** confirmSessionLost 去抖后的会话确认：短时间内多个 401 共享一次请求与结论。 */
export function confirmSessionLost(fetchImpl: typeof fetch = fetch): Promise<SessionProbe> {
  const now = Date.now()
  if (lastProbe && now - lastProbeAt < SESSION_PROBE_CACHE_MS) {
    return Promise.resolve(lastProbe)
  }
  if (inflight) return inflight
  inflight = probeSession(fetchImpl)
    .then((result) => {
      lastProbe = result
      lastProbeAt = Date.now()
      return result
    })
    .finally(() => {
      inflight = null
    })
  return inflight
}

/** resetSessionProbe 清掉缓存（登录/登出后调用，避免沿用旧结论）。 */
export function resetSessionProbe() {
  lastProbe = null
  lastProbeAt = 0
  inflight = null
}

/**
 * reportUnauthorized 收到 401 时的统一处理：确认会话真的失效再广播 unauthorized。
 *
 * 接口的 401 有两种来源：本机会话过期，以及上游的 401 被代理原样回传（模型测试、
 * 其他转发接口）。后者若被当成登出，用户只是点了下「测试」就被踢到登录页。
 */
export async function reportUnauthorized() {
  if ((await confirmSessionLost()) !== 'lost') return
  emitter.emit('unauthorized')
}
