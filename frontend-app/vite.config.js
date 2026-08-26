import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// 构建输出到 dist；base 使用相对路径以兼容 Gin 子路径托管与 Capacitor file 协议
export default defineConfig({
  plugins: [vue()],
  base: './',
  build: { outDir: 'dist', emptyOutDir: true },
  server: {
    proxy: {
      '/api': 'http://localhost:9090',
    }
  }
})
