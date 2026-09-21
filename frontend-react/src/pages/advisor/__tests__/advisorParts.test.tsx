// C2 拆分(2026-09-21) 防回归：Advisor.tsx 拆成子组件后，
// 用独立渲染断言守住「props 接线没接错 / 纯函数语义没漂移」两件事。
import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { H, HI, splitTags, fmtShort, TD_STATUS_LABELS, SENDER_LABELS } from '../shared'
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
  it('HomeView 渲染客户列表与阶段徽标，点击回调回传客户 id', () => {
    let opened = 0
    render(<HomeView
      stats={[{ value: 3, label: '今日线索', color: '' }]}
      list={[{ id: 7, name: '张三', journey_stage: 'lead_captured', last_message: '在吗' }]}
      status="all"
      onStatus={() => {}}
      onOpen={(id) => { opened = id }}
    />)
    expect(screen.getByText('今日线索')).toBeTruthy()
    expect(screen.getByText('张三')).toBeTruthy()
    expect(screen.getByText('已留资')).toBeTruthy()
    expect(screen.getByText('在吗')).toBeTruthy()
    screen.getByText('张三').click()
    expect(opened).toBe(7)
  })

  it('HomeView 空列表给出空态文案', () => {
    render(<HomeView stats={[]} list={[]} status="all" onStatus={() => {}} onOpen={() => {}} />)
    expect(screen.getByText('暂无客户')).toBeTruthy()
  })

  it('FollowupView 按 next_follow_at 与当前时间切分今日/逾期', () => {
    const now = Date.now()
    render(<FollowupView
      followups={[
        { customer_id: 1, customer_name: '未来的', content: '待跟进内容', next_follow_at: new Date(now + 3600_000).toISOString() },
        { customer_id: 2, customer_name: '逾期的', content: '逾期内容', next_follow_at: new Date(now - 3600_000).toISOString() },
      ]}
      onOpen={() => {}}
    />)
    expect(screen.getByText('今日待跟进')).toBeTruthy()
    expect(screen.getByText('逾期跟进')).toBeTruthy()
    expect(screen.getByText('待跟进内容')).toBeTruthy()
    expect(screen.getByText('逾期内容')).toBeTruthy()
  })

  it('MeView 有套餐时展示三桶用量，无套餐时不崩', () => {
    const { unmount } = render(<MeView quota={{ used_ai_calls: 12, max_ai_calls: 100, ai_call_balance: 5, expired_at: '2026-12-31' }} onFeedback={() => {}} />)
    expect(screen.getByText('本月AI调用')).toBeTruthy()
    expect(screen.getByText('增量余额')).toBeTruthy()
    unmount()
    render(<MeView quota={null} onFeedback={() => {}} />)
    expect(screen.getByText('提交产品反馈')).toBeTruthy()
  })
})
