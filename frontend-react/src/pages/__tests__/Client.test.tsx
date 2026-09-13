// F13/T6 C 端聊天页 PIPL 删除权入口冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Client from '../Client'

vi.mock('../../lib/branding', () => ({ useBrand: () => ({ brandName: '测试品牌', primaryColor: '#4f46e5' }) }))
vi.mock('../../lib/realtime', () => ({ useClientWS: vi.fn() }))
vi.mock('../../lib/chat', async () => {
  const actual = await vi.importActual('../../lib/chat')
  return { ...actual, collectFreshMessages: () => [] }
})

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('scrm_customer_id', '7')
  localStorage.setItem('scrm_visitor_key', 'vk_test')
  vi.stubGlobal('confirm', () => true)
})

describe('Client 个人信息删除', () => {
  it('访客可提交 customer 删除申请', async () => {
    const fetchMock = vi.fn(async (url: string, init?: { method?: string }) => {
      if (url.includes('/turnstile/sitekey')) return { json: async () => ({ code: 0, data: { enabled: false } }) }
      if (url.includes('/privacy/deletion-request') && init?.method === 'POST') return { json: async () => ({ code: 0, data: { deadline: '2026-09-28T00:00:00Z' } }) }
      if (url.includes('/chat/guest') && init?.method === 'POST') return { json: async () => ({ code: 0, data: { customer_id: 7, visitor_key: 'vk_test' } }) }
      if (url.includes('/chat/history')) return { json: async () => ({ code: 0, data: [] }) }
      if (url.includes('/chat/welcome') && init?.method === 'POST') return { json: async () => ({ code: 0, data: {} }) }
      return { json: async () => ({ code: 0, data: null }) }
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Client />)
    fireEvent.click(await screen.findByRole('button', { name: '申请删除我的资料' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/privacy/deletion-request',
      expect.objectContaining({ method: 'POST' }),
    ))
    expect(await screen.findByText(/已受理删除申请/)).toBeTruthy()
  })
})
