// F12/T6 顾问工作台通道侧边栏冒烟测试。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import Advisor from '../Advisor'

vi.mock('../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
  setToken: vi.fn(),
  logoutAndRedirect: vi.fn(),
}))
vi.mock('../../lib/realtime', () => ({ useAdvisorWS: vi.fn() }))
vi.mock('../../lib/chat', () => ({ collectFreshMessages: () => [] }))
vi.mock('../../lib/branding', () => ({ useBrand: () => ({ brandName: '测试品牌' }) }))

import { AUTH } from '../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  window.history.replaceState({}, '', '/advisor?corpid=ww_test&external_userid=wm_test_1')
})

describe('Advisor 通道侧边栏', () => {
  it('URL 带渠道参数时展示企业微信客户卡且不出现 AI 文案', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/channel/wecom/context')) return { code: 0, data: { customer_id: 12, name: '渠道客户', journey_stage: 'lead_captured', interest_model: 'Model X', tags: '试驾,报价', staff_id: 'wx_staff_9' } }
      if (url.includes('/advisor/stats')) return { code: 0, data: [] }
      if (url.includes('/advisor/customers')) return { code: 0, data: { list: [{ id: 12, name: '渠道客户', journey_stage: 'lead_captured', updated_at: new Date().toISOString() }] } }
      if (url.includes('/advisor/customer/12')) return { code: 0, data: { customer: { id: 12, name: '渠道客户', phone: '13800001111', journey_stage: 'lead_captured', interest_model: 'Model X', budget: 30, remark: '', status: 1 }, tags: [{ id: 1, tag_name: '高意向' }], conversations: [] } }
      if (url.includes('/advisor/tags')) return { code: 0, data: { list: [{ id: 1, name: '高意向', code: 'high_intent', status: 1 }] } } // P0-10：后端 GetTagList 返回 Tag 对象数组
      if (url.includes('/chat/history')) return { code: 0, data: [] }
      if (url.includes('/test-drives')) return { code: 0, data: [] }
      return { code: 0, data: null }
    })
    render(<Advisor />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/channel/wecom/context?corpid=ww_test&external_userid=wm_test_1')))
    expect(await screen.findByText('企业微信客户')).toBeTruthy()
    expect(screen.getByText('wm_test_1')).toBeTruthy()
    expect(screen.getAllByText('Model X').length).toBeGreaterThan(0)
    expect(screen.queryByText(/AI/)).toBeNull()
  })
})
