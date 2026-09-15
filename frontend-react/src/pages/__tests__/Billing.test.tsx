// E1 收银台 static_qr 收款码渲染回归测试（§W：static_qr 为主收款通道）。
// 旧版 Billing.tsx 待支付弹窗只有一行占位文案，客户拿不到码只能"盲付"——
// 断言后端下发 qr_content 时前端确实渲染 <img alt="平台收款码">。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Billing from '../Billing'

vi.mock('../../lib/api', () => ({
  AUTH: () => ({ headers: { Authorization: 'Bearer t' } }),
  apiFetch: (url: string, init?: RequestInit) => globalThis.fetch(url, init),
  getToken: () => 'test-token',
}))
vi.mock('../../lib/branding', () => ({
  useBrand: () => ({ brandName: 'AI-SCRM', logoUrl: '', primaryColor: '', secondaryColor: '' }),
}))

const DATA_URI = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB'
const pendingOrder = {
  id: 55, order_no: 'BOE1TEST0001', amount_cents: 9900, package_name: '加油包', channel: 'manual',
  status: 'pending', manual_confirm: false, created_at: '2026-09-14T10:00:00Z', qr_content: DATA_URI,
}

/** 按 URL 分派 fetch：my-package 带 pay_mode=static_qr；orders 返回待支付单。 */
function routeFetch() {
  return vi.fn(async (url: string) => {
    const body = url.includes('/billing/my-package')
      ? { code: 0, data: { tenant_name: 'ACME', status: 'trial', used_ai_calls: 0, max_ai_calls: 100, ai_call_balance: 0, pay_mode: 'static_qr' } }
      : url.includes('/api/v1/packages')
        ? { code: 0, data: [] }
        : url.includes('/billing/orders')
          ? { code: 0, data: [pendingOrder] }
          : { code: 0, data: {} }
    return { json: async () => body, ok: true } as Response
  })
}

describe('Billing 收银台 E1 static_qr 渲染', () => {
  beforeEach(() => { localStorage.clear(); localStorage.setItem('role', 'admin') })

  it('继续支付 static_qr 待支付单时渲染后端下发的收款码图片', async () => {
    vi.stubGlobal('fetch', routeFetch())
    render(<Billing />)
    // 等订单列表出来，命中"继续支付"按钮
    const payBtn = await screen.findByLabelText('继续支付订单BOE1TEST0001', undefined, { timeout: 4000 })
    fireEvent.click(payBtn)
    const img = await screen.findByAltText('平台收款码', undefined, { timeout: 4000 })
    expect(img.getAttribute('src')).toBe(DATA_URI)
    // 收款模式提示 + 我已付费入口（static_qr 非 sdk 走人工确认）
    expect(screen.getByText(/扫码转账/)).toBeTruthy()
    expect(screen.getByText('我已付费')).toBeTruthy()
  })
})
