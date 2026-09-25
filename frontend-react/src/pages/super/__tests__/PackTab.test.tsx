// 超管「行业包管理」档位门槛单测（G-22c，2026-09-24）。
//
// 这一列决定"哪个包对哪一档客户开放"，是销售口径而不是包作者口径，所以断言重点：
//   1. 每行的下拉值必须逐字回显后端 min_tier（空串显示"全部档位"，不能显示成"个人版"）
//   2. 改档位真的 PUT /tier，并且成功后重拉列表（否则界面停在一个没生效的值上）
//   3. 清除门槛=PUT 空串，必须发请求（不发 = 平台再也关不掉已放开的包）
import { fireEvent, render, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  authHeaders: () => ({ Authorization: 'Bearer test-token' }),
}))

import { AUTH } from '../../../lib/api'
import { PackTab } from '../PackTab'

const authMock = AUTH as ReturnType<typeof vi.fn>

/** 目录两行：一行已限企业版，一行没设门槛。 */
const ROWS = [
  { id: 1, code: 'lux', name: '奢侈品', industry: 'lux', version: '1.0.0', pack_level: 'industry', parent_code: '', share_cross_dept: 1, min_tier: 'enterprise', file_name: 'lux.aipack', file_size: 1024, status: 'active', created_at: '2026-09-24T10:00:00+08:00' },
  { id: 2, code: 'auto', name: '汽车', industry: 'auto', version: '1.0.0', pack_level: 'industry', parent_code: '', share_cross_dept: 1, min_tier: '', file_name: 'auto.aipack', file_size: 1024, status: 'active', created_at: '2026-09-24T10:00:00+08:00' },
]

/** 列表请求计数（PUT /packs/:id/tier 不算——锚点收在整串，前缀匹配会把动作请求也数进来）。 */
function listCalls() {
  return authMock.mock.calls.filter((c) => /^\/api\/v1\/super\/packs(\?.*)?$/.test(String(c[0]))).length
}

/** 取某行的显示 input（TDesign 单选把选中 label 落在它身上）。 */
function rowInput(container: HTMLElement, rowText: string): HTMLInputElement | undefined {
  const row = Array.from(container.querySelectorAll('tr')).find((tr) => (tr.textContent || '').includes(rowText))
  return row?.querySelector<HTMLInputElement>('input.t-input__inner')
}

/** 点开某行的「可绑档位」下拉（选项浮层挂在 body，不在行内）。 */
async function openTierPanel(container: HTMLElement, rowText: string) {
  await waitFor(() => expect(rowInput(container, rowText)).toBeTruthy())
  fireEvent.click(rowInput(container, rowText) as HTMLInputElement)
  await waitFor(() => expect(document.querySelectorAll('.t-select-option').length).toBeGreaterThan(0))
}

function optionByText(text: string) {
  return Array.from(document.querySelectorAll<HTMLElement>('.t-select-option')).find((o) => (o.textContent || '').trim() === text)
}

beforeEach(() => {
  authMock.mockReset()
  authMock.mockImplementation(async (url: string) => (/^\/api\/v1\/super\/packs/.test(String(url)) ? { code: 0, data: ROWS } : { code: 0, data: [] }))
})

describe('PackTab（超管档位门槛）', { timeout: 15000 }, () => {
  it('逐行回显 min_tier：企业版行显示企业版，空值行显示"全部档位"', async () => {
    const { container } = render(<PackTab />)
    await waitFor(() => expect(listCalls()).toBeGreaterThan(0))
    // waitFor 而非直接取值：TDesign Select 把 value→label 的映射放在一次 effect 里，
    // 行刚渲染出来那一刻读到的还是原始码。真浏览器上同理（首帧闪原始码），
    // 但断言要的是"最终显示中文档位名"，不是"某一帧恰好是码"。
    await waitFor(() => expect(rowInput(container, '奢侈品')?.value).toContain('企业版'))
    // '' 的语义是"不设门槛"，渲染成"个人版"会让人误以为已经收口了
    expect(rowInput(container, '汽车')?.value).toContain('全部档位')
  })

  it('改档位 PUT 对应包并重拉列表', async () => {
    const { container } = render(<PackTab />)
    await waitFor(() => expect(rowInput(container, '汽车')).toBeTruthy())
    const before = listCalls()
    await openTierPanel(container, '汽车')
    fireEvent.click(optionByText('企业版+') as HTMLElement)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/packs/2/tier', { method: 'PUT', body: { min_tier: 'enterprise' } }))
    // 动作成功后必须回读，否则下拉停在前端猜测值上
    await waitFor(() => expect(listCalls()).toBe(before + 1))
  })

  it('清除门槛也要发请求（PUT 空串），不是就地不发', async () => {
    const { container } = render(<PackTab />)
    await waitFor(() => expect(rowInput(container, '奢侈品')).toBeTruthy())
    await openTierPanel(container, '奢侈品')
    fireEvent.click(optionByText('全部档位') as HTMLElement)
    // 反证用例：若实现把空值当"没改动"跳过，平台就再也关不掉已放开的包
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/packs/1/tier', { method: 'PUT', body: { min_tier: '' } }))
  })
})
