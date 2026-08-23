import { createApp } from 'vue'
import { createPinia } from 'pinia'
import './style.css'
import 'shadcn-vue-cdn/style.css'
import App from './App.vue'
import router from './router'
import { ShadcnVue } from 'shadcn-vue-cdn'

// 注意：不要在挂载前手动清理 URL 里的 ?sso= —— router/index.ts 模块加载时会
// 先读取并缓存 token、再清理地址栏（先读后清）。若在这里提前删掉 URL 参数，
// 缓存的 token 会变成 null，桌面端免登录链路会失效。

createApp(App).use(createPinia()).use(router).use(ShadcnVue).mount('#app')
