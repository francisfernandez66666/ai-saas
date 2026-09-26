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
import { AUTH, ERROR_CODE_MESSAGES, IMPERSONATE_TENANT_KEY, VISITOR_KEY, clearToken, getImpersonateTenant, getToken, invalidateSession, logoutAndRedirect, redirectByRole, setImpersonateTenant, setToken, toastError, verifySession } from '../api'
// 后端码清单的**生成物**（go run ./cmd/apidump -format errorcodes），不是本文件手抄的清单——
// 手抄那份与后端源码之间没有机制约束，后端加码而前端没登记时它会跟着一起漏，护栏就在最该响的时候沉默。
import { BACKEND_ERROR_CODES } from '../../types/error_codes.generated'

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

  it('码集与后端生成的清单双向对齐：漏登记红、留死码也红', () => {
    // 残项3(2026-09-25)：比对对象从"本文件手抄的清单"换成生成物，并补齐反向半边。
    //   正向（后端有 → 前端必须登记）：漏登记的新码会静默走未登记分支（只回传 message、
    //     分级恒为 warning），正是这张表存在的意义；
    //   反向（前端有 → 后端必须真发）：后端删码而前端留着，那格文案永远命中不了，
    //     还会在下一次改文案时被当成"仍在用的码"一起改错。
    const frontendCodes = Object.keys(ERROR_CODE_MESSAGES).sort()
    const backendCodes = [...BACKEND_ERROR_CODES].sort()
    expect(frontendCodes).toEqual(backendCodes)
    // 自证（反证前置）：生成物若因命令失败而变成空数组，上面的等式会在 []==[] 上假绿，
    // 故先钉"清单确实非空且含已知码"——这一条红说明生成物坏了，不是码集漂移。
    expect(backendCodes.length).toBeGreaterThanOrEqual(15)
    expect(backendCodes).toContain('param_error')
    expect(backendCodes).toContain('token_revoked')
  })

  it('生成物不含成功码 ok（成功响应不带 error_code，登记它是留一格死码）', () => {
    expect([...BACKEND_ERROR_CODES]).not.toContain('ok')
    expect(ERROR_CODE_MESSAGES).not.toHaveProperty('ok')
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

// ===== FIX-6（2026-09-27）：会话校验必须"失败形态不同 → 返回值不同" =====
// 旧签名 Promise<MeInfo | null> 把三种完全不同的失败（服务端判死会话 / 服务端要求改密 /
// 请求根本没发出去）压成同一个 null，调用方只好拿"本地还有没有 token"反推原因，
// 于是断网被推成"必须改密"。本段钉的是返回值这一层；UI 落点另见
// src/pages/__tests__/ProtectedRoute.test.tsx（两层分开才防住"改了返回值忘了改分支"）。
describe('verifySession 失败形态分形（FIX-6）', () => {
  afterEach(() => {
    invalidateSession() // 模块级 60s 缓存：不清则下一条用例命中上一条身份，打的不是本例分支
    vi.unstubAllGlobals()
  })

  // 只给 verifySession 真正读的东西：status / json() / clone().json()（jsdom 不保证有 Response 类）
  function meResp(status: number, body: unknown) {
    return {
      status,
      json: () => Promise.resolve(body),
      clone: () => ({ json: () => Promise.resolve(body) }),
    } as unknown as Response
  }
  const okBody = { code: 0, data: { id: 1, username: 'admin', role: 'admin', tenant_id: 1 } }

  it('本地无 token → anonymous，且一次请求都不发', async () => {
    const f = vi.fn()
    vi.stubGlobal('fetch', f)
    expect(await verifySession()).toEqual({ state: 'anonymous' })
    expect(f).not.toHaveBeenCalled()
  })

  it('fetch reject → network_error，token 原样保留（断网不是会话被判死）', async () => {
    setToken('jwt-online-lost')
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('Failed to fetch'))))
    expect(await verifySession()).toEqual({ state: 'network_error' })
    expect(getToken()).toBe('jwt-online-lost')
  })

  it('401 → revoked 并清 token', async () => {
    setToken('jwt-expired')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResp(401, { code: 401, error_code: 'token_expired' }))))
    expect(await verifySession()).toEqual({ state: 'revoked' })
    expect(getToken()).toBe('')
  })

  it('403 must_change_password → 单独一态且保留 token（改密接口要用它）', async () => {
    setToken('jwt-first-login')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResp(403, { code: 403, error_code: 'must_change_password' }))))
    expect(await verifySession()).toEqual({ state: 'must_change_password' })
    expect(getToken()).toBe('jwt-first-login')
  })

  it('403 其它原因 → forbidden 且不动 token（重登也还是 403，销毁会话是过度反应）', async () => {
    setToken('jwt-suspended')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResp(403, { code: 403, error_code: 'tenant_suspended' }))))
    expect(await verifySession()).toEqual({ state: 'forbidden' })
    expect(getToken()).toBe('jwt-suspended')
  })

  it('HTTP 200 但信封非 0 → revoked（服务端确实否了这次会话）', async () => {
    setToken('jwt-weird-envelope')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResp(200, { code: 500, message: 'boom' }))))
    expect(await verifySession()).toEqual({ state: 'revoked' })
    expect(getToken()).toBe('')
  })

  it('成功态以服务端角色为准并纠偏 localStorage（localStorage 造假role 不再有效）', async () => {
    setToken('jwt-forged-role')
    localStorage.setItem('role', 'super_admin') // 篡改值
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResp(200, okBody))))
    const r = await verifySession()
    expect(r.state).toBe('ok')
    expect(localStorage.getItem('role')).toBe('admin')
  })
})

// FIX-6 第二条验收：登出的键白名单必须"两键给两个答案"。
// 只断代管键为空是不够的——一句 localStorage.clear() 也能过；
// 同段断 visitor_key 仍在，才同时守住 P2-85（访客身份不许被登出顺带清掉）不回归。
describe('logoutAndRedirect 键白名单（FIX-6 + P2-85）', () => {
  it('清登录态键（token/记住的用户名/代管租户），保访客身份键', () => {
    // jsdom 的 location.href 只读，整体换成可写对象再断跳转
    const href = { href: '' } as Location
    Object.defineProperty(window, 'location', { value: href, configurable: true })
    setToken('jwt-active')
    localStorage.setItem('remember_username', 'admin')
    setImpersonateTenant('42') // 超管此刻代管着的租户
    localStorage.setItem(VISITOR_KEY, 'vk-c-visitor')

    logoutAndRedirect()

    expect(getToken()).toBe('')
    expect(localStorage.getItem('remember_username')).toBeNull()
    expect(getImpersonateTenant()).toBe('')
    expect(localStorage.getItem(IMPERSONATE_TENANT_KEY)).toBeNull()
    // 反向半边：访客身份必须活着
    expect(localStorage.getItem(VISITOR_KEY)).toBe('vk-c-visitor')
    expect(href.href).toBe('/login')
  })
})
