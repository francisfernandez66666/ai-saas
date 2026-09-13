// F8/T6 CDP 画像页冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import CdpTab from '../CdpTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('CdpTab', () => {
  it('展示标签字典并查询 OneID 画像', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/tag-defs')) {
        return { code: 0, data: [{ id: 1, code: 'beh_lead_captured', name: '已留资', category: 'behavior', weight_default: 2, is_active: true }] }
      }
      if (url.includes('/customers')) return { code: 0, data: { list: [{ id: 123, name: '张三' }], total: 1 } }
      if (url.includes('/segments')) return { code: 0, data: { tag: 'beh_lead_captured', total: 1, one_ids: ['c:123'] } }
      if (url.includes('/profiles/')) return { code: 0, data: { one_id: 'c:123', name: '张三', status: 1, event_count: 8, tags: { beh_lead_captured: 'yes' } } }
      return { code: 0, data: [] }
    })
    render(<CdpTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/cdp/tag-defs'))
    expect(await screen.findByText('已留资')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '圈选' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/cdp/segments?tag=beh_lead_captured'))
    expect(await screen.findByText('c:123')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '123 张三' }))
    expect(await screen.findByText('事件数')).toBeTruthy()
    expect(await screen.findByText('yes')).toBeTruthy()
  })
})
