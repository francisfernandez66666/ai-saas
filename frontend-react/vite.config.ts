import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// 开发期代理 /api 到 Go 后端（9090），生产由 Go 同域托管 dist，无需代理
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://localhost:9090',
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        // D1：路由级 lazy 先保证页面可用；TDesign 自动分块避免手工切分触发 ES 循环初始化。
      },
    },
    chunkSizeWarningLimit: 300,
  },
})
