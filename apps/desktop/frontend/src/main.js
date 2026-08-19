import { createApp } from 'vue'
import App from './App.vue'
import { setupWindowControls } from './composables/useWindow.js'
import '@wailsio/runtime'

import './style.css'

setupWindowControls()
createApp(App).mount('#app')