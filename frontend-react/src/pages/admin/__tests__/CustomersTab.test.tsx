// F10/T6 客户线索增强页冒烟测试：列表、详情、编辑/打标入口。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CustomersTab } from '../CustomersTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH, apiFetch } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>
const fetchMock = apiFetch as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  fetchMock.mockReset()
})

describe('CustomersTab', () => {
  it('渲染客户列表并可打开详情编辑', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      const data = url.includes('/advisor/customers')
        ? { code: 0, data: { list: [{ id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', assigned_user_name: '销售' }], total: 1 } }
        : { code: 0, data: { customer: { id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', city: '北京', intent_score: 0.82, assigned_user_id: 2, status: 1 }, tags: [{ id: 11, tag_name: '高意向' }], conversations: [] } }
      return { ok: true, status: 200, json: async () => data }
    })
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/admin/tags')) return { code: 0, data: { list: [{ id: 1, name: '高意向', code: 'high_intent' }], total: 1 } }
      if (url.includes('/org/users')) return { code: 0, data: [{ id: 2, username: 'sales1', real_name: '销售' }] }
      return { code: 0, data: [] }
    })
    render(<CustomersTab />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/advisor/customers')))
    expect(await screen.findByText('张三')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '详情' }))
    expect(await screen.findByText('编辑资料')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '管理标签' }))
    expect(await screen.findByText('保存后会覆盖当前客户标签；如需删除某个标签，取消勾选即可。')).toBeTruthy()
  })
})
