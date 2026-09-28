// E1 收银台 static_qr 收款码渲染回归测试（§W：static_qr 为主收款通道）。
// 旧版 Billing.tsx 待支付弹窗只有一行占位文案，客户拿不到码只能"盲付"——
// 断言后端下发 qr_content 时前端确实渲染 <img alt="平台收款码">。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Billing from '../Billing'
// 期望串不抄字面量：徽标文案由 lib/money.fenToYuan 单点换算（复制 '99.00' 即双维护）
import { fenToYuan } from '../../lib/money'


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
    // 原来弱在哪：/扫码转账/ 会被任何含这四个字的节点喂绿；现在钉 Billing.tsx
    // static_qr 分支的完整文案「收款方式：扫码转账+平台人工确认」。
    expect(screen.getByText(/扫码转账/)).toHaveTextContent('收款方式：扫码转账+平台人工确认')
    expect(screen.getByText('我已付费')).toHaveTextContent('我已付费')
  })
})

// 续费触达 banner 回归（商业缺口批 2026-09-16）：到期/临期/待支付三态此前租户侧零感知。
describe('Billing 续费触达 banner', () => {
  beforeEach(() => { localStorage.clear(); localStorage.setItem('role', 'admin') })

  function bannerFetch(quota: Record<string, unknown>, orders: unknown[] = []) {
    return vi.fn(async (url: string) => {
      const body = url.includes('/billing/my-package')
        ? { code: 0, data: { tenant_name: 'ACME', used_ai_calls: 0, max_ai_calls: 100, ai_call_balance: 0, pay_mode: 'mock', ...quota } }
        : url.includes('/api/v1/packages')
          ? { code: 0, data: [] }
          : url.includes('/billing/orders')
            ? { code: 0, data: orders }
            : { code: 0, data: {} }
      return { json: async () => body, ok: true } as Response
    })
  }

  it('status=expired 展示到期停服红色横幅', async () => {
    vi.stubGlobal('fetch', bannerFetch({ status: 'expired' }))
    render(<Billing />)
    // 原来弱在哪：正则命中「已到期…恢复」子串即绿，横幅整句被改写也看不见。
    // 现在钉 Billing.tsx expired 分支的完整文案。
    expect(await screen.findByText(/已到期.*续费到账后立即恢复/s, undefined, { timeout: 4000 }))
      .toHaveTextContent('企业空间已到期，AI 会话与新登录已暂停，数据保留——续费到账后立即恢复。')
  })

  it('剩余不足 7 天展示临期横幅', async () => {
    const soon = new Date(Date.now() + 3 * 86400000).toISOString()
    vi.stubGlobal('fetch', bannerFetch({ status: 'active', expired_at: soon }))
    render(<Billing />)
    // 原来弱在哪：/3 天后到期/ 不区分试用/套餐两分支文案。夹具 status='active' →
    // 走 Billing.tsx「套餐将于 N 天后到期」分支，N=ceil(3天窗口)=3，整句钉死。
    // 不断精确到期时刻（nowMs 取数与夹具生成间有毫秒级漂移），但档位与文案是确定的。
    expect(await screen.findByText(/3 天后到期/, undefined, { timeout: 4000 }))
      .toHaveTextContent('套餐将于 3 天后到期，请及时续费以免影响使用。')
  })

  it('无到期风险但有待支付订单时提示继续支付', async () => {
    const far = new Date(Date.now() + 300 * 86400000).toISOString()
    vi.stubGlobal('fetch', bannerFetch({ status: 'active', expired_at: far }, [pendingOrder]))
    render(<Billing />)
    // 原来弱在哪：/1 笔订单待支付/ 只数了个"1"，后半句丢了也绿。
    // pending 计数=orders 里 status==='pending' 的行数=1（夹具恰一条），整句钉死。
    expect(await screen.findByText(/1 笔订单待支付/, undefined, { timeout: 4000 }))
      .toHaveTextContent('您有 1 笔订单待支付，在下方订单区点击"继续支付"完成。')
  })
})

