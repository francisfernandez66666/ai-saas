// F1 回归测试（UATFOLLOWUP 2026-09-15）：订单列表退款字段展示。
// 修复前：后端列表接口 Select 白名单漏 refund_amount_cents/refunded_at（恒 0/null），
// 且前端状态列直接渲染英文原值 "refunded"——已退款订单看不出退了多少钱。
// 修复后：状态列显示「已退款 ¥xx.xx」，字段来自后端 /billing/orders 列表响应。
import { render, screen } from '@testing-library/react'
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

const refundedOrder = {
  id: 77, order_no: 'BOF1REFUND0001', amount_cents: 19900, refund_amount_cents: 9900,
  refunded_at: '2026-09-15T10:13:08+08:00', package_name: '加油包', channel: 'mock',
  status: 'refunded', manual_confirm: false, created_at: '2026-09-14T10:00:00Z',
}

/** 按 URL 分派 fetch：my-package 普通态；orders 返回一笔已退款单。 */
function routeFetch() {
  return vi.fn(async (url: string) => {
    const body = url.includes('/billing/my-package')
      ? { code: 0, data: { tenant_name: 'ACME', status: 'trial', used_ai_calls: 0, max_ai_calls: 100, ai_call_balance: 0, pay_mode: 'mock' } }
      : url.includes('/api/v1/packages')
        ? { code: 0, data: [] }
        : url.includes('/billing/orders')
          ? { code: 0, data: [refundedOrder] }
          : { code: 0, data: {} }
    return { json: async () => body, ok: true } as Response
  })
}

describe('Billing 订单列表 F1 退款字段展示', () => {
  beforeEach(() => { localStorage.clear(); localStorage.setItem('role', 'admin') })

  it('已退款订单显示中文态与实际退款金额', async () => {
    vi.stubGlobal('fetch', routeFetch())
    render(<Billing />)
    const badge = await screen.findByText('已退款 ¥99.00', undefined, { timeout: 4000 })
    expect(badge).toBeTruthy()
    // 不应再出现英文原值裸奔
    expect(screen.queryByText('refunded')).toBeNull()
  })
})
