// 后端就绪探测：桌面壳的窗口会先于内嵌 Loadout Server 打开，前端必须等后端
// 真正可访问后再进入登录/控制台，否则首批 /api/* 请求会打到尚未监听的端口，
// 冒出「Loadout Server 连接失败」。
//
// 本模块保持「纯逻辑、无 Vue 依赖」，方便用 node --test 直接测试；
// 响应式状态与单例封装在 lib/boot.ts。

// READINESS_PATH 就绪探测端点：公开、无认证，后端开始监听后才会返回 2xx。
export const READINESS_PATH = '/api/health'

// PROBE_TIMEOUT_MS 单次探测超时。后端未监听时连接会被立即拒绝，这里的超时主要
// 兜住「连上了但迟迟不响应」的极端情况，避免等待被卡死。
export const PROBE_TIMEOUT_MS = 2500

// SLOW_AFTER_MS 超过该时长仍未就绪，就在加载页上提示「启动较慢」。
export const SLOW_AFTER_MS = 8000

export interface WaitForBackendOptions {
  // probe 探测一次后端是否可访问；返回 true 表示已就绪。
  probe: (timeoutMs: number) => Promise<boolean>
  sleep: (ms: number) => Promise<void>
  now?: () => number
  intervalMs?: number
  slowAfterMs?: number
  // onTick 每轮失败后回调，用于向加载页汇报已等待时长与是否已偏慢。
  onTick?: (elapsedMs: number, slow: boolean) => void
}

// probeBackend 请求就绪端点一次：2xx 视为就绪，其余（含网络错误、502 代理失败）都算未就绪。
export async function probeBackend(
  timeoutMs: number = PROBE_TIMEOUT_MS,
  fetchImpl: typeof fetch = fetch,
): Promise<boolean> {
  const controller = new AbortController()
  const timer = setTimeout(() => controller.abort(), timeoutMs)
  try {
    const resp = await fetchImpl(READINESS_PATH, { signal: controller.signal, cache: 'no-store' })
    return resp.ok
  } catch {
    return false
  } finally {
    clearTimeout(timer)
  }
}

// waitForBackend 反复探测直到成功；永不 reject，resolve 即代表后端可访问。
// 探测失败时按 intervalMs 轮询，超过 slowAfterMs 后放慢节奏并置 slow 提示。
export async function waitForBackend(options: WaitForBackendOptions): Promise<void> {
  const now = options.now ?? (() => Date.now())
  const intervalMs = options.intervalMs ?? 350
  const slowAfterMs = options.slowAfterMs ?? SLOW_AFTER_MS
  const startedAt = now()
  for (;;) {
    let ready = false
    try {
      ready = await options.probe(PROBE_TIMEOUT_MS)
    } catch {
      ready = false
    }
    if (ready) return
    const elapsed = now() - startedAt
    const slow = elapsed >= slowAfterMs
    options.onTick?.(elapsed, slow)
    await options.sleep(slow ? 1200 : intervalMs)
  }
}
