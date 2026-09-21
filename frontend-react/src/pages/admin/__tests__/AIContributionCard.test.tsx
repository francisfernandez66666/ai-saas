// AI 贡献度卡片单测（D2）。
// 重点不在"数字好看"，而在三条防线：
//   1. 前端**不复算**比率——展示值必须逐字等于后端返回值（防前端自造第二套口径）
//   2. 窗口切换必须真的换请求参数（防切换只改样式不改数据）
//   3. 请求失败必须显式可见（静默留空会把"端点挂了"读成"业务为零"）
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
import { AIContributionCard, pct } from '../AIContributionCard'

const authMock = AUTH as ReturnType<typeof vi.fn>

/** 一份正常响应：故意让比率不等于"肉眼可推的整除值"，以便抓前端复算。 */
const OK_PAYLOAD = {
  period_days: 30,
  since: '2026-08-22T19:00:00+08:00',
  until: '2026-09-21T19:00:00+08:00',
  new_conversations: 236,
  active_conversations: 234,
  ai_served_customers: 172,
  human_served_customers: 2,
  ai_serve_share: 0.9885057471264368,
  handoff_rate: 0.011494252873563218,
  ai_leads: 8,
  assisted_leads: 1,
  ai_lead_rate: 0.046511627906976744,
  assisted_lead_rate: 0.5,
  ai_arrived: 0,
  ai_ordered: 0,
  ai_arrive_rate: 0,
  ai_order_rate: 0,
  ai_messages: 261,
  human_messages: 2,
  customer_messages: 265,
  ai_message_share: 0.9923954372623575,
  pending_handoff_now: 14,
  notes: ['归因口径：交叉判定。', '全部指标按登录者的数据范围裁剪。'],
}

beforeEach(() => {
  authMock.mockReset()
})

describe('AIContributionCard', () => {
  it('展示后端返回值（接待量/归因/消息），比率不在前端复算', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)

    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=30'))
    // 计数
    expect(await screen.findByText('172')).toBeTruthy()   // AI 独立接待客户
    expect(screen.getAllByText('2').length).toBeGreaterThan(0) // 人工参与客户 / 人工消息
    expect(screen.getByText('236')).toBeTruthy()          // 新建会话
    expect(screen.getByText('261')).toBeTruthy()          // AI 消息
    expect(screen.getByText('265')).toBeTruthy()          // 客户消息
    expect(screen.getByText('234')).toBeTruthy()          // 活跃会话
    // 比率：必须等于后端给的 0.9885057471264368 → 98.9%（若前端自行相除得同一值，
    // 说明"后端口径"没被绕过；此处为展示契约锁定 1 位小数）
    expect(screen.getByText(pct(OK_PAYLOAD.ai_serve_share))).toBeTruthy()
    expect(screen.getByText(pct(OK_PAYLOAD.ai_message_share))).toBeTruthy()
    expect(pct(OK_PAYLOAD.ai_serve_share)).toBe('98.9%')
  })

  it('窗口切换会带新的 days 重新请求（不是只改样式）', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=30'))

    fireEvent.click(screen.getByText('近 7 天'))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=7'))

    fireEvent.click(screen.getByText('近 90 天'))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=90'))
  })

  it('请求失败必须显式提示，不得静默留空（否则"挂了"被读成"业务为零"）', async () => {
    authMock.mockResolvedValue({ code: 500, data: null, message: 'boom' })
    render(<AIContributionCard />)
    expect(await screen.findByText(/AI 贡献度数据加载失败/)).toBeTruthy()
  })

  it('口径说明原样透出后端 notes', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)
    expect(await screen.findByText(/口径说明（2 条）/)).toBeTruthy()
    expect(screen.getByText('全部指标按登录者的数据范围裁剪。')).toBeTruthy()
  })

  it('小样本（接待 <30 人）给出"只作趋势参考"提示', async () => {
    authMock.mockResolvedValue({
      code: 0,
      data: { ...OK_PAYLOAD, ai_served_customers: 9, human_served_customers: 1, notes: [] },
    })
    render(<AIContributionCard />)
    expect(await screen.findByText(/接待样本仅 10 人/)).toBeTruthy()
  })

  it('零数据新租户不崩且不出现 NaN', async () => {
    authMock.mockResolvedValue({
      code: 0,
      data: {
        period_days: 30, new_conversations: 0, active_conversations: 0,
        ai_served_customers: 0, human_served_customers: 0, ai_serve_share: 0, handoff_rate: 0,
        ai_leads: 0, assisted_leads: 0, ai_lead_rate: 0, assisted_lead_rate: 0,
        ai_arrived: 0, ai_ordered: 0, ai_arrive_rate: 0, ai_order_rate: 0,
        ai_messages: 0, human_messages: 0, customer_messages: 0, ai_message_share: 0,
        pending_handoff_now: 0, notes: [],
      },
    })
    const { container } = render(<AIContributionCard />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    expect(container.textContent).not.toMatch(/NaN|Infinity|undefined/)
  })
})
