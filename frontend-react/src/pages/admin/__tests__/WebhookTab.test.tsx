// Admin Webhook UI 冒烟测试：列表、事件解析、测试/记录按钮。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { WebhookTab } from '../WebhookTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  authMock.mockReset()
})

describe('WebhookTab', () => {
  it('渲染 Webhook 订阅列表与操作', async () => {
    authMock.mockImplementation(async (url: string, opts?: { method?: string }) => {
      if (url.includes('/deliveries')) return { code: 0, data: { list: [{ id: 101, event: 'payment.paid', status: 'delivered', attempts: 1, payload: '{"order_no":"o1"}', last_error: '', created_at: '2026-09-12T10:00:00Z', updated_at: '2026-09-12T10:00:01Z', delivered_at: '2026-09-12T10:00:01Z' }] } }
      if (opts?.method === 'POST') return { code: 0, data: { ok: true, status: 200 } }
      return { code: 0, data: { list: [{ id: 7, name: '支付回调', url: 'https://example.com/hooks/scrm', events: 'payment.paid,lead.captured', active: true, fail_count: 0, disabled_at: null, secret_mask: 'whsec_****abcd', created_at: '2026-09-12T09:00:00Z' }] } }
    })
    render(<WebhookTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/webhooks'))
    expect(await screen.findByText('支付回调')).toBeTruthy()
    expect(screen.getByText('payment.paid')).toBeTruthy()
    expect(screen.getByText('lead.captured')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '记录' }))
    expect(await screen.findByText(/投递记录/)).toBeTruthy()
    expect(screen.getByText('{"order_no":"o1"}')).toBeTruthy()
  })
})
