// F1 通用 CRUD 状态层与表格冒烟测试。
import { render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import { useCrud } from '../../hooks/useCrud'

vi.mock('../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

/** useCrud 测试夹具：把 hook 结果暴露给断言。 */
function Harness({ base, onResult }: { base: string; onResult: (c: any) => void }) {
  const crud = useCrud(base, { pageSize: 10 })
  onResult(crud)
  return <span data-testid="rows">{crud.rows.length}</span>
}

beforeEach(() => authMock.mockReset())

describe('useCrud', () => {
  it('GET 列表并保留分页信封', async () => {
    authMock.mockResolvedValue({ code: 0, data: { list: [{ id: 1 }, { id: 2 }], total: 8, page: 1, page_size: 10 } })
    let captured: any
    render(<Harness base="/api/v1/admin/demo" onResult={(c) => { captured = c }} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(1))
    expect(authMock.mock.calls[0][0]).toContain('page_size=10')
    await waitFor(() => expect(screen.getByTestId('rows').textContent).toBe('2'))
    expect(captured.total).toBe(8)
  })

  it('POST 创建后触发刷新', async () => {
    authMock
      .mockResolvedValueOnce({ code: 0, data: [] })
      .mockResolvedValueOnce({ code: 0, data: { list: [{ id: 1 }] } })
      .mockResolvedValueOnce({ code: 0, data: { list: [{ id: 1 }, { id: 2 }] } })
    let captured: any
    render(<Harness base="/api/v1/admin/demo" onResult={(c) => { captured = c }} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(1))
    await captured.create({ name: '新增' })
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(3))
    expect(authMock.mock.calls[1]).toEqual(['/api/v1/admin/demo', { method: 'POST', body: { name: '新增' } }])
  })
})
