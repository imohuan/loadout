import { createApp } from 'vue'
import { createPinia } from 'pinia'
import './style.css'
import 'shadcn-vue-cdn/style.css'
import App from './App.vue'
import router from './router'
import { ShadcnVue } from 'shadcn-vue-cdn'

createApp(App).use(createPinia()).use(router).use(ShadcnVue).mount('#app')
