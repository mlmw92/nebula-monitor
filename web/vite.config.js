import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 前端工程根目录即 web/，构建产物输出到 dist/，由 Go embed 内嵌托管。
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
