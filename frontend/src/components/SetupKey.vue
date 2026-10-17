<script setup lang="ts">
import { reactive, ref } from 'vue'
import { RiEyeLine, RiEyeOffLine, RiKey2Line, RiRobot2Line } from '@remixicon/vue'

defineProps<{
  pending?: boolean
  error?: string
}>()

const emit = defineEmits<{
  submit: [key: string]
}>()

const key = reactive({ value: '', confirm: '' })
const keyVisible = ref(false)
const confirmVisible = ref(false)
const localError = ref('')

function onSubmit() {
  localError.value = ''
  if (key.value.length < 6) {
    localError.value = '访问密钥至少 6 位'
    return
  }
  if (key.value !== key.confirm) {
    localError.value = '两次输入的密钥不一致'
    return
  }
  emit('submit', key.value)
}
</script>

<template>
  <section class="w-full max-w-sm" aria-labelledby="setup-title">
    <Card class="rounded-md shadow-sm">
      <CardHeader class="space-y-3">
        <div class="grid size-10 place-items-center bg-primary text-primary-foreground">
          <RiRobot2Line size="22" aria-hidden="true" />
        </div>
        <div class="space-y-1">
          <CardTitle id="setup-title">初始化 Loadout</CardTitle>
          <CardDescription>设置一个访问密钥，之后用它登录管理台</CardDescription>
        </div>
      </CardHeader>
      <CardContent>
        <form class="space-y-4" @submit.prevent="onSubmit">
          <div class="space-y-2">
            <Label for="setup-key">访问密钥</Label>
            <div class="relative">
              <Input
                id="setup-key"
                v-model="key.value"
                :type="keyVisible ? 'text' : 'password'"
                autocomplete="new-password"
                placeholder="至少 6 位"
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
          <div class="space-y-2">
            <Label for="setup-key-confirm">确认密钥</Label>
            <div class="relative">
              <Input
                id="setup-key-confirm"
                v-model="key.confirm"
                :type="confirmVisible ? 'text' : 'password'"
                autocomplete="new-password"
                placeholder="再输入一次"
                required
                :disabled="pending"
                class="pr-10"
              />
              <TooltipProvider>
                <Tooltip>
                  <TooltipTrigger as-child>
                    <button
                      type="button"
                      :aria-label="confirmVisible ? '隐藏密钥' : '显示密钥'"
                      :disabled="pending"
                      class="absolute top-0 right-0 inline-flex size-9 items-center justify-center text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring rounded-sm disabled:opacity-50 disabled:pointer-events-none"
                      @click="confirmVisible = !confirmVisible"
                    >
                      <RiEyeOffLine v-if="confirmVisible" size="17" aria-hidden="true" />
                      <RiEyeLine v-else size="17" aria-hidden="true" />
                    </button>
                  </TooltipTrigger>
                  <TooltipContent>{{ confirmVisible ? '隐藏密钥' : '显示密钥' }}</TooltipContent>
                </Tooltip>
              </TooltipProvider>
            </div>
          </div>
          <Alert v-if="localError || error" variant="destructive" role="alert">
            <RiKey2Line size="16" aria-hidden="true" />
            <AlertDescription>{{ localError || error }}</AlertDescription>
          </Alert>
          <Button type="submit" class="w-full" :disabled="pending || !key.value || !key.confirm">
            {{ pending ? '正在设置…' : '完成初始化' }}
          </Button>
        </form>
      </CardContent>
    </Card>
  </section>
</template>

<style scoped>
:deep(input::-ms-reveal),
:deep(input::-ms-clear) {
  display: none;
}
</style>
