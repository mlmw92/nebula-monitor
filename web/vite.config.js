import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 前端工程根目录即 web/，构建产物输出到 dist/。
// 产物由 Server 从磁盘目录托管（默认 /etc/monitor-server/web，见 internal/server/api/spa.go 与 server.yaml 的 webDir），不内嵌进二进制。
export default defineConfig({
  root: '.',
  plugins: [vue()],
  base: '/',
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    assetsDir: 'assets',
  },
  server: {
    port: 5173,
    proxy: {
      // 开发态将 API 与 WebSocket 代理到本地 Server（默认 8080）
      '/api': 'http://localhost:8080',
      '/ws': { target: 'ws://localhost:8080', ws: true, changeOrigin: true },
    },
  },
})
