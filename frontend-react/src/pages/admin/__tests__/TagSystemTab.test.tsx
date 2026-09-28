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
    // 命名夹具：mock 与断言共用一份
    const TAG = { id: 1, name: '高意向', code: 'high_intent', category: 'intent', weight: 1.5, status: 1 }
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/tag-rules')) return { code: 0, data: { list: [], total: 0 } }
      if (url.includes('/tag-weights')) return { code: 0, data: { list: [], total: 0 } }
      return { code: 0, data: { list: [TAG], total: 1 } }
    })
    render(<TagSystemTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/admin/tags')))
    // 原来弱在哪：findAllByText('高意向').length>0 连静态说明表里的「高意向」也算数，字典行没渲染也可能蒙绿；
    // 现在用编码（页面上唯一）定位字典行，行内钉 name/分类/权重都来自回包
    const codeEl = await screen.findByText(TAG.code)
    const row = codeEl.closest('tr')
    if (!row) throw new Error('编码单元格不在表格行内')
    expect(row).toHaveTextContent(TAG.name)
    expect(row).toHaveTextContent(TAG.category)
    expect(row).toHaveTextContent(String(TAG.weight))
  })
})
