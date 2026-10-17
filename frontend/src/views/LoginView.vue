<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAsyncTask } from '@/composables/useAsyncTask'
import Login from '@/components/Login.vue'
import SetupKey from '@/components/SetupKey.vue'

const router = useRouter()
const auth = useAuthStore()
const error = ref('')
const { pending, run } = useAsyncTask()

// 启动时查询是否已初始化（未初始化 → 显示"设置访问密钥"表单）。
onMounted(() => {
  void auth.fetchSetupStatus()
})

async function submitLogin(value: string) {
  error.value = ''
  try {
    await run(() => auth.login(value))
    await router.push('/')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '登录失败'
  }
}

async function submitSetup(value: string) {
  error.value = ''
  try {
    await run(() => auth.setupKey(value))
    await router.push('/')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '初始化失败'
  }
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-muted/40 px-4">
    <SetupKey v-if="!auth.initialized" :error="error" @submit="submitSetup" />
    <Login v-else :pending="pending" :error="error" @submit="submitLogin" />
  </main>
</template>
