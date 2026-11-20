import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api, request, ApiError } from '@/lib/api'
import type { Overview } from '@/lib/types'
import { emitter } from '@/lib/emitter'
import { resetSessionProbe } from '@/lib/session'

export const useAuthStore = defineStore('auth', () => {
  const checked = ref(false)
  const authenticated = ref(false)
  const overview = ref<Overview>()
  const displayName = computed(() => overview.value?.app || 'Loadout')
  emitter.on('unauthorized', () => {
    authenticated.value = false
    checked.value = true
    overview.value = undefined
  })

  /** 探测登录态：ok=已登录；unauthorized=后端明确说未登录；error=这次没问到（不该当作登出）。 */
  async function check(): Promise<'ok' | 'unauthorized' | 'error'> {
    try {
      overview.value = await api<Overview>('/api/overview')
      authenticated.value = true
      checked.value = true
      return 'ok'
    } catch (err) {
      // 只有后端明确回 401 才清登录态。网络抖动、后端重启时 fetch 会直接抛错，
      // 那种情况并非登出：不要让一次临时故障把用户甩到登录页。
      if (err instanceof ApiError && err.status === 401) {
        authenticated.value = false
        overview.value = undefined
        checked.value = true
        return 'unauthorized'
      }
      return 'error'
    }
  }

  /** 是否已设置访问密钥（false = 首次使用，需初始化）。 */
  const initialized = ref(true)

  async function fetchSetupStatus() {
    try {
      const data = await api<{ initialized: boolean }>('/api/setup-status')
      initialized.value = data.initialized
    } catch {
      initialized.value = true
    }
    return initialized.value
  }

  /** 首次初始化访问密钥；成功后即已登录。 */
  async function setupKey(key: string) {
    await request('/api/setup-key', 'POST', { key })
    initialized.value = true
    resetSessionProbe()
    await check()
  }

  async function login(key: string) {
    await request('/api/login', 'POST', { password: key })
    resetSessionProbe()
    await check()
  }

  // ssoLogin 用桌面端签发的短时效 token 换取会话（托盘「打开网页」免登录入口）。
  async function ssoLogin(token: string) {
    await request('/api/sso/login', 'POST', { token })
    resetSessionProbe()
    await check()
  }

  async function logout() {
    await request('/api/logout', 'POST')
    overview.value = undefined
    authenticated.value = false
    resetSessionProbe()
  }

  return {
    checked,
    authenticated,
    overview,
    displayName,
    initialized,
    check,
    fetchSetupStatus,
    setupKey,
    login,
    ssoLogin,
    logout,
  }
})
