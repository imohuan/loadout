import { createApp } from 'vue'
import { createPinia } from 'pinia'
// 顺序不能反：style.css（Tailwind 入口）必须在 shadcn-vue 的样式之前加载。
// 反过来的话，Tailwind 的 preflight（`border: 0 solid`）会排在 shadcn 的
// `* { border-color: var(--border) }` 之后，把所有边框重置成 currentColor（白色），
// 侧边栏的显示类也会被压掉。
import './style.css'
import 'shadcn-vue-cdn/style.css'
import App from './App.vue'
import router from './router'
import { ShadcnVue } from 'shadcn-vue-cdn'

// 注意：不要在挂载前手动清理 URL 里的 ?sso= —— router/index.ts 模块加载时会
// 先读取并缓存 token、再清理地址栏（先读后清）。若在这里提前删掉 URL 参数，
// 缓存的 token 会变成 null，桌面端免登录链路会失效。

// 全局快捷键：Ctrl+Shift+Alt+D 触发 debugger
window.addEventListener('keydown', (e) => {
  if (e.ctrlKey && e.shiftKey && e.altKey && e.key.toLowerCase() === 'd') {
    debugger
  }
})

createApp(App).use(createPinia()).use(router).use(ShadcnVue).mount('#app')
