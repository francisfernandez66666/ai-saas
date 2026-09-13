// F4/T6 标签体系增强冒烟测试。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { TagSystemTab } from '../TagSystemTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('TagSystemTab', () => {
  it('渲染标签字典列表', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/tag-rules')) return { code: 0, data: { list: [], total: 0 } }
      if (url.includes('/tag-weights')) return { code: 0, data: { list: [], total: 0 } }
      return { code: 0, data: { list: [{ id: 1, name: '高意向', code: 'high_intent', category: 'intent', weight: 1.5, status: 1 }], total: 1 } }
    })
    render(<TagSystemTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/admin/tags')))
    expect((await screen.findAllByText('高意向')).length).toBeGreaterThan(0)
  })
})