// P2-11/P2-12 回归(2026-09-20 批三)：
// ① 发票弹窗邮箱预填必须来自 /auth/me（旧版读 localStorage['email'] 全仓无写入点，恒空假预填）；
// ② 已退款终态单不得再出"退款/发票"动作按钮（再点必 409/误申请），paid 正常单动作保留。
describe('Billing 发票预填与退款终态按钮门（P2-11/P2-12）', () => {
  const paidOrder = {
    id: 61, order_no: 'BOP1TEST', amount_cents: 9900, package_name: '商业包', channel: 'mock',
    status: 'paid', created_at: '2026-09-19T10:00:00Z',
  }
  const refundedOrder = {
    id: 62, order_no: 'BOR1TEST', amount_cents: 9900, package_name: '商业包', channel: 'mock',
    status: 'refunded', refund_amount_cents: 9900, created_at: '2026-09-19T11:00:00Z',
  }
  function routeFetch2(orders: unknown[]) {
    return vi.fn(async (url: string) => {
      const body = url.includes('/billing/my-package')
        ? { code: 0, data: { tenant_name: 'ACME', status: 'paid', used_ai_calls: 1, max_ai_calls: 100, ai_call_balance: 99, pay_mode: 'mock' } }
        : url.includes('/api/v1/packages')
          ? { code: 0, data: [] }
          : url.includes('/billing/orders')
            ? { code: 0, data: orders }
            : url.includes('/auth/me')
              ? { code: 0, data: { username: 'admin', role: 'admin', email: 'finance@acme.test' } }
              : { code: 0, data: {} }
      return { json: async () => body, ok: true } as Response
    })
  }

  it('发票弹窗接收邮箱预填 /auth/me 绑定邮箱', async () => {
    localStorage.clear(); localStorage.setItem('role', 'admin')
    vi.stubGlobal('fetch', routeFetch2([paidOrder]))
    render(<Billing />)
    const btn = await screen.findByLabelText('申请发票订单BOP1TEST', undefined, { timeout: 4000 })
    fireEvent.click(btn)
    const emailInp = (await screen.findByPlaceholderText('接收邮箱', undefined, { timeout: 4000 })) as HTMLInputElement
    await waitFor(() => expect(emailInp.value).toBe('finance@acme.test'))
  })

  it('已退款单无退款/发票按钮，paid 单动作保留', async () => {
    localStorage.clear(); localStorage.setItem('role', 'admin')
    vi.stubGlobal('fetch', routeFetch2([paidOrder, refundedOrder]))
    render(<Billing />)
    // 原来弱在哪：findByText(/已退款/) 只证明这三个字在页面上；按钮 truthy 只证明
    // 有个节点。现在钉：终态行状态徽标整句「已退款 ¥99」（Billing.tsx 按
    // `'已退款 ¥' + fenToYuan(o.refund_amount_cents)` 拼接，夹具退了 9900 分），
    // 且按钮断到 tagName/文案/可用态（BUTTON + "退款"/"发票"），不是"任何带该 label 的节点"。
    const refundChip = await screen.findByText(/已退款/, undefined, { timeout: 4000 })
    expect(refundChip).toHaveTextContent(`已退款 ¥${fenToYuan(refundedOrder.refund_amount_cents)}`)
    expect(screen.queryByLabelText('申请退款订单BOR1TEST')).toBeNull()
    expect(screen.queryByLabelText('申请发票订单BOR1TEST')).toBeNull()
    // 对照：paid 行两键仍在（门不能一刀切）
    const refundBtn = screen.getByLabelText('申请退款订单BOP1TEST')
    expect(refundBtn.tagName).toBe('BUTTON')
    expect(refundBtn).toHaveTextContent('退款')
    const invoiceBtn = screen.getByLabelText('申请发票订单BOP1TEST')
    expect(invoiceBtn.tagName).toBe('BUTTON')
    expect(invoiceBtn).toHaveTextContent('发票')
  })
})
