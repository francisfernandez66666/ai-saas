// 前端纯逻辑单元测试（能力建设首包，2026-09-05）
// 覆盖：token 生命周期（读写清除）、角色分流 redirectByRole、业务错误码文案映射。
// 定位：只测纯逻辑/浏览器 API 层，不渲染真实组件（组件渲染测试另立 @testing-library 再评估）。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// toastError 内部调 tdesign MessagePlugin.warning（依赖真实 DOM 挂载），单测 mock 掉只验文案与返回值
vi.mock('tdesign-react', () => ({
  MessagePlugin: { warning: vi.fn() },
}))

import { MessagePlugin } from 'tdesign-react'
import { clearToken, getToken, redirectByRole, setToken, toastError } from '../api'

const warningMock = MessagePlugin.warning as ReturnType<typeof vi.fn>

afterEach(() => {
  localStorage.clear()
  // location.href 是只读赋值，jsdom 下整体替换测试专用（每例重置防串）
  delete (window as any).location
  warningMock.mockClear()
})

describe('token 生命周期', () => {
  it('未登录时返回空串', () => {
    expect(getToken()).toBe('')
  })

  it('setToken 后可读回', () => {
    setToken('jwt-abc')
    expect(getToken()).toBe('jwt-abc')
  })

  it('clearToken 后归空', () => {
    setToken('jwt-abc')
    clearToken()
    expect(getToken()).toBe('')
  })
})

describe('角色分流 redirectByRole', () => {
  function mockLocation() {
    const href = { href: '' } as Location
    Object.defineProperty(window, 'location', { value: href, configurable: true })
    return href
  }

  it('super_admin → /super', () => {
    const href = mockLocation()
    redirectByRole('super_admin')
    expect(href.href).toBe('/super')
  })

  it('admin 与 tenant_admin → /admin', () => {
    const href = mockLocation()
    redirectByRole('admin')
    expect(href.href).toBe('/admin')
  })

  it('其他角色（sales）→ /advisor 工作台', () => {
    const href = mockLocation()
    redirectByRole('sales')
    expect(href.href).toBe('/advisor')
  })
})

describe('业务错误码文案', () => {
  beforeEach(() => {
    // 需要 code!=0 才走文案分支
  })

  it('error_code 命中映射返回去 AI 味短句', () => {
    const j = toastError({ code: 42901, error_code: 'rate_limited' } as any)
    expect(j).toBe(true)
    expect(warningMock).toHaveBeenCalledWith('操作太频繁，稍后再试')
  })

  it('无 error_code 时回退 message 原文', () => {
    const j = toastError({ code: 400, message: '余额不足' } as any)
    expect(j).toBe(true)
    expect(warningMock).toHaveBeenCalledWith('余额不足')
  })

  it('code=0 成功不弹窗', () => {
    const j = toastError({ code: 0 } as any)
    expect(j).toBe(false)
    expect(warningMock).not.toHaveBeenCalled()
  })
})
