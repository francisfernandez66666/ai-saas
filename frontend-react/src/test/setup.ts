import '@testing-library/jest-dom/vitest'
import { cleanup, configure } from '@testing-library/react'
import { afterEach, vi } from 'vitest'

// D2 修复(2026-09-16，AUDIT_DEFECT_VERIFY)：UATFOLLOWUP F4 把 asyncUtilTimeout 写进了
// vitest.config——但 **asyncUtilTimeout 不是 vitest 的合法配置键**（vitest dist 零引用，被静默忽略），
// testing-library 的 waitFor/findBy* 仍走自己的 1s 默认值（探针实测 1004ms 超时）。
// 冷启动/高负载下编排跑必现假失败（如 StrategyTab.test.tsx:78）。
// 正确姿势是在 setup 里 configure()，此处补上。
configure({ asyncUtilTimeout: 8000 })

// 每个用例卸载组件并清空浏览器存储，避免 jsdom 中用例互相污染
afterEach(() => {
  cleanup()
  localStorage.clear()
  sessionStorage.clear()
  vi.restoreAllMocks()
})
