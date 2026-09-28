// C2 拆分(2026-09-21) 防回归：Advisor.tsx 拆成子组件后，
// 用独立渲染断言守住「props 接线没接错 / 纯函数语义没漂移」两件事。
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { H, HI, splitTags, fmtShort, STAGE_LABELS, TD_STATUS_LABELS, SENDER_LABELS } from '../shared'
import HomeView from '../HomeView'
import FollowupView from '../FollowupView'
import MeView from '../MeView'

describe('advisor/shared 纯函数', () => {
  it('H/HI 对匿名访客脱敏，对实名客户原样', () => {
    expect(H('访客_abc')).toBe('客户')
    expect(H(undefined)).toBe('客户')
    expect(H('张三')).toBe('张三')
    expect(HI('访客_abc')).toBe('客')
    expect(HI('张三')).toBe('张')
  })

  it('splitTags 同时兼容中英文逗号且丢弃空片段', () => {
    expect(splitTags('试驾,报价')).toEqual(['试驾', '报价'])
    expect(splitTags('试驾，报价,')).toEqual(['试驾', '报价'])
    expect(splitTags(undefined)).toEqual([])
  })

  it('TD_STATUS_LABELS / SENDER_LABELS 覆盖已知枚举', () => {
    expect(TD_STATUS_LABELS.pending).toBe('待试驾')
    expect(TD_STATUS_LABELS.completed).toBe('已完成')
    expect(TD_STATUS_LABELS.cancelled).toBe('已取消')
    expect(SENDER_LABELS.human).toBe('顾问')
    expect(SENDER_LABELS.ai).toBe('AI')
    expect(SENDER_LABELS.customer).toBe('客户')
  })

  it('fmtShort 空值返回空串（不渲染 Invalid Date）', () => {
    expect(fmtShort(undefined)).toBe('')
    expect(fmtShort(null)).toBe('')
  })
})

describe('advisor 子组件接线', () => {
  const STATS = [{ value: 3, label: '今日线索', color: '' }]
  const LEAD = { id: 7, name: '张三', journey_stage: 'lead_captured', last_message: '在吗' }

  it('HomeView 渲染客户列表与阶段徽标，点击回调回传客户 id', () => {
    let opened = 0
    render(<HomeView
      stats={STATS}
      list={[LEAD]}
      status="all"
      onStatus={() => {}}
      onOpen={(id) => { opened = id }}
    />)
    // 原来弱在哪：truthy 只证明「今日线索」几个字在场，卡片数字没渲染也过；
    // 现在钉什么：数字与标签同格（HomeView.tsx:18 一个 div 内 <p>value</p><p>label</p>）。
    const statLabel = screen.getByText(STATS[0].label)
    expect(statLabel.closest('div')).toHaveTextContent(String(STATS[0].value))
    const nameEl = screen.getByText(LEAD.name)
    // 原来弱在哪：徽标 truthy 只证明有个「已留资」，挂到别的客户身上也过；
    // 现在钉什么：徽标文案由 shared.STAGE_LABELS[journey_stage] 现推，
    // 且与姓名同排（:27-28 flex 行）。
    expect(STAGE_LABELS[LEAD.journey_stage]).toBe('已留资')
    expect(nameEl.closest('div')).toHaveTextContent(STAGE_LABELS[LEAD.journey_stage])
    // 末条消息钉在同一行内，不是"页面上有这两个字"
    expect(nameEl.closest('div[style*="cursor"]') ?? nameEl.closest('div')).toHaveTextContent(LEAD.last_message)
    screen.getByText('张三').click()
    expect(opened).toBe(7)
  })

  it('HomeView 空列表给出空态文案', () => {
    render(<HomeView stats={[]} list={[]} status="all" onStatus={() => {}} onOpen={() => {}} />)
    // 空态是整段文案唯一来源（HomeView.tsx:24），钉精确串而非包含
    expect(screen.getByText('暂无客户')).toBeInTheDocument()
  })

  it('FollowupView 按 next_follow_at 与当前时间切分今日/逾期', () => {
    const now = Date.now()
    const TODAY = { customer_id: 1, customer_name: '未来的', content: '待跟进内容', next_follow_at: new Date(now + 3600_000).toISOString() }
    const LATE = { customer_id: 2, customer_name: '逾期的', content: '逾期内容', next_follow_at: new Date(now - 3600_000).toISOString() }
    render(<FollowupView followups={[TODAY, LATE]} onOpen={() => {}} />)
    // 原来弱在哪：四个 truthy 只证明两张小标题和两段内容都在，
    // 谁落在哪一区完全没锁——切分反了也照样绿；
    // 现在钉什么：取分区的 <h3>（FollowupView.tsx:9/11），断"未来的"条目在
    // 今日区之后、逾期区之前，逾期条目在逾期区之后，即真按时间分栏。
    const headings = Array.from(document.querySelectorAll('h3'))
    const todayH = headings.find((h) => h.textContent === '今日待跟进') as HTMLElement
    const lateH = headings.find((h) => h.textContent === '逾期跟进') as HTMLElement
    if (!todayH) throw new Error('未找到「今日待跟进」标题节点')
    if (!lateH) throw new Error('未找到「逾期跟进」标题节点')
    const rows = Array.from(document.querySelectorAll('div[style*="cursor: pointer"]'))
    const rowOf = (content: string) => rows.find((r) => (r.textContent || '').includes(content)) as HTMLElement
    const todayRow = rowOf(TODAY.content)
    const lateRow = rowOf(LATE.content)
    const order = (a: Element, b: Element) => !!(a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING)
    expect(order(todayH, todayRow)).toBe(true)
    expect(order(todayRow, lateH)).toBe(true)
    expect(order(lateH, lateRow)).toBe(true)
    // 两段内容各自带客户名（同一条目行内），不是孤零零的一截文案
    expect(todayRow).toHaveTextContent(TODAY.customer_name)
    expect(lateRow).toHaveTextContent(LATE.customer_name)
  })

  it('MeView 有套餐时展示三桶用量，无套餐时不崩', () => {
    const QUOTA = { used_ai_calls: 12, max_ai_calls: 100, ai_call_balance: 5, expired_at: '2026-12-31' }
    const { unmount } = render(<MeView quota={QUOTA} onFeedback={() => {}} />)
    // 原来弱在哪：两句 truthy 只证明标签字样在场，数字没回填也过；
    // 现在钉什么：用量按 `${used}/${max}` 精确回环、余额精确到数字，
    // 且各自与标签同格（MeView.tsx:11-12）。
    expect(screen.getByText(`${QUOTA.used_ai_calls}/${QUOTA.max_ai_calls}`)).toBeInTheDocument()
    expect(screen.getByText('本月AI调用').closest('div')).toHaveTextContent(`${QUOTA.used_ai_calls}/${QUOTA.max_ai_calls}`)
    expect(screen.getByText('增量余额').closest('div')).toHaveTextContent(String(QUOTA.ai_call_balance))
    unmount()
    render(<MeView quota={null} onFeedback={() => {}} />)
    // quota 为 null 时整块用量不渲染，只留反馈按钮（:10 的 && 短路）
    expect(screen.queryByText('本月AI调用')).toBeNull()
    expect(screen.getByRole('button', { name: '提交产品反馈' })).toBeEnabled()
  })
})
