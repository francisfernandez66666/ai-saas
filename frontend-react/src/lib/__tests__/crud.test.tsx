// F1 通用 CRUD 状态层与表格冒烟测试。
import { act, render, screen, waitFor } from '@testing-library/react'
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

  // P1-13 复核批（2026-09-15）：快速并发 load 时"迟到旧响应必须作废"——
  // 旧实现无代际守卫，慢的先回会把快的结果覆盖回旧数据（列表与筛选条件错位）。
  it('迟到的旧请求响应不覆盖新结果（代数守卫）', async () => {
    let releaseOld: (v: any) => void
    const oldPending = new Promise<any>((r) => { releaseOld = r })
    authMock.mockImplementationOnce(() => oldPending)
    authMock.mockImplementationOnce(async () => ({ code: 0, data: [{ id: 99 }] }))
    let captured: any
    render(<Harness base="/api/v1/admin/demo" onResult={(c) => { captured = c }} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(1))
    act(() => { void captured.reload() }) // 第二个请求发出（旧请求仍在途）
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(screen.getByTestId('rows').textContent).toBe('1'))
    releaseOld!({ code: 0, data: [{ id: 1 }, { id: 2 }, { id: 3 }] }) // 旧请求迟到返回 3 行
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.getByTestId('rows').textContent).toBe('1') // 必须仍为新结果
    expect(captured.loading).toBe(false)
  })
