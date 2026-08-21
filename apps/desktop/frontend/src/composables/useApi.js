import { ref, computed } from 'vue'

// 接收 apiPort (ref) 即可动态构建
export function useApi(apiPort) {
  const apiBase = computed(() => 'http://localhost:' + apiPort.value)

  async function api(path, opts) {
    try {
      const res = await fetch(apiBase.value + '/__api' + path, opts)
      return await res.json()
    } catch (e) {
      console.warn('API error:', path, e)
      return null
    }
  }

  return { api, apiBase }
}
