// F10/T6 客户线索增强页冒烟测试：列表、详情、编辑/打标入口。
// D4(2026-09-23) 追加下钻态冒烟：请求换真相源、口径横幅随行、前端不得再叠筛选。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { CustomersTab } from '../CustomersTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH, apiFetch } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>
const fetchMock = apiFetch as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  fetchMock.mockReset()
})

describe('CustomersTab', () => {
  it('渲染客户列表并可打开详情编辑', async () => {
    fetchMock.mockImplementation(async (url: string) => {
      const data = url.includes('/advisor/customers')
        ? { code: 0, data: { list: [{ id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', assigned_user_name: '销售' }], total: 1 } }
        : { code: 0, data: { customer: { id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', city: '北京', intent_score: 0.82, assigned_user_id: 2, status: 1 }, tags: [{ id: 11, tag_name: '高意向' }], conversations: [] } }
      return { ok: true, status: 200, json: async () => data }
    })
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/admin/tags')) return { code: 0, data: { list: [{ id: 1, name: '高意向', code: 'high_intent' }], total: 1 } }
      if (url.includes('/org/users')) return { code: 0, data: [{ id: 2, username: 'sales1', real_name: '销售' }] }
      return { code: 0, data: [] }
    })
    render(<CustomersTab />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/advisor/customers')))
    expect(await screen.findByText('张三')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '详情' }))
    expect(await screen.findByText('编辑资料')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: '管理标签' }))
    expect(await screen.findByText('保存后会覆盖当前客户标签；如需删除某个标签，取消勾选即可。')).toBeTruthy()
    // 常规态绝不去碰下钻端点（两条真相源各走各的，不互相串）
    const statsCall = authMock.mock.calls.find((c) => String(c[0]).includes('ai-contribution'))
    expect(statsCall).toBeUndefined()
  })
})

// ── D4 下钻态 ─────────────────────────────────────────────────────────────
/** 下钻响应样例：label/note 由后端下发，前端原样透出（不复写第二套文案）。 */
const DRILL_PAYLOAD = {
  metric: 'ai_lead',
  label: 'AI 留资客户',
  period_days: 30,
  since: '2026-08-24T00:00:00+08:00',
  until: '2026-09-23T00:00:00+08:00',
  total: 2,
  page: 1,
  page_size: 20,
  metrics: [{ metric: 'ai_served', label: 'AI 独立接待客户' }, { metric: 'ai_lead', label: 'AI 留资客户' }],
  note: '名单与看板同一判据：登录者数据范围 ∩ 窗口内有消息往来的会话所涉客户。',
  list: [
    { id: 7, name: '李四', phone: '13800002222', journey_stage: 'lead_captured', intent_score: 0.63, interest_model: 'Model X', assigned_user_id: 2, assigned_user_name: '销售', served_by: 'ai' },
    { id: 9, name: '访客_9527', phone: '', journey_stage: 'ordered', intent_score: 0.9, interest_model: '', assigned_user_id: 0, assigned_user_name: '', served_by: 'human' },
  ],
}

/** 只回下钻端点的 AUTH mock（其余路径给空列表，模拟标签/成员下拉）。 */
function mockDrill(payload: Record<string, unknown> = DRILL_PAYLOAD) {
  authMock.mockImplementation(async (url: string) => {
    if (url.includes('/stats/ai-contribution/customers')) return { code: 0, data: payload }
    return { code: 0, data: [] }
  })
}

describe('CustomersTab（AI 贡献度下钻态）', () => {
  it('带 drill 时改打下钻端点，参数只有 metric/days/分页', async () => {
    mockDrill()
    fetchMock.mockImplementation(async () => ({ ok: true, status: 200, json: async () => ({ code: 0, data: {} }) }))
    render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} />)

    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/stats/ai-contribution/customers?')))
    const url = String(authMock.mock.calls.find((c) => String(c[0]).includes('ai-contribution/customers'))![0])
    expect(url).toContain('metric=ai_lead')
    expect(url).toContain('days=30')
    expect(url).toContain('page=1')
    expect(url).toContain('page_size=20')
    // 名单不再走 /advisor/customers：两条真相源不能混用
    expect(fetchMock).not.toHaveBeenCalledWith(expect.stringContaining('/advisor/customers'))
  })

  it('横幅透出后端 label/窗口/total/note，行含姓名·手机·阶段·意向分·接待归属', async () => {
    mockDrill()
    render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} />)

    expect(await screen.findByText(/AI 贡献度下钻/)).toBeTruthy()
    expect(screen.getByText('AI 留资客户')).toBeTruthy()
    expect(screen.getByText(/近 30 天 · 共 2 位客户/)).toBeTruthy()
    expect(screen.getByText(/名单与看板同一判据/)).toBeTruthy()

    expect(screen.getByText('李四')).toBeTruthy()
    expect(screen.getByText(/13800002222/)).toBeTruthy()
    expect(screen.getByText('已留资')).toBeTruthy()
    expect(screen.getByText('意向 63%')).toBeTruthy()
    expect(screen.getByText('AI 独立接待')).toBeTruthy()
    // 访客占位名统一显示为「客户」，人工参与侧另有标注
    expect(screen.getAllByText('客户').length).toBeGreaterThan(0)
    expect(screen.getByText('人工参与接待')).toBeTruthy()
    expect(screen.getByText('意向 90%')).toBeTruthy()
  })

  it('下钻态隐藏阶段筛选与新建/导出（前端再叠一层就会和卡片数字对不上）', async () => {
    mockDrill()
    render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} />)
    await screen.findByText(/AI 贡献度下钻/)
    expect(screen.queryByLabelText('客户阶段筛选')).toBeNull()
    expect(screen.queryByText('+ 新建线索')).toBeNull()
    expect(screen.queryByText('导出客户')).toBeNull()
    expect(screen.getByRole('button', { name: '返回看板' })).toBeTruthy()
  })

  it('返回看板把控制权交回持有者（组件自己不切 Tab）', async () => {
    const onExitDrill = vi.fn()
    mockDrill()
    render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} onExitDrill={onExitDrill} />)
    await screen.findByText(/AI 贡献度下钻/)
    fireEvent.click(screen.getByRole('button', { name: '返回看板' }))
    expect(onExitDrill).toHaveBeenCalledTimes(1)
  })

  it('名单超一页时给出翻页，且下一页带 page=2 重新请求', async () => {
    const many = { ...DRILL_PAYLOAD, total: 45 }
    authMock.mockImplementation(async (url: string) => {
      if (!String(url).includes('/stats/ai-contribution/customers')) return { code: 0, data: [] }
      if (String(url).includes('page=2')) return { code: 0, data: { ...many, page: 2 } }
      return { code: 0, data: many }
    })
    render(<CustomersTab drill={{ metric: 'ai_served', days: 7 }} />)
    await screen.findByText(/AI 贡献度下钻/)
    expect(screen.getByText(/第 1\/3 页/)).toBeTruthy()

    fireEvent.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('page=2')))
  })

  it('页码越界（后端回空列表但 total 如实）自动收回第 1 页，不停在空白页', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (!String(url).includes('/stats/ai-contribution/customers')) return { code: 0, data: [] }
      if (String(url).includes('page=2')) return { code: 0, data: { ...DRILL_PAYLOAD, total: 30, page: 2, list: [] } }
      return { code: 0, data: { ...DRILL_PAYLOAD, total: 30 } }
    })
    render(<CustomersTab drill={{ metric: 'ai_ordered', days: 30 }} />)
    await screen.findByText(/AI 贡献度下钻/)
    fireEvent.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('page=1')))
    expect(authMock).toHaveBeenCalledWith(expect.stringContaining('page=2'))
  })

  it('指标一变就是另一份名单：页码回到第 1 页并带新 metric 重查', async () => {
    mockDrill()
    const { rerender } = render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} />)
    await screen.findByText(/AI 贡献度下钻/)
    rerender(<CustomersTab drill={{ metric: 'ai_ordered', days: 90 }} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('metric=ai_ordered')))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('days=90')))
  })

  it('下钻请求失败显式留空名单并清空横幅，不把旧名单留在屏上', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/stats/ai-contribution/customers')) return { code: 500, data: null, message: 'boom' }
      return { code: 0, data: [] }
    })
    render(<CustomersTab drill={{ metric: 'ai_arrived', days: 30 }} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/stats/ai-contribution/customers')))
    expect(await screen.findByText(/该窗口内没有命中这个指标的客户/)).toBeTruthy()
    expect(screen.queryByText('李四')).toBeNull()
  })
})
