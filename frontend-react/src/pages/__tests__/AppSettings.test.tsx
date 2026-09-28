// F13/T6 移动端设置页 PIPL 删除权入口冒烟测试。
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
    // 原来弱在哪：/已受理/ 命中任意包含这四个字的节点即可，"受理了但没带期限回执"
    // 这种残缺文案也绿。现在钉：AppSettings.tsx 的拼接前缀「已受理，预计 」（夹具返回了
    // deadline 就必带此前缀）。具体日期串取决于运行机时区（toLocaleString 无 tz 注入），
    // 钉死会把用例变成本机专属——故只钉到前缀为止。
    expect(await screen.findByText(/已受理/)).toHaveTextContent(/^已受理，预计 /)
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
    // 原来弱在哪：toBeTruthy 只证明页面上有"售后政策"四个字。现在钉：命中的是 <li> 本体、
    // 文本逐字等（夹具 title），且该条目内不含任何 <a> 删除链接（成员侧只读由
    // AppSettings.tsx `{isAdmin && <a>删除</a>}` 条件渲染决定）。
    const item = await screen.findByText('售后政策')
    expect(item.tagName).toBe('LI')
    expect(item).toHaveTextContent('售后政策')
    expect(item.querySelector('a')).toBeNull()
    expect(screen.queryByRole('button', { name: '上传切片入库' })).toBeNull()
    expect(screen.queryByText('删除')).toBeNull()
    // 成员引导文案整句钉死（AppSettings.tsx 非 admin 分支的完整字符串，不再只匹配尾缀）
    expect(screen.getByText(/请联系管理员/))
      .toHaveTextContent('本租户已沉淀的资料，AI 对话会自动引用；需要补充或修改请联系管理员')
    // 成员侧不得打管理端点（打了就是 403）
    expect(authMock.mock.calls.some((c) => String(c[0]).startsWith('/api/v1/admin/'))).toBe(false)
  })

  it('管理员读 /admin/kb/my 且保留上传/删除', async () => {
    localStorage.setItem('role', 'tenant_admin')
    authMock.mockImplementation(async (url: string) => (url.includes('/kb/my') ? kbResp : { code: 0, data: {} }))
    render(<AppSettings />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/kb/my')))
    // 原来弱在哪：三处 toBeTruthy 都只证明"字出现了"。现在钉：条目是 <li> 且行内带
    // <a>删除</a>（组件里删除入口就是 li 内的 a 标签）；上传按钮可见且可用。
    const item = await screen.findByText('售后政策')
    expect(item.tagName).toBe('LI')
    expect(item).toHaveTextContent('售后政策')
    const delLink = within(item).getByText('删除')
    expect(delLink.tagName).toBe('A')
    const uploadBtn = screen.getByRole('button', { name: '上传切片入库' })
    expect(uploadBtn).toHaveTextContent('上传切片入库')
    expect(uploadBtn).toBeEnabled()
  })
})
