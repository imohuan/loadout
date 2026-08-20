<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { useAsyncTask } from '@/composables/useAsyncTask'
import Login from '@/components/Login.vue'

const router = useRouter()
const auth = useAuthStore()
const form = reactive({ username: 'admin', password: '' })
const error = ref('')
const { pending, run } = useAsyncTask()
async function submit() {
  error.value = ''
  try {
    await run(() => auth.login(form.username, form.password))
    await router.push('/')
  } catch (reason) {
    error.value = reason instanceof Error ? reason.message : '登录失败'
  }
}
</script>

<template>
  <main class="grid min-h-dvh place-items-center bg-muted/40 px-4">
    <Login
      v-model:username="form.username"
      v-model:password="form.password"
      :pending="pending"
      :error="error"
      @submit="submit"
    />
  </main>
</template>
