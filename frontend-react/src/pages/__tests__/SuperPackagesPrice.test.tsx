// 超管「AI 商业包」售价单位回归（FIX-11，2026-09-29 端到端审计批）。
// 背景：建包表单从前直接收"分"（placeholder 售价分），绕开 lib/money.ts
// 「界面是元、提交体是分」的单点，且与同页列表的元展示口径打架；旧写法
// parseInt(x)||0 还会把 '99.9' 截成 99、把乱码静默归 0——价格错但创建成功。
// 本文件钉三件事：①0.29 元提交体恰为 29 分（浮点漂移 28.999… 不允许出现）；
// ②三位小数等非法形态拦在卡内（不发请求，toast 点名）；③空值仍按 0 分
// （免费/试用包口径与改造前一致，防"收紧校验顺手砸了旧用法"）。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import SuperAdmin from '../SuperAdmin'
import { MessagePlugin } from 'tdesign-react'

// tdesign Table 依赖的浏览器 API 在 jsdom 缺失——与 SuperAdminPlan.test.tsx 同款最小桩
beforeAll(() => {
  const g = globalThis as unknown as Record<string, unknown>
  if (!g.ResizeObserver) {
    g.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} }
  }
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
})

// 只桩登录态，保留真实 AUTH 链路（fetch 已被用例 stub）
vi.mock('../../lib/api', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../lib/api')
  return { ...actual, getToken: () => 'test-token' }
})
vi.mock('../../lib/branding', () => ({
  useBrand: () => ({ brandName: 'AI-SCRM', logoUrl: '', primaryColor: '', secondaryColor: '' }),
}))

// 商业包列表返回空数组即可：本测试关心的是**新建提交体**，不是列表渲染
describe('SuperAdmin AI 商业包新建〔售价元→分单点换算〕', () => {
  let calls: { url: string; init?: RequestInit }[]
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('role', 'super_admin')
    calls = []
    // MessagePlugin 真实现要挂 DOM 容器，jsdom 里全部桩成 no-op——
    // warning 那条留给用例断言（拦截文案是真判据，不许只看不发请求）
    for (const k of ['success', 'error', 'info', 'warning'] as const) {
      vi.spyOn(MessagePlugin, k).mockImplementation(() => ({ close: () => {} }) as any)
    }
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      let body: unknown = { code: 0, data: { list: [], total: 0 } }
      if (url.includes('/super/packages') && init?.method === 'POST') body = { code: 0, data: { id: 99 } }
      else if (url.includes('/super/packages')) body = { code: 0, data: [] }
      else if (url.includes('/super/tenants')) body = { code: 0, data: { list: [], total: 0 } }
      return { json: async () => body, ok: true } as Response
    }))
  })

  // 打开「AI 商业包」Tab 并等表单挂载（lazy chunk，findBy 而非 get）
  async function openPkgForm() {
    render(<SuperAdmin />)
    fireEvent.click(await screen.findByText('AI 商业包', undefined, { timeout: 6000 }))
    const price = await screen.findByPlaceholderText('售价元', undefined, { timeout: 6000 })
    // 单位改元的字面锁：页面上不许再出现"售价分"这种要人肉算分的入口
    expect(screen.queryByPlaceholderText('售价分')).toBeNull()
    const code = screen.getByPlaceholderText('标识(如 pro_8000)') as HTMLInputElement
    const name = screen.getByPlaceholderText('名称') as HTMLInputElement
    code.value = 'pro_99'
    name.value = '专业版'
    return price as HTMLInputElement
  }

  it('0.29 元 → 提交体 price_cents 恰为 29（无 28.999… 浮点漂移）', async () => {
    const price = await openPkgForm()
    price.value = '0.29'
    fireEvent.click(screen.getByText('新增'))
    const post = await waitFor(() => {
      const found = calls.find((c) => c.init?.method === 'POST' && c.url.includes('/super/packages'))
      if (!found) throw new Error('还没看到建包 POST 请求')
      return found
    })
    const body = JSON.parse(String(post.init?.body))
    expect(body.price_cents).toBe(29)
    expect(body.code).toBe('pro_99')
  })

  it('三位小数 9.999 拦在卡内：不发请求且 toast 点名单位口径', async () => {
    const price = await openPkgForm()
    price.value = '9.999'
    fireEvent.click(screen.getByText('新增'))
    await waitFor(() => {
      expect((MessagePlugin.warning as unknown as ReturnType<typeof vi.fn>).mock.calls.flat().join('|')).toContain('售价')
    })
    // 反向半边：拦截必须真的没把请求发出去（只 toast 照提交＝拦了个寂寞）
    expect(calls.some((c) => c.init?.method === 'POST' && c.url.includes('/super/packages'))).toBe(false)
  })

  it('空售价仍按 0 分提交（免费/试用包旧口径不破）', async () => {
    const price = await openPkgForm()
    price.value = ''
    fireEvent.click(screen.getByText('新增'))
    const post = await waitFor(() => {
      const found = calls.find((c) => c.init?.method === 'POST' && c.url.includes('/super/packages'))
      if (!found) throw new Error('还没看到建包 POST 请求')
      return found
    })
    expect(JSON.parse(String(post.init?.body)).price_cents).toBe(0)
  })
})
