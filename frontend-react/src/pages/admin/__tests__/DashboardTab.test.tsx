// F9/T6 工作台指标看板冒烟测试。
// D4(2026-09-23) 追加：工作台必须把下钻回调原样递给贡献度卡片——卡片自己无法切 Tab，
// 断点若在这一层，看板的数字就退化成"只能看"。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DashboardTab } from '../DashboardTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  vi.stubGlobal('fetch', vi.fn(async () => ({ json: async () => ({ code: 0, data: { list: [{ id: 1, name: '张三', journey_stage: 'lead_captured', intent_score: 0.86 }] } }) })))
})

describe('DashboardTab', () => {
  it('渲染经营指标和模型健康', async () => {
    // 命名夹具：mock 与断言共用一份，期望串按 DashboardTab.tsx 的渲染口径推导
    const OVERVIEW = { total_customers: 12, new_customers_today: 3, active_conversations: 4, conversion_rate: 0.25, avg_intent_score: 0.67, human_transfer_rate: 0.18 }
    const ADVISOR = [{ label: '跟进中', value: 8, color: 'blue' }]
    const MODEL = { display_name: 'GLM-4-9B (硅基流动免费)', provider: 'siliconflow', available: false, consecutive_fails: 5, cooldown_left_sec: 120 }
    const fmtPct = (v: number) => `${(v * 100).toFixed(1)}%` // 镜像 DashboardTab.tsx:40 pct()
    const fmtCd = (sec: number) => (!sec ? '-' : sec < 60 ? `${sec}s` : `${Math.ceil(sec / 60)}min`) // 镜像 :45 formatCooldown()
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/stats/overview')) return { code: 0, data: OVERVIEW }
      if (url.includes('/advisor/stats')) return { code: 0, data: ADVISOR }
      if (url.includes('/admin/models')) return { code: 0, data: { models: [MODEL] } }
      return { code: 0, data: { list: [] } }
    })
    render(<DashboardTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/overview'))
    // 原来弱在哪：toBeTruthy 只证明查到；现在钉「有效客户」格子读到的就是 overview.total_customers
    expect(await screen.findByText(String(OVERVIEW.total_customers))).toBeInTheDocument()
    // 原来弱在哪：字面量 '25.0%'；现在由 conversion_rate 按组件 pct() 口径推导
    expect(screen.getByText(fmtPct(OVERVIEW.conversion_rate))).toBeInTheDocument()
    // 原来弱在哪：字面量；现在钉顾问漏斗卡片标签就是 advisor/stats 回包的 label
    expect(screen.getByText(ADVISOR[0].label)).toBeInTheDocument()
    // 原来弱在哪：字面量；现在钉模型健康表第一行展示名来自 models 回包
    expect(screen.getByText(MODEL.display_name)).toBeInTheDocument()
    // 原来弱在哪：length>0；现在钉「冷却」= 表头 + available=false 的状态 Tag，恰好 2 处
    expect((await screen.findAllByText('冷却')).length).toBe(2)
    // 顺带钉冷却倒计时列按 formatCooldown(120s) 渲染成「2min」
    expect(screen.getByText(fmtCd(MODEL.cooldown_left_sec))).toBeInTheDocument()
  })

  it('把 AI 贡献度的下钻点击原样递给持有者（指标 + 当前窗口）', async () => {
    const onDrill = vi.fn()
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/stats/ai-contribution')) {
        return {
          code: 0,
          data: {
            period_days: 30, new_conversations: 5, active_conversations: 4,
            ai_served_customers: 12, human_served_customers: 2, ai_serve_share: 0.857, handoff_rate: 0.143,
            ai_leads: 3, assisted_leads: 1, ai_lead_rate: 0.25, assisted_lead_rate: 0.5,
            ai_arrived: 1, ai_ordered: 1, ai_arrive_rate: 0.083, ai_order_rate: 0.083,
            ai_messages: 40, human_messages: 5, customer_messages: 30, ai_message_share: 0.88,
            pending_handoff_now: 0, notes: [],
          },
        }
      }
      // 其余端点按各自真实形态回值（advisor/stats 是数组、models 是对象），别一稿通用形态把页面带崩
      if (url.includes('/stats/overview')) return { code: 0, data: { total_customers: 12, new_customers_today: 3, active_conversations: 4, conversion_rate: 0.25, avg_intent_score: 0.67, human_transfer_rate: 0.18 } }
      if (url.includes('/advisor/stats')) return { code: 0, data: [] }
      if (url.includes('/admin/models')) return { code: 0, data: { models: [] } }
      return { code: 0, data: { list: [] } }
    })
    render(<DashboardTab onDrill={onDrill} />)
    // findByRole 会等到卡片真拿到数据（未拿到时格子不是 button，正是"没数据不可点"那条规则）
    const tile = await screen.findByRole('button', { name: /AI 留资/ })
    fireEvent.click(tile)
    expect(onDrill).toHaveBeenCalledWith({ metric: 'ai_lead', days: 30 })
  })
})
