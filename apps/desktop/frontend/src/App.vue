<script setup>
import { ref, reactive, onMounted, onUnmounted } from 'vue'
import { useApi } from './composables/useApi.js'
import { updateTitlebarPort } from './composables/useWindow.js'

const savedPort = parseInt(localStorage.getItem('myapp_port') || '8866')
const apiPort = ref(savedPort)
const { api } = useApi(apiPort)
const running = ref(false)
const showSettings = ref(false)
const form = reactive({ port: savedPort })

async function refreshStatus() {
  const s = await api('/status')
  if (s !== null) {
    running.value = s.running
    if (s.port) apiPort.value = s.port
  }
  updateTitlebarPort(apiPort.value)
}

async function openSettings() {
  showSettings.value = true
  const c = await api('/config')
  if (c) form.port = c.port || 8866
}

async function saveSettings() {
  await api('/config', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ port: form.port })
  })
  localStorage.setItem('myapp_port', form.port)
  apiPort.value = form.port
  showSettings.value = false
  setTimeout(refreshStatus, 1500)
}

let interval = null
onMounted(() => {
  updateTitlebarPort(apiPort.value)
  refreshStatus()
  interval = setInterval(refreshStatus, 5000)
})
onUnmounted(() => clearInterval(interval))
</script>

<template>
  <div style="height:calc(100vh - 32px);display:flex;align-items:center;justify-content:center;flex-direction:column;gap:16px;">
    <div style="font-size:24px;font-weight:600;">Wails v3 App Template</div>
    <div style="color:#666;">
      服务状态: <span :style="{color:running?'#16a34a':'#dc2626',fontWeight:600}">{{ running ? '运行中' : '已停止' }}</span>
      端口: {{ apiPort }}
    </div>
    <button @click="openSettings" style="padding:8px 16px;cursor:pointer;">打开设置</button>

    <div v-if="showSettings" style="position:fixed;inset:0;background:rgba(0,0,0,0.3);display:flex;align-items:center;justify-content:center;z-index:50;" @click.self="showSettings=false">
      <div style="background:#fff;padding:24px;border-radius:8px;min-width:300px;">
        <h3 style="margin:0 0 16px;">设置</h3>
        <label style="display:block;margin-bottom:8px;">端口: <input v-model.number="form.port" type="number" min="1" max="65535" style="width:80px;" /></label>
        <div style="display:flex;gap:8px;justify-content:flex-end;margin-top:16px;">
          <button @click="showSettings=false" style="padding:6px 16px;cursor:pointer;">取消</button>
          <button @click="saveSettings" style="padding:6px 16px;cursor:pointer;background:#000;color:#fff;border:none;border-radius:2px;">保存</button>
        </div>
      </div>
    </div>
  </div>
</template>
