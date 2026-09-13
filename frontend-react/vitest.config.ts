// Vitest 配置：前端自动化测试能力（P2 测试欠账清零，2026-09-05）
// jsdom 环境支撑浏览器 API；纯逻辑模块和关键组件冒烟共用同一套配置
import { defineConfig } from 'vitest/config'

// 导出 Vitest 配置：jsdom 环境 + __tests__ 用例 + testing-library 断言扩展
export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['src/**/__tests__/**/*.test.{ts,tsx}'],
    setupFiles: ['./src/test/setup.ts'],
    globals: true,
  },
})
