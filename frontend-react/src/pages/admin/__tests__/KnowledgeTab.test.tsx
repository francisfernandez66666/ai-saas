// F2/T6 知识库 Tab 冒烟测试：片段列表渲染与向量状态。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import KnowledgeTab from '../KnowledgeTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('KnowledgeTab', () => {
  it('渲染知识片段标题和向量状态', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/fragments')) {
        return { code: 0, data: { list: [{ id: 7, category: '售后', title: '质保政策', content: '整车三年', tags: '质保,售后', vectorized: true, status: 1 }], total: 1, page: 1, page_size: 20 } }
      }
      return { code: 0, data: { list: [], total: 0 } }
    })
    render(<KnowledgeTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    expect(await screen.findByText('质保政策')).toBeInTheDocument()
    expect(screen.getByText('已向量化')).toBeInTheDocument()
  })
})
