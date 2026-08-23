import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import { api, request } from '@/lib/api'
import type { Overview } from '@/lib/types'
import { emitter } from '@/lib/emitter'

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

  async function check() {
    try {
      overview.value = await api<Overview>('/api/overview')
      authenticated.value = true
    } catch {
      authenticated.value = false
    } finally {
      checked.value = true
    }
  }

  async function login(username: string, password: string) {
    await request('/api/login', 'POST', { username, password })
    await check()
  }

  // ssoLogin 用桌面端签发的短时效 token 换取会话（托盘「打开网页」免登录入口）。
  async function ssoLogin(token: string) {
    await request('/api/sso/login', 'POST', { token })
    await check()
  }

  async function logout() {
    await request('/api/logout', 'POST')
    overview.value = undefined
    authenticated.value = false
  }

  return { checked, authenticated, overview, displayName, check, login, ssoLogin, logout }
})
