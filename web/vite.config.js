import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 管理后台构建配置：产物输出到 web/dist，由 apps/server 通过 go:embed 打包。
export default defineConfig({
  plugins: [vue()],
  server: {
    host: '0.0.0.0', // 监听所有网卡（支持 IPv4 + IPv6），避免 IPv6-only 导致 Desktop 无法连接
    // 开发模式（npm run dev）下，把后端接口代理到 Loadout 服务器（单端口 :3000）：
    //   /api/* → 管理 API（session）
    //   /v1/*  → 模型 API（sk- key）
    //   /mcp/* → MCP 端点（header key）
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:3000',
        changeOrigin: true,
        cookieDomainRewrite: 'localhost', // 将后端 Cookie 的 domain 重写为 localhost
      },
      '/v1': {
        target: 'http://127.0.0.1:3000',
        changeOrigin: true,
        cookieDomainRewrite: 'localhost',
      },
      '/mcp': {
        target: 'http://127.0.0.1:3000',
        changeOrigin: true,
        cookieDomainRewrite: 'localhost',
      }
    }
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true
  },
})
