// F1 PIPL 删除请求 Tab 冒烟：列表渲染、状态筛选触发请求、待处理行显示"立即执行"。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { PrivacyTab } from '../PrivacyTab'

// P1-7 迁移(2026-09-20)：组件改走 lib/api AUTH——mock 层同步换成 AUTH 实现，
// 内部仍调全局 fetch（由各用例 stubGlobal 提供），保留 URL/请求头断言能力
vi.mock('../../../lib/api', () => ({
  AUTH: async (url: string, opts: { method?: string; body?: unknown } = {}) => {
    const r = await fetch(url, {
      method: opts.method || 'GET',
      headers: { Authorization: 'Bearer test-token' },
      ...(opts.body !== undefined ? { body: JSON.stringify(opts.body) } : {}),
    })
    return r.json()
  },
}))
vi.mock('../../../lib/confirm', () => ({
  confirmDialog: () => Promise.resolve(false),
}))

const pendingRow = {
  id: 3, scope: 'customer', customer_id: 66, user_id: 0, status: 'pending',
  requested_at: '2026-09-01T10:00:00Z', deadline: '2026-09-16T10:00:00Z', processed_at: null, error: '',
}
const doneRow = {
  id: 2, scope: 'user', customer_id: 0, user_id: 9, status: 'anonymized',
  requested_at: '2026-08-20T10:00:00Z', deadline: '2026-09-04T10:00:00Z', processed_at: '2026-08-21T10:00:00Z', error: '',
}

/** 构造统一 JSON API 响应。 */
function jsonResp(data: unknown) {
  return { json: () => Promise.resolve({ code: 0, message: 'ok', data }) }
}

describe('PrivacyTab（PIPL 删除请求）', () => {
  beforeEach(() => { vi.restoreAllMocks() })

  it('默认拉取 pending 列表并渲染待处理行的立即执行入口', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResp({ list: [pendingRow, doneRow], total: 2, page: 1, page_size: 100 }))
    vi.stubGlobal('fetch', fetchMock)
    render(<PrivacyTab />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalled())
    // 请求带 pending 状态过滤
    expect(fetchMock.mock.calls[0][0]).toContain('/admin/privacy/deletion-requests')
    expect(fetchMock.mock.calls[0][0]).toContain('status=pending')
    expect(await screen.findByText('立即执行')).toBeTruthy()
    expect(screen.getByText('已处理')).toBeTruthy()
    expect(fetchMock.mock.calls[0][1].headers.Authorization).toBe('Bearer test-token')
  })

  it('切换状态筛选触发新请求', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResp({ list: [], total: 0, page: 1, page_size: 100 }))
    vi.stubGlobal('fetch', fetchMock)
    render(<PrivacyTab />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'failed' } })
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(fetchMock.mock.calls[1][0]).toContain('status=failed')
  })
})
