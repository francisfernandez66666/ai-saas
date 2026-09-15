// 前端页面冒烟回归（2026-09-15 价值增强批）
// 定位：八套 E2E 全是 API 断言，`vite build` 通过 ≠ 页面能渲染（FIXLOG_2026-08-24 的
// advisor JS 语法 bug 即前端盲区实证）。本用例把 10 个核心页面挂进 jsdom 真渲染：
// 壳层文案断言 + 控制台未捕获异常即失败，守住"页面打不开"这条产品底线。
// 网络层统一 mock fetch（默认返回空列表信封），不发真实请求。
// 注：显式 any 受 T7 契约"只降不升"基线管控，本文件零 any——垫片走 unknown/defineProperty。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import Advisor from '../Advisor'
import Admin from '../Admin'
import Billing from '../Billing'
import Client from '../Client'
import Index from '../Index'
import Login from '../Login'
import Org from '../Org'
import Pricing from '../Pricing'
import Register from '../Register'
import SuperAdmin from '../SuperAdmin'
import { TOKEN_KEY } from '../../lib/api'

// jsdom 缺失的浏览器 API 垫片（TDesign 表格/WS 客户端会触达），存在则不覆盖
function shimGlobal(name: string, value: unknown) {
  const g = globalThis as unknown as Record<string, unknown>
  if (!(name in g)) g[name] = value
}
shimGlobal('ResizeObserver', class {
  observe() {}
  unobserve() {}
  disconnect() {}
})
if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (q: string) => ({
      matches: false, media: q, onchange: null,
      addListener: () => {}, removeListener: () => {},
      addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
    }),
  })
}
shimGlobal('WebSocket', class {
  url: string
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(url: string) { this.url = url }
  close() {}
  send() {}
})

// location 桩（登录漏斗 redirectByRole 写 location.href，jsdom 导航不可用）
function stubLocation() {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { href: '/', pathname: '/login', search: '', assign() {}, replace() {}, reload() {} },
  })
}

// fetch 桩：按 URL 命中专用信封，其余统一 {code:0,data:[]}（多数列表接口兼容）
const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ code: 0, data }) })
let fetchLog: string[] = []

function installFetch(role = 'tenant_admin') {
  fetchLog = []
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : 'url' in input ? String(input.url) : String(input)
    fetchLog.push(url)
    if (url.includes('/auth/me')) return ok({ id: 1, username: 'admin', role, tenant_id: 1 })
    if (url.includes('/auth/login')) return ok({ token: 'jwt-smoke', user: { id: 1, username: 'admin', role, must_change_password: false } })
    if (url.includes('/auth/register-config')) return ok({ industries: [], need_email: false })
    if (url.includes('/packages')) return ok([])
    if (url.includes('/plans')) return ok({ plans: [], packages: [] })
    if (url.includes('/admin/config')) return ok([])
    if (url.includes('/billing/my-package')) return ok({})
    if (url.includes('/org/departments/tree')) return ok([])
    return ok([])
  })
  globalThis.fetch = fn as unknown as typeof fetch
}

beforeEach(() => {
  stubLocation()
  localStorage.clear()
})
afterEach(() => {
  vi.restoreAllMocks()
})

// 各页挂载即触达的接口是异步的：冒烟断言"壳层渲染成功"，用 waitFor 兜渲染异常
async function smoke(label: string, ui: React.ReactElement, finder?: () => unknown) {
  const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
  render(ui)
  await waitFor(() => {
    if (finder) expect(finder()).toBeTruthy()
    else expect(screen.getAllByText(label, { exact: false }).length).toBeGreaterThan(0)
  })
  // 未捕获渲染异常会经 console.error（React 18 错误日志）——冒烟即失败
  const crashes = errSpy.mock.calls.filter((a) => /Cannot read|is not a function|Minified React error|useLayoutEffect/.test(String(a[0])))
  expect(crashes, `页面 ${label} 渲染异常`).toHaveLength(0)
  errSpy.mockRestore()
}

const R = (el: React.ReactNode) => <MemoryRouter>{el}</MemoryRouter>

describe('核心页面冒烟（无后端可渲染）', () => {
  it('落地页 Index', async () => await smoke('登录工作台', R(<Index />)))
  it('登录页 Login', async () => {
    installFetch()
    await smoke('用户名', R(<Login />))
  })
  it('注册页 Register', async () => {
    installFetch()
    await smoke('免费开通试用', R(<Register />))
  })
  it('定价页 Pricing', async () => {
    installFetch()
    await smoke('选择适合您的套餐', R(<Pricing />))
  })
  it('客户对话 Client', async () => {
    installFetch()
    // 客户消息框是 placeholder 文本，用 getByPlaceholderText 作壳层断言
    await smoke('输入你的问题', R(<Client />), () => screen.getByPlaceholderText(/输入你的问题/))
  })
  it('顾问工作台 Advisor', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    await smoke('顾问工作台', R(<Advisor />))
  })
  it('管理中心 Admin', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    localStorage.setItem('role', 'tenant_admin')
    await smoke('管理中心', R(<Admin />))
  })
  it('平台超管 SuperAdmin', async () => {
    installFetch('super_admin')
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    localStorage.setItem('role', 'super_admin')
    await smoke('租户管理', R(<SuperAdmin />))
  })
  it('收银台 Billing', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    await smoke('订阅与收银台', R(<Billing />))
  })
  it('组织架构 Org', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    await smoke('部门', R(<Org />))
  })
})

describe('登录漏斗（mock 后端）', () => {
  it('提交表单 → 调用 login 接口并落 token', async () => {
    installFetch()
    render(R(<Login />))
    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'admin' } })
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'admin123' } })
    fireEvent.click(screen.getByRole('button', { name: /登录|登 录/ }))
    await waitFor(() => expect(localStorage.getItem(TOKEN_KEY)).toBe('jwt-smoke'))
    expect(fetchLog.some((u) => u.includes('/auth/login'))).toBe(true)
    expect(window.location.href).toBe('/admin') // tenant_admin 角色分流
  })
})
