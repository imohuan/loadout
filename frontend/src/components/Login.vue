<script setup lang="ts">
import { ref } from 'vue'
import { RiEyeLine, RiEyeOffLine, RiKey2Line, RiRobot2Line } from '@remixicon/vue'

defineProps<{
  pending?: boolean
  error?: string
}>()

const emit = defineEmits<{
  submit: [key: string]
}>()

const key = ref('')
const keyVisible = ref(false)

function onSubmit() {
  if (key.value.trim()) emit('submit', key.value)
}
</script>

<template>
  <section class="w-full max-w-sm" aria-labelledby="login-title">
    <Card class="rounded-md shadow-sm">
      <CardHeader class="space-y-3">
        <div class="grid size-10 place-items-center bg-primary text-primary-foreground">
          <RiRobot2Line size="22" aria-hidden="true" />
        </div>
        <div class="space-y-1">
          <CardTitle id="login-title">登录 Loadout</CardTitle>
          <CardDescription>进入模型、能力与转发策略管理控制台</CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        <form class="space-y-4" @submit.prevent="onSubmit">
          <div class="space-y-2">
            <Label for="access-key">访问密钥</Label>
            <div class="relative">
              <Input
                id="access-key"
                v-model="key"
                :type="keyVisible ? 'text' : 'password'"
                autocomplete="current-password"
                placeholder="请输入访问密钥"
                required
                :disabled="pending"
                class="pr-10"
              />
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <button
                      type="button"
                      :aria-label="keyVisible ? '隐藏密钥' : '显示密钥'"
                      :disabled="pending"
                      class="absolute top-0 right-0 inline-flex size-9 items-center justify-center text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-sm disabled:opacity-50 disabled:pointer-events-none"
                      @click="keyVisible = !keyVisible"
                    >
                      <RiEyeOffLine v-if="keyVisible" size="17" aria-hidden="true" />
                      <RiEyeLine v-else size="17" aria-hidden="true" />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent>{{ keyVisible ? '隐藏密钥' : '显示密钥' }}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </div>
          <Alert v-if="error" variant="destructive" role="alert">
            <RiKey2Line size="16" aria-hidden="true" />
            <AlertDescription>{{ error }}</AlertDescription>
          </Alert>
          <Button type="submit" class="w-full" :disabled="pending || !key.trim()">
            {{ pending ? '正在登录…' : '登录' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </section>
</template>

<style scoped>
/* 隐藏 WebView2/Edge 自动注入的密码 reveal 按钮，避免与上方自定义 toggle 重复显示。 */
:deep(input::-ms-reveal),
:deep(input::-ms-clear) {
  display: none;
}
</style>
