// 启动门控：把「等后端就绪」的状态做成单例，供 App.vue（显示加载页）与
// router（放行前先等就绪）共用。ensureBackendReady 幂等：多次调用共享同一次轮询，
// 后端可访问时同一 Promise 才 resolve，因此 App 之后才可能渲染 RouterView。

import { ref } from 'vue'
import { probeBackend, waitForBackend } from './readiness'

// backendReady 后端是否已就绪（就绪后保持 true）。
export const backendReady = ref(false)
// bootSlow 启动是否已偏慢（用于加载页上的提示与重试入口）。
export const bootSlow = ref(false)
// bootElapsedMs 已等待时长，加载页用来显示「已等待 N 秒」。
export const bootElapsedMs = ref(0)

let bootPromise: Promise<void> | null = null

// ensureBackendReady 返回「后端已就绪」的 Promise；首次调用开始轮询，之后复用同一 Promise。
export function ensureBackendReady(): Promise<void> {
  if (!bootPromise) {
    bootPromise = waitForBackend({
      probe: (timeoutMs) => probeBackend(timeoutMs),
      sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
      onTick: (elapsedMs, slow) => {
        bootElapsedMs.value = elapsedMs
        bootSlow.value = slow
      },
    }).then(() => {
      backendReady.value = true
      bootSlow.value = false
    })
  }
  return bootPromise
}
