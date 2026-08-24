import { defineConfig } from 'vite'
import { fileURLToPath, URL } from 'node:url'
import vue from '@vitejs/plugin-vue'
import tailwindcss from '@tailwindcss/vite'

const targetUrl = 'http://127.0.0.1:3000'

// https://vite.dev/config/
export default defineConfig({
  plugins: [vue(), tailwindcss()],
  // dev 模式预构建 subpath import（Vite ESM 解析需显式 include）
  optimizeDeps: {
    include: ['highlight.js/lib/common', 'dompurify'],
  },
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
