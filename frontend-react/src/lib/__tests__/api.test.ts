// 前端纯逻辑单元测试（能力建设首包，2026-09-05）
// 覆盖：token 生命周期（读写清除）、角色分流 redirectByRole、业务错误码文案映射。
// 定位：只测纯逻辑/浏览器 API 层（token、角色分流、错误码分级、请求超时闸门）。
// 组件渲染测试在 src/pages/**/__tests__/*.test.tsx 与 src/pages/__tests__/smoke.test.tsx，
// 已用 @testing-library 落地，不在本文件范围内（此注 2026-09-25 校正，原写"另立再评估"已过时）。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// toastError 内部调 tdesign MessagePlugin.warning/error（依赖真实 DOM 挂载），单测 mock 掉只验文案与返回值
vi.mock('tdesign-react', () => ({
  MessagePlugin: { warning: vi.fn(), error: vi.fn() },
}))

import { MessagePlugin } from 'tdesign-react'
import { AUTH, ERROR_CODE_MESSAGES, clearToken, getToken, redirectByRole, setToken, toastError } from '../api'

const warningMock = MessagePlugin.warning as ReturnType<typeof vi.fn>
const errorMock = MessagePlugin.error as ReturnType<typeof vi.fn>

afterEach(() => {
  localStorage.clear()
  // location.href 是只读赋值，jsdom 下整体替换测试专用（每例重置防串）
  delete (window as any).location
  warningMock.mockClear()
  errorMock.mockClear()
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
  // mockLocation 把 window.location 整体替换为可写对象（jsdom 下 href 可赋值），
  // 供 redirectByRole 断言跳转目标；每例重建防用例间串扰
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

  // ===== G-13 错误码全量迁移（2026-09-24）=====
  it('登录态类错误走 error 级而非黄色可忽略的 warning', () => {
    // token_revoked 用 warning 弹，用户会以为"再点一次就好"，实际怎么点都是 401
    expect(toastError({ code: 401, error_code: 'token_revoked' })).toBe(true)
    expect(errorMock).toHaveBeenCalledWith('这个登录已经过期了，重新登录一次')
    expect(warningMock).not.toHaveBeenCalled()
  })

  it('域拒码不写通用话，精确 message 不被覆盖', () => {
    // 后端 deal_rejected 的 message 本身就是"阶段不能往回退…"这种精确文案，
    // 前端若登记一句"这张单暂时不能这么操作"反而会把它盖掉
    expect(toastError({ code: 400, error_code: 'deal_rejected', message: '阶段不能往回退', reason: 'stage_backward' })).toBe(true)
    expect(warningMock).toHaveBeenCalledWith('阶段不能往回退')
    expect(errorMock).not.toHaveBeenCalled()
  })

  it('未登记的新码不静默：回落 message 且按 warning 弹', () => {
    expect(toastError({ code: 400, error_code: 'some_future_code', message: '后端新加的文案' })).toBe(true)
    expect(warningMock).toHaveBeenCalledWith('后端新加的文案')
  })

  it('码集覆盖后端实际发出的全部 error_code', () => {
    // 这份清单是从后端源码镜像来的（internal/api/code.go 的 codeName + 各处显式字面量），
    // 后端加码而忘了同步这里时本例会红——比"线上遇到才发现文案是英文/空白"便宜得多
    const BACKEND_CODES = [
      'param_error', 'unauthorized', 'forbidden', 'not_found', 'rate_limited', 'biz_error', 'internal_error',
      'token_revoked', 'must_change_password', 'invalid_api_key', 'channel_config_incomplete',
      'deal_rejected', 'outreach_rejected', 'acquisition_rejected', 'pack_tier_denied',
    ]
    // 注意不含 code.go 里的 "ok"：成功响应根本不走 toastError，登记它只会多一格死码
    expect(Object.keys(ERROR_CODE_MESSAGES).sort()).toEqual(BACKEND_CODES.slice().sort())
  })
})

// P1-13 复核批（2026-09-15）：请求层默认 30s 超时——后端挂死时不再让按钮永久 pending；
// timeoutMs:0 显式关闭（聊天等长任务保留通道）。
describe('apiFetch 超时闸门', () => {
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('挂死请求 30s 后被掐断并返回错误信封（AUTH 不 reject，调用方走 else 分支）', async () => {
    vi.useFakeTimers()
    const fakeFetch = vi.fn(
      (_u: string, opts: any) =>
        new Promise((_res, reject) => {
          opts?.signal?.addEventListener('abort', () => reject(new Error('AbortError')))
        }),
    )
    vi.stubGlobal('fetch', fakeFetch)
    const p = AUTH('/api/v1/hang')
    await vi.advanceTimersByTimeAsync(30_000)
    const j: any = await p
    expect(fakeFetch).toHaveBeenCalledTimes(1)
    expect(j.code).toBe(-1)
    expect(String(j.message)).toContain('网络')
  })

  it('timeoutMs=0 不装定时器：长任务永不被本地超时误掐', async () => {
    vi.useFakeTimers()
    let aborted = false
    vi.stubGlobal('fetch', vi.fn((_u: string, opts: any) => {
      opts?.signal?.addEventListener('abort', () => { aborted = true })
      return new Promise(() => {})
    }))
    void AUTH('/api/v1/long-chat', { timeoutMs: 0 })
    await vi.advanceTimersByTimeAsync(120_000)
    expect(aborted).toBe(false)
  })
})
