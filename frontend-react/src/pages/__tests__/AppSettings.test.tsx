// F13/T6 移动端设置页 PIPL 删除权入口冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppSettings from '../AppSettings'

vi.mock('../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('role', 'user')
  authMock.mockReset()
  vi.stubGlobal('confirm', () => true)
})

describe('AppSettings 个人信息删除', () => {
  it('成员可提交账号 PII 删除请求', async () => {
    authMock.mockImplementation(async (url: string, opts?: { method?: string }) => {
      if (url.includes('/privacy/deletion-request') && opts?.method === 'POST') return { code: 0, data: { deadline: '2026-09-28T00:00:00Z' } }
      return { code: 0, data: { list: [], total: 0, page: 1, page_size: 20 } }
    })
    render(<AppSettings />)
    fireEvent.click(await screen.findByRole('button', { name: '申请删除账号信息' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/privacy/deletion-request', expect.objectContaining({ method: 'POST', body: { scope: 'user' } })))
    expect(await screen.findByText(/已受理/)).toBeTruthy()
  })
})
