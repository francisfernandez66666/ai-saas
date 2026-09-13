// F3/T6 租户资料页冒烟测试：列表、向量状态与融合检索开关。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import TenantKBTab from '../TenantKBTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('TenantKBTab', () => {
  it('渲染已上传片段与向量状态', async () => {
    authMock.mockResolvedValue({
      code: 0,
      data: {
        list: [{ id: 9, title: '门店活动规则', content: '每月第一周试驾有礼', category: '企业知识', vectorized: true, created_at: '2026-09-13T12:00:00+08:00' }],
        total: 1,
        page: 1,
        page_size: 20,
      },
    })
    render(<TenantKBTab configs={[{ key: 'kb_vector_search', category: 'ai', value: 'true', value_type: 'bool' }]} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/admin/kb/my')))
    expect(await screen.findByText('门店活动规则')).toBeInTheDocument()
    expect(screen.getByText('已向量化')).toBeInTheDocument()
    expect(screen.getByText('向量融合检索开启')).toBeInTheDocument()
  })
})
