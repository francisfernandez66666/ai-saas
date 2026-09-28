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
    // 命名夹具：断言与 mock 共用一份；事件码由 events 字段按组件 eventList() 口径切出（WebhookTab.tsx:34）
    const SUB = { id: 7, name: '支付回调', url: 'https://example.com/hooks/scrm', events: 'payment.paid,lead.captured', active: true, fail_count: 0, disabled_at: null, secret_mask: 'whsec_****abcd', created_at: '2026-09-12T09:00:00Z' }
    const DELIVERY = { id: 101, event: 'payment.paid', status: 'delivered', attempts: 1, payload: '{"order_no":"o1"}', last_error: '', created_at: '2026-09-12T10:00:00Z', updated_at: '2026-09-12T10:00:01Z', delivered_at: '2026-09-12T10:00:01Z' }
    const eventCodes = SUB.events.split(',').map((x) => x.trim()).filter(Boolean)
    authMock.mockImplementation(async (url: string, opts?: { method?: string }) => {
      if (url.includes('/deliveries')) return { code: 0, data: { list: [DELIVERY] } }
      if (opts?.method === 'POST') return { code: 0, data: { ok: true, status: 200 } }
      return { code: 0, data: { list: [SUB] } }
    })
    render(<WebhookTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/webhooks'))
    // 原来弱在哪：toBeTruthy 只证明查到；现在钉订阅名/事件 Tag 全部来自夹具字段
    expect(await screen.findByText(SUB.name)).toBeInTheDocument()
    expect(screen.getByText(eventCodes[0])).toBeInTheDocument()
    expect(screen.getByText(eventCodes[1])).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: '记录' }))
    // 原来弱在哪：/投递记录/ 半句正则；现在钉抽屉整标题「投递记录：<订阅名>」（WebhookTab.tsx:221）
    expect(await screen.findByText(`投递记录：${SUB.name}`)).toBeInTheDocument()
    // 原来弱在哪：length>0；抽屉打开后事件码 Tag 总数钉死为「订阅 2 枚 + 投递记录 1 枚」
    expect(screen.getAllByText(/payment\.paid|lead\.captured/).length).toBe(eventCodes.length + 1)
    // 原来弱在哪：toBeTruthy；现在钉 payload 单元格回显投递行的原文
    expect(screen.getByText(DELIVERY.payload)).toBeInTheDocument()
  })
})
