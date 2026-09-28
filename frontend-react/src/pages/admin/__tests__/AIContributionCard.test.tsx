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

// 格子「值↔标签」配对断言：Tile/页脚 span 的结构里值与标签同容器
// （AIContributionCard.tsx：格子是 <div><b>值</b><span>标签</span></div>，
// 页脚是 <span>标签 <b>值</b></span>）。原来弱在哪：findByText('172') 配 truthy
// 与 getAllByText('2').length>0 只证明"这个数字在卡片某处出现过"——172 挂错格子、
// 人工参与客户的 2 和人工消息的 2 互相顶包，全都绿。现在钉"这个数字就在标签为 X 的
// 那格子里"，期望值全部由 OK_PAYLOAD 逐字段推得。
function tileWith(label: string): HTMLElement {
  const span = screen.getByText(label)
  const holder = (span.closest('div') || span) as HTMLElement
  return holder
}

describe('AIContributionCard', () => {
  it('展示后端返回值（接待量/归因/消息），比率不在前端复算', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)

    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=30'))
    // 计数：每格值与标签同容器（值来自夹具，非"页面某处出现过"）
    expect(tileWith('AI 独立接待客户')).toHaveTextContent(`172AI 独立接待客户`) // ai_served_customers=172
    expect(tileWith('人工参与客户')).toHaveTextContent(`2人工参与客户`)          // human_served_customers=2
    expect(screen.getByText('新建会话')).toHaveTextContent(`新建会话 ${OK_PAYLOAD.new_conversations}`)
    expect(screen.getByText('AI 消息')).toHaveTextContent(`AI 消息 ${OK_PAYLOAD.ai_messages}`)
    expect(screen.getByText('客户消息')).toHaveTextContent(`客户消息 ${OK_PAYLOAD.customer_messages}`)
    expect(screen.getByText('活跃会话')).toHaveTextContent(`活跃会话 ${OK_PAYLOAD.active_conversations}`)
    // 比率：必须等于后端给的 0.9885057471264368 → 98.9%（若前端自行相除得同一值，
    // 说明"后端口径"没被绕过；此处为展示契约锁定 1 位小数）。同样要求"值在标签格内"。
    expect(tileWith('AI 独立接待占比')).toHaveTextContent(pct(OK_PAYLOAD.ai_serve_share))
    expect(screen.getByText('AI 消息占比')).toHaveTextContent(pct(OK_PAYLOAD.ai_message_share))
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
    // 原来弱在哪：只匹配前半句；现在钉失败文案整句（含"请点上方窗口重试"的引导语）
    expect(await screen.findByText(/AI 贡献度数据加载失败/))
      .toHaveTextContent('AI 贡献度数据加载失败，请点上方窗口重试。')
  })

  it('口径说明原样透出后端 notes', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)
    // 条数由夹具 notes.length 推得（组件渲染「口径说明（N 条）」），不再正则模糊命中
    expect(await screen.findByText(`口径说明（${OK_PAYLOAD.notes.length} 条）`))
      .toHaveTextContent(`口径说明（${OK_PAYLOAD.notes.length} 条）`)
    // 每条 note 必须是 <li> 本体且逐字等于后端下发文案（前端不改写口径措辞）
    const noteLi = screen.getByText('全部指标按登录者的数据范围裁剪。')
    expect(noteLi.tagName).toBe('LI')
    expect(noteLi).toHaveTextContent('全部指标按登录者的数据范围裁剪。')
  })

  it('小样本（接待 <30 人）给出"只作趋势参考"提示', async () => {
    authMock.mockResolvedValue({
      code: 0,
      data: { ...OK_PAYLOAD, ai_served_customers: 9, human_served_customers: 1, notes: [] },
    })
    render(<AIContributionCard />)
    // 「10 人」由夹具推得：ai_served 9 + human_served 1（组件 served 两列相加）
    expect(await screen.findByText(/接待样本仅 10 人/))
      .toHaveTextContent('接待样本仅 10 人（<30），转化率只作趋势参考，不建议直接用于投放或考核结论。')
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

  // ── D4(2026-09-23) 下钻：可点的必须恰好是六个客户数 ──────────────────────

  /** 取某个客户数格子（role=button 的容器）；比率/会话/消息格不是 button，天然不命中。 */
  function tile(name: string) {
    const el = screen.getAllByRole('button').find((x) => (x.textContent || '').includes(name))
    if (!el) throw new Error(`未找到可下钻格子：${name}`)
    return el as HTMLElement
  }

  it('六个客户数格子可点且各自回带正确 metric', async () => {
    const onDrill = vi.fn()
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard onDrill={onDrill} />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())

    // 等值锁：可点格子数必须恰好 6——多一个是"把比率当客户数"，少一个是名单缺一角
    expect(screen.getAllByRole('button')).toHaveLength(6)

    const cases: Array<[string, string]> = [
      ['AI 独立接待客户', 'ai_served'],
      ['人工参与客户', 'human_served'],
      ['AI 留资', 'ai_lead'],
      ['人工留资（对照）', 'assisted_lead'],
      ['AI 到店', 'ai_arrived'],
      ['AI 成交', 'ai_ordered'],
    ]
    for (const [label, metric] of cases) {
      onDrill.mockClear()
      fireEvent.click(tile(label))
      expect(onDrill).toHaveBeenCalledTimes(1)
      expect(onDrill).toHaveBeenCalledWith({ metric, days: 30 })
    }
  })

  it('比率与会话/消息量不可下钻（单位不是客户，点了就对不上数）', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard onDrill={vi.fn()} />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    for (const label of ['AI 独立接待占比', '人机切换率', 'AI 留资转化率', '人工留资转化率', 'AI 消息占比']) {
      expect(screen.getByText(label).closest('[role="button"]')).toBeNull()
    }
  })

  it('下钻带的是当前窗口：切到近 7 天后点数字，days 必须跟着变', async () => {
    const onDrill = vi.fn()
    authMock.mockResolvedValue({ code: 0, data: { ...OK_PAYLOAD, period_days: 7 } })
    render(<AIContributionCard onDrill={onDrill} />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=30'))
    fireEvent.click(screen.getByText('近 7 天'))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/ai-contribution?days=7'))

    fireEvent.click(tile('AI 留资'))
    expect(onDrill).toHaveBeenCalledWith({ metric: 'ai_lead', days: 7 })
  })

  it('没挂 onDrill 时整卡只读（不做点了没反应的假可点）', async () => {
    authMock.mockResolvedValue({ code: 0, data: OK_PAYLOAD })
    render(<AIContributionCard />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    expect(screen.queryAllByRole('button')).toHaveLength(0)
    expect(screen.queryByText(/可核对背后的客户名单/)).toBeNull()
  })

  it("后端缺字段（格子显示 '-'）时不可点，也不提示可下钻", async () => {
    const onDrill = vi.fn()
    const partial = { ...OK_PAYLOAD } as Record<string, unknown>
    delete partial.ai_ordered
    authMock.mockResolvedValue({ code: 0, data: partial })
    render(<AIContributionCard onDrill={onDrill} />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    expect(screen.getAllByRole('button')).toHaveLength(5)
    expect(screen.getByText('-').closest('[role="button"]')).toBeNull()
    expect(onDrill).not.toHaveBeenCalled()
  })
})
