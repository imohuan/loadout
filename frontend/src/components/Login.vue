<script setup lang="ts">
import { ref } from 'vue'
import { RiEyeLine, RiEyeOffLine, RiLock2Line, RiRobot2Line } from '@remixicon/vue'

defineProps<{
  username: string
  password: string
  pending?: boolean
  error?: string
}>()

const emit = defineEmits<{
  'update:username': [value: string]
  'update:password': [value: string]
  submit: []
}>()

const passwordVisible = ref(false)
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
        <form class="space-y-4" @submit.prevent="emit('submit')">
          <div class="space-y-2">
            <Label for="username">用户名</Label>
            <Input
              id="username"
              :model-value="username"
              autocomplete="username"
              placeholder="请输入用户名"
              required
              :disabled="pending"
              @update:model-value="emit('update:username', $event)"
            />
          </div>
          <div class="space-y-2">
            <div class="flex items-center justify-between gap-3">
              <Label for="password">密码</Label>
              <span class="text-xs text-muted-foreground">由管理员配置</span>
            </div>
            <div class="relative">
              <Input
                id="password"
                :model-value="password"
                :type="passwordVisible ? 'text' : 'password'"
                autocomplete="current-password"
                placeholder="请输入密码"
                required
                :disabled="pending"
                class="pr-10"
                @update:model-value="emit('update:password', $event)"
              />
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <button
                      type="button"
                      :aria-label="passwordVisible ? '隐藏密码' : '显示密码'"
                      :disabled="pending"
                      class="absolute top-0 right-0 inline-flex size-9 items-center justify-center text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-sm disabled:opacity-50 disabled:pointer-events-none"
                      @click="passwordVisible = !passwordVisible"
                    >
                      <RiEyeOffLine v-if="passwordVisible" size="17" aria-hidden="true" />
                      <RiEyeLine v-else size="17" aria-hidden="true" />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent>{{ passwordVisible ? '隐藏密码' : '显示密码' }}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </div>
          <Alert v-if="error" variant="destructive" role="alert">
            <RiLock2Line size="16" aria-hidden="true" />
            <AlertDescription>{{ error }}</AlertDescription>
          </Alert>
          <Button type="submit" class="w-full" :disabled="pending">
            {{ pending ? '正在登录…' : '登录' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </section>
</template>

<style scoped>
/* 隐藏 WebView2/Edge 自动注入的密码 reveal 按钮，避免与上方自定义 toggle 重复显示。
   仅作用于本组件的密码 input（其他密码框没自定义 toggle，保留 WebView2 reveal）。
   ::-ms-reveal 是 Edge/WebView2 密码字段右侧内嵌的眼睛图标（浏览器标准 UI）。 */
:deep(input::-ms-reveal),
:deep(input::-ms-clear) {
  display: none;
}
</style>
