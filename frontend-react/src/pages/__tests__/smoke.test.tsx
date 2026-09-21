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
// P1-11 修复(2026-09-20 审计批二)：原"任意 URL 都回 {code:0,data:[]}"让 10 页冒烟只测"不崩"，
// 列表/树等结构化端点拿到错误形态也假绿——关键数据源改回真实响应形态（分页信封/树/裸数组），
// 配套用例对关键列（客户名/租户名/部门名）做内容断言，并用文末护栏自测用例封堵"桩兜底假绿"。
const ok = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ code: 0, data }) })
let fetchLog: string[] = []

// 关键列断言用的固定样本（形态对齐后端真实信封：/super/tenants 分页、/org 树、/advisor/customers 分页）
export const SMOKE_TENANT_NAME = '极石汽车体验店'
// SMOKE_DEPT_NAME 渲染冒烟用例中用于断言的部门名。
export const SMOKE_DEPT_NAME = '销售一部'
// SMOKE_CUSTOMER_NAME 渲染冒烟用例中用于断言的客户名。
export const SMOKE_CUSTOMER_NAME = '王小明'

function installFetch(role = 'tenant_admin') {
  fetchLog = []
  const fn = vi.fn(async (input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : 'url' in input ? String(input.url) : String(input)
    fetchLog.push(url)
    if (url.includes('/auth/me')) return ok({ id: 1, username: 'admin', role, tenant_id: 1 })
    if (url.includes('/auth/login')) return ok({ token: 'jwt-smoke', user: { id: 1, username: 'admin', role, must_change_password: false } })
    if (url.includes('/auth/register-config')) return ok({ industries: [], need_email: false })
    if (url.includes('/super/tenants')) return ok({ list: [{ id: 2, name: SMOKE_TENANT_NAME, code: 'rox', plan_name: '商业版', used_customers: 3, max_customers: 10, status: 'active', created_at: '2026-01-01T00:00:00Z' }], total: 1, page: 1, page_size: 100 })
    if (url.includes('/org/departments/tree')) return ok([{ id: 1, name: SMOKE_DEPT_NAME, depth: 0, path: SMOKE_DEPT_NAME, user_count: 2, children: [{ id: 2, name: '华东组', depth: 1, path: SMOKE_DEPT_NAME + '/华东组', user_count: 1 }] }])
    if (url.includes('/org/users')) return ok([{ id: 3, username: 'sales1', real_name: SMOKE_CUSTOMER_NAME, role: 'sales', status: 1 }])
    if (url.includes('/advisor/customers')) return ok({ list: [{ id: 7, name: SMOKE_CUSTOMER_NAME, phone: '13800000000', journey_stage: 'lead_captured', updated_at: '2026-01-01T00:00:00Z' }], total: 1 })
    if (url.includes('/packages')) return ok([])
    if (url.includes('/plans')) return ok({ plans: [], packages: [] })
    if (url.includes('/admin/config')) return ok([])
    if (url.includes('/billing/my-package')) return ok({})
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
    // P1-11：除壳层文案外断言客户列表关键列真实渲染（数据来自 /advisor/customers 分页信封）
    await smoke('顾问工作台', R(<Advisor />), () => screen.getAllByText('顾问工作台', { exact: false }).length > 0 && screen.getByText(SMOKE_CUSTOMER_NAME))
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
    // P1-11：断言租户表关键列——/super/tenants 返回 {list,total} 信封，取数路径写错此列即空
    await smoke('租户管理', R(<SuperAdmin />), () => screen.getAllByText('租户管理').length > 0 && screen.getByText(SMOKE_TENANT_NAME))
  })
  it('收银台 Billing', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    await smoke('订阅与收银台', R(<Billing />))
  })
  it('组织架构 Org', async () => {
    installFetch()
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    // P1-11：断言部门树节点真实渲染（/org/departments/tree 返回裸数组，形态错则整树为空）
    await smoke('部门', R(<Org />), () => screen.getAllByText('部门树').length > 0 && screen.getByText(SMOKE_DEPT_NAME, { exact: false }))
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

  // P1-6 护栏(2026-09-20 审计批)：断网（fetch reject）→ loading 必复位且出错误 toast，
  // 修复前 doLogin 无 try/finally，await 抛错后 setLoading(false) 永不执行→按钮永久转圈
  it('网络异常提交 → loading 复位并提示，不卡死', async () => {
    globalThis.fetch = vi.fn(async () => {
      throw new TypeError('Failed to fetch')
    }) as unknown as typeof fetch
    vi.spyOn(console, 'error').mockImplementation(() => {})
    render(R(<Login />))
    fireEvent.change(screen.getByPlaceholderText('用户名'), { target: { value: 'admin' } })
    fireEvent.change(screen.getByPlaceholderText('密码'), { target: { value: 'admin123' } })
    fireEvent.click(screen.getByRole('button', { name: /登录|登 录/ }))
    // toast 经 MessagePlugin 挂 body：等错误文案出现
    await waitFor(() => {
      expect(document.body.textContent || '').toContain('网络异常')
    }, { timeout: 3000 })
    // 按钮恢复可点（loading 图标消失）：TDesign loading 按钮 aria-disabled 或类名变化
    await waitFor(() => {
      const btn = screen.getByRole('button', { name: /登录|登 录/ }) as HTMLButtonElement
      expect(btn.querySelector('.t-loading')).toBeNull()
    })
    expect(localStorage.getItem(TOKEN_KEY)).toBeNull() // 失败不落 token
  })
})

// P1-11 护栏自测(2026-09-20 审计批二)：故意注入"坏端点"——/super/tenants 返回 500 信封。
// 若冒烟的关键列断言真实绑定响应数据，租户名列必须不出现；哪一天通用桩/断言放宽放进了假绿，
// 这条会先红（对应验收"坏端点注入 spec 必须变红"的常态化护栏）。
describe('护栏自测（关键列断言数据耦合）', () => {
  it('超管租户接口坏（code:500）→ 租户名关键列不得渲染', async () => {
    installFetch('super_admin')
    const prev = globalThis.fetch
    globalThis.fetch = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : 'url' in input ? String(input.url) : String(input)
      if (url.includes('/super/tenants')) return { ok: true, status: 200, json: async () => ({ code: 500, message: 'boom' }) }
      return prev(input, init)
    }) as unknown as typeof fetch
    localStorage.setItem(TOKEN_KEY, 'jwt-smoke')
    localStorage.setItem('role', 'super_admin')
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    render(R(<SuperAdmin />))
    // 壳层（菜单）照常渲染，数据请求失败不崩；失败信封经 AUTH toast，不弹窗外异常
    await waitFor(() => expect(screen.getAllByText('租户管理').length).toBeGreaterThan(0))
    // 数据请求经包装桩返回 500 信封（此路径不入 fetchLog），给异步链留完成窗口
    await new Promise((r) => setTimeout(r, 150))
    expect(screen.queryByText(SMOKE_TENANT_NAME)).toBeNull()
    errSpy.mockRestore()
  })
})
