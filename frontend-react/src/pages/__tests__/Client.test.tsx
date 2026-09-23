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

// E8：删除/找人工改用 confirmDialog（非原生 confirm），mock 成"用户点了确认"
vi.mock('../../lib/confirm', () => ({
  ConfirmDialog: ({ open, onConfirm }: { open: boolean; onConfirm: () => void }) => (open ? <button onClick={onConfirm}>confirm-mock</button> : null),
  confirmDialog: () => Promise.resolve(true),
  uiAlert: () => Promise.resolve(),
}))

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('scrm_customer_id', '7')
  localStorage.setItem('scrm_visitor_key', 'vk_test')
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

// ============================================================
// 获客活码（批次4）：?code= 的三步链路——先问码活着没、再带码建档、最后记一次打开事件。
// 断言集中在"顺序"和"死码不入库"两件事上：
//   · 不带 resolve 结果就建档 = 印错的码也会给客户身上写一个来源；
//   · scan 放在建客之后 = 有 visitor_key 才谈得上窗口内去重，否则重复刷新把渠道数字刷虚。
// ============================================================
describe('Client 获客活码归因', () => {
  /** 造一个按 URL 分流的 fetch 桩，并记录调用顺序与请求体。 */
  function stub(opts: { resolveOk?: boolean; guest?: { customer_id: number; visitor_key: string } } = {}) {
    const calls: Array<{ url: string; method: string; body: unknown }> = []
    const resolveOk = opts.resolveOk !== false
    const fetchMock = vi.fn(async (url: string, init?: { method?: string; body?: string }) => {
      calls.push({ url, method: init?.method || 'GET', body: init?.body ? JSON.parse(init.body as string) : null })
      if (url.includes('/turnstile/sitekey')) return { json: async () => ({ code: 0, data: { enabled: false } }) }
      if (/\/acquisition\/[^/]+$/.test(url)) {
        return resolveOk
          ? { json: async () => ({ code: 0, data: { code: 'ABCD2345', channel: '抖音', landing_path: '/client' } }) }
          : { json: async () => ({ code: 404, message: '活动已结束' }) }
      }
      if (url.includes('/scan')) return { json: async () => ({ code: 0, data: { counted: true } }) }
      if (url.includes('/chat/guest')) return { json: async () => ({ code: 0, data: opts.guest || { customer_id: 91, visitor_key: 'vk_new' } }) }
      if (url.includes('/chat/history')) return { json: async () => ({ code: 0, data: [] }) }
      if (url.includes('/chat/welcome')) return { json: async () => ({ code: 0, data: {} }) }
      return { json: async () => ({ code: 0, data: null }) }
    })
    vi.stubGlobal('fetch', fetchMock)
    return calls
  }

  beforeEach(() => {
    localStorage.clear()
    window.history.replaceState({}, '', '/client?code=abcd2345')
  })

  it('带码进来：先 resolve，再把码交给建客请求，最后带 visitor_key 记扫码事件', async () => {
    const calls = stub()
    render(<Client />)
    await waitFor(() => expect(calls.some((c) => c.url.includes('/acquisition/abcd2345'))).toBe(true))
    const guest = calls.find((c) => c.url.includes('/chat/guest'))
    expect(guest?.body).toMatchObject({ code: 'ABCD2345' })
    // resolve 必须排在建客之前（顺序错=死码也照写来源）
    expect(calls.findIndex((c) => c.url.includes('/acquisition/abcd2345'))).toBeLessThan(calls.indexOf(guest!))
    // 扫码事件在建客之后，且带上了刚拿到的访客密钥
    const scan = calls.find((c) => c.url.endsWith('/scan'))
    expect(scan).toBeTruthy()
    expect(calls.indexOf(scan!)).toBeGreaterThan(calls.indexOf(guest!))
    expect(scan?.body).toMatchObject({ visitor_key: 'vk_new' })
    window.history.replaceState({}, '', '/client')
  })

  it('码已停用/不存在：提示活动结束，但照常放行对话且不带码建档、不记事件', async () => {
    const calls = stub({ resolveOk: false })
    render(<Client />)
    expect(await screen.findByText(/这个活动已经结束了/)).toBeTruthy()
    await waitFor(() => expect(calls.some((c) => c.url.includes('/chat/guest'))).toBe(true))
    expect(calls.find((c) => c.url.includes('/chat/guest'))?.body).toMatchObject({ code: '' })
    expect(calls.some((c) => c.url.endsWith('/scan'))).toBe(false)
    window.history.replaceState({}, '', '/client')
  })

  it('老访客带着新码进来：不重复建客，只记一次打开（首触归因不被后来的码改写）', async () => {
    localStorage.setItem('scrm_customer_id', '7')
    localStorage.setItem('scrm_visitor_key', 'vk_old')
    const calls = stub()
    render(<Client />)
    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/scan'))).toBe(true))
    expect(calls.some((c) => c.url.includes('/chat/guest'))).toBe(false)
    expect(calls.find((c) => c.url.endsWith('/scan'))?.body).toMatchObject({ visitor_key: 'vk_old' })
    window.history.replaceState({}, '', '/client')
  })
})
