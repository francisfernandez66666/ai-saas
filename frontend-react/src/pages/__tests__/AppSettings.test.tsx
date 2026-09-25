// F13/T6 移动端设置页 PIPL 删除权入口冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppSettings from '../AppSettings'

vi.mock('../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

// E8：删除入口改用 confirmDialog（非原生 confirm），mock 成"用户点了确认"
vi.mock('../../lib/confirm', () => ({
  confirmDialog: () => Promise.resolve(true),
  uiAlert: () => Promise.resolve(),
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn(), info: vi.fn() },
}))

import { AUTH } from '../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('role', 'user')
  authMock.mockReset()
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

// G-23(2026-09-25)：知识库改「全员只读 + 管理员可写」。旧口径是成员整块隐藏，
// 销售在移动端看不到公司沉淀的资料；后端另开了 /advisor/kb/my 只读子集，
// 上传/删除/注销仍只在 admin 组——所以成员侧必须不发出这两个请求，否则得到 403。
describe('AppSettings 知识库角色分流（G-23）', () => {
  const kbResp = { code: 0, data: { list: [{ id: 7, title: '售后政策' }], total: 1, page: 1, page_size: 50 } }

  it('成员读 /advisor/kb/my，列表只读（无上传表单、无删除入口）', async () => {
    localStorage.setItem('role', 'user')
    authMock.mockImplementation(async (url: string) => (url.includes('/kb/my') ? kbResp : { code: 0, data: {} }))
    render(<AppSettings />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/advisor/kb/my')))
    expect(await screen.findByText('售后政策')).toBeTruthy()
    expect(screen.queryByRole('button', { name: '上传切片入库' })).toBeNull()
    expect(screen.queryByText('删除')).toBeNull()
    expect(screen.getByText(/请联系管理员/)).toBeTruthy()
    // 成员侧不得打管理端点（打了就是 403）
    expect(authMock.mock.calls.some((c) => String(c[0]).startsWith('/api/v1/admin/'))).toBe(false)
  })

  it('管理员读 /admin/kb/my 且保留上传/删除', async () => {
    localStorage.setItem('role', 'tenant_admin')
    authMock.mockImplementation(async (url: string) => (url.includes('/kb/my') ? kbResp : { code: 0, data: {} }))
    render(<AppSettings />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/kb/my')))
    expect(await screen.findByText('售后政策')).toBeTruthy()
    expect(screen.getByRole('button', { name: '上传切片入库' })).toBeTruthy()
    expect(screen.getByText('删除')).toBeTruthy()
  })
})
