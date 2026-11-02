// vite.config.js 配置前端开发服务器：静态资源走默认，/api 与 /uploads 代理到本机 API（8090）。
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

const apiTarget = process.env.PULSEFEED_API || 'http://127.0.0.1:8090'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': { target: apiTarget, changeOrigin: true },
      '/uploads': { target: apiTarget, changeOrigin: true },
    },
  },
})
