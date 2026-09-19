import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

// 后端地址可被环境变量覆盖（debug.ps1 / npm run dev 场景）：
//   LOADOUT_SERVER_ADDR=:5009  或  VITE_BACKEND_PORT=5009
const backendPort =
  process.env.VITE_BACKEND_PORT ||
  (process.env.LOADOUT_SERVER_ADDR || '').replace(/^:/, '') ||
  '3000'
const targetUrl = `http://127.0.0.1:${backendPort}`

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    port: 5273,
    host: '0.0.0.0', // 监听所有网卡（支持 IPv4 + IPv6），避免 IPv6-only 导致 Desktop 无法连接
    // 开发模式（npm run dev）下，把后端接口代理到 Loadout 服务器（单端口 :3000）：
    //   /api/* → 管理 API（session）
    //   /v1/*  → 模型 API（sk- key）
    //   /mcp/* → MCP 端点（header key）
    proxy: {
      '/api': {
        target: targetUrl,
        changeOrigin: true,
        cookieDomainRewrite: 'localhost', // 将后端 Cookie 的 domain 重写为 localhost
      },
      '/v1': {
        target: targetUrl,
        changeOrigin: true,
        cookieDomainRewrite: 'localhost',
      },
      '/mcp': {
        target: targetUrl,
        changeOrigin: true,
        cookieDomainRewrite: 'localhost',
      },
    },
  },
})
