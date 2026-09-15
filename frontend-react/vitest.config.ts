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
    // G3 稳定性收口(2026-09-14)：test_all 与 go build/vite build 并发跑时，
    // 默认 5s 单测超时在 CPU 争抢下会误报（jsdom 渲染+多路 AUTH mock 用例偶发 >5s）。
    // 放宽到 20s，消除负载抖动导致的假失败，不改变用例逻辑。
    testTimeout: 20000,
    hookTimeout: 20000,
    // UATFOLLOWUP F4 修复(2026-09-15)：冷启动（transform/import 峰值 35s+87s）下
    // testing-library 的 findBy*/waitFor 默认 1s 异步等待也会超时，首跑 flaky。
    // asyncUtilTimeout 放宽到 8s，只影响异步查询等待上限，不改用例逻辑。
    asyncUtilTimeout: 8000,
  },
})
