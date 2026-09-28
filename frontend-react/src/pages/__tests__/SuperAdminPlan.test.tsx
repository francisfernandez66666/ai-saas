// SuperAdmin 换套餐弹窗回归（商业缺口批 2026-09-16）。
// 背景：超管改租户套餐此前只能人肉 UPDATE 库；新增 PUT /super/tenants/:id/plan + 弹窗。
// 断言：租户行出现"换套餐"按钮 → 弹窗内套餐下拉（含席位/客户/月价）→ 确认后发出 PUT 请求。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import SuperAdmin from '../SuperAdmin'

// tdesign Table 依赖的浏览器 API 在 jsdom 缺失——与 smoke.test.tsx 同款最小桩
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

// 只桩登录态，保留真实 AUTH/apiJSON 链路（其内部走全局 fetch，已被用例 stub）
vi.mock('../../lib/api', async () => {
  const actual = await vi.importActual<Record<string, unknown>>('../../lib/api')
  return { ...actual, getToken: () => 'test-token' }
})
vi.mock('../../lib/branding', () => ({
  useBrand: () => ({ brandName: 'AI-SCRM', logoUrl: '', primaryColor: '', secondaryColor: '' }),
}))

const tenantRow = {
  id: 77, name: '演练科技', code: 'e2e-plan', tier: 'personal', status: 'active',
  plan_name: '个人版', plan_id: 1, used_customers: 3, max_customers: 200,
  max_users: 1, used_users: 1, created_at: '2026-09-01',
}
const planList = [
  { id: 1, name: '个人版', tier: 'personal', max_users: 1, max_customers: 200, price_monthly_cents: 0, is_active: true },
  { id: 2, name: '企业标准版', tier: 'enterprise', max_users: 5, max_customers: 500, price_monthly_cents: 3900, is_active: true },
]

describe('SuperAdmin 换套餐', () => {
  let calls: { url: string; init?: RequestInit }[]
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('role', 'super_admin')
    calls = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init })
      let body: unknown = { code: 0, data: { list: [], total: 0 } }
      if (url.includes('/tenants/77/plan')) body = { code: 0, data: { plan_id: 2, plan_name: '企业标准版', max_users: 5 } }
      else if (url.includes('/super/tenants')) body = { code: 0, data: { list: [tenantRow], total: 1 } }
      else if (url.includes('/super/plans')) body = { code: 0, data: { items: planList } }
      else if (url.includes('/super/packages')) body = { code: 0, data: [] }
      return { json: async () => body, ok: true } as Response
    }))
  })

  it('租户行有换套餐按钮，弹窗可下拉选择并发出 PUT', async () => {
    render(<SuperAdmin />)
    const btn = await screen.findByText('换套餐', undefined, { timeout: 6000 })
    // 原来弱在哪：getByText('席位') 配 truthy 只证明表头有"席位"俩字，列有没有真的
    // 渲染用量单元格不管。现在钉 TenantsTab.tsx 列定义的 cell 公式：
    // 席位 = `${used_users}/${max_users}` = '1/1'、客户用量 = `${used_customers}/${max_customers}` = '3/200'（夹具推得）。
    expect(screen.getByText('席位')).toHaveTextContent('席位')
    expect(screen.getByText('1/1')).toHaveTextContent('1/1')
    expect(screen.getByText('3/200')).toHaveTextContent('3/200')
    fireEvent.click(btn)
    const opt = await screen.findByText(/企业标准版 · 席位5/, undefined, { timeout: 6000 })
    const sel = opt.closest('select') as HTMLSelectElement
    sel.value = '2'
    fireEvent.click(screen.getByText('确认变更'))
    const put = await waitFor(() => {
      const found = calls.find((c) => c.init?.method === 'PUT' && c.url.includes('/super/tenants/77/plan'))
      if (!found) throw new Error('还没看到换套餐 PUT 请求')
      return found
    })
    // 原来弱在哪：waitFor 里 expect(put) 配 truthy + body 里"含一段字符串"——
    // method 写错、body 序列化成 {"plan_id":"2"}（字符串）都能绿。
    // 现在钉：method 精确等于 PUT、body 反序列化后 plan_id 是**数字** 2（夹具里选中的企业标准版 id）。
    expect(put.init?.method).toBe('PUT')
    expect(JSON.parse(String(put.init?.body))).toMatchObject({ plan_id: 2 })
  })
})
