// F11/T6 通道接入 Tab 冒烟测试：通道列表、凭据掩码、死信重发入口。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ChannelsTab } from '../ChannelsTab'

// C3(PLAN_FIX_2026-09-21)：ChannelsTab 改走统一请求层 apiFetch，mock 需补该导出。
// 这里让 apiFetch 薄封装转发到下方 stubGlobal 的 fetch，既保持"断言 fetch 调用与参数"
// 的既有语义，又覆盖到真实调用路径（不再直连 fetch）。
vi.mock('../../../lib/api', () => ({
  getToken: () => 'test-token',
  authHeaders: () => ({ Authorization: 'Bearer test-token' }),
  apiFetch: (url: string, opts: RequestInit = {}) => fetch(url, opts),
}))

const channel = {
  id: 8,
  type: 'wecom_app',
  name: '企微自建应用',
  corpid: 'wp_test_corpid',
  appid: '',
  status: 'active',
  department_id: 10,
  secret_mask: 'sec_****abcd',
  token_mask: 'tok_****1234',
  aeskey_mask: 'aes_****5678',
  config_json: '{"agentid":"1000002"}',
  created_at: '2026-09-13T10:00:00Z',
}

const deadLetter = {
  id: 91,
  channel_id: 8,
  customer_id: 66,
  conversation_id: 12,
  content: '测试失败出站消息',
  status: 'dead',
  retries: 5,
  error: 'access_token invalid',
  created_at: '2026-09-13T10:05:00Z',
  next_retry_at: null,
}

/** 构造统一 JSON API 响应，供 vitest mock fetch。 */
function jsonResp(data: unknown) {
  return {
    ok: true,
    json: async () => ({ code: 0, message: 'ok', data }),
  }
}

describe('ChannelsTab', () => {
  beforeEach(() => {
    localStorage.setItem('scrm_auth_token', 'test-token')
    vi.spyOn(window, 'confirm').mockImplementation(() => true)
  })

  it('渲染通道列表、凭据掩码和死信重发入口', async () => {
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/admin/channel-dlq') && init?.method === 'POST') return jsonResp(null)
      if (url.includes('/admin/channel-dlq')) return jsonResp({ list: [deadLetter] })
      if (url.includes('/admin/channels')) return jsonResp({ list: [channel] })
      return jsonResp(null)
    })
    vi.stubGlobal('fetch', fetchMock)

    const { container } = render(<ChannelsTab />)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/channels', expect.any(Object)))
    expect(await screen.findByText('企微自建应用')).toBeTruthy()
    expect(container.textContent).toContain('S:sec_****abcd · T:tok_****1234 · A:aes_****5678')
    expect(screen.getByText('企业微信自建应用')).toBeTruthy()
    expect(screen.getByText('已启用')).toBeTruthy()
    expect(screen.getByText('测试失败出站消息')).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '重发' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/api/v1/admin/channel-dlq/91/retry',
      expect.objectContaining({ method: 'POST' }),
    ))
  })
})
