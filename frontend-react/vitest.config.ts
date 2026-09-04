// Vitest 配置：前端自动化测试能力（P2 测试欠账清零，2026-09-05）
// jsdom 环境支撑 localStorage/location 等浏览器 API；仅测纯逻辑模块（不依赖真实组件渲染）
import { defineConfig } from 'vitest/config'

export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['src/**/__tests__/**/*.test.{ts,tsx}'],
    globals: true,
  },
})
