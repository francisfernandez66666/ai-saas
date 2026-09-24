// 顾问台商机卡单测（商机批 · 前端批3，2026-09-24）。
//
// 这张卡是顾问每天点的三个动作（开单 / 推进 / 出报价），断言集中在四条会真出事的纪律上：
//   1. **建单不传 source**——来源是归因事实、由后端按活码判定，表单里出现 source 键就是前端越权替客户编归因；
//   2. **金额以元入表、以分提交**——表单里传出去的是用户输入的那个字符串，转换只在父组件一处发生；
//   3. **终局单只读**——流失/成交单仍要列出来（防重复开单），但绝不能给"推进/出报价"入口，
//      点下去必吃 deal_closed，而顾问看到的是"页面报错了"；
//   4. **后端拒绝要说人话**——reason 翻成中文话术留在表单上，撞"已有在途单"这种"父组件已替你处理"
//      的（dismiss）要把窗关掉，别留一张填了一半的表单让人再撞一次。
//
// 组件是纯展示 + 异步回调（写请求在 pages/Advisor.tsx），所以这里不 mock AUTH，
// 只用 vi.fn() 收表单——测的是这张卡**交出去什么**，不是它请求了几次。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import DealCard, { type DealCreateForm, type DealMoveForm, type DealQuoteForm, type DealWriteResult } from '../DealCard'
import type { DealConfig, DealRow } from '../../../types'

// CFG 完全照后端 dealBoardConfig() 的形态：阶段码与中文名刻意不同形
// （"quoted" vs "已报价"），前端若自己拼阶段名就会在这里露出来
const CFG: DealConfig = {
  stages: ['lead', 'qualified', 'quoted', 'negotiating', 'won', 'lost'],
  stage_names: { lead: '线索确认', qualified: '需求确认', quoted: '已报价', negotiating: '商务谈判', won: '成交', lost: '流失' },
  filters: ['open', 'stuck', 'won', 'lost'],
  filter_labels: { open: '在途', stuck: '停滞', won: '赢单', lost: '输单' },
  lost_reasons: { price: '价格没谈拢', competitor: '输给竞品', other: '其它' },
  quote_statuses: { draft: '草稿', sent: '已发出', accepted: '已接受', declined: '已拒绝', superseded: '已被新版取代', void: '已作废', expired: '已过期' },
  sources: ['manual', 'acquisition', 'ai'],
  max_amount_cents: 500000000,
  max_quote_lines: 20,
  default_stuck_days: 7,
}

// row 造一行商机：默认一张停在「已报价」的在途单，用例只覆盖自己关心的字段
function row(over: Partial<DealRow> = {}): DealRow {
  return {
    id: 501, customer_id: 1024, customer_name: '王女士', customer_phone: '138****0001',
    customer_journey_stage: 'arrived', owner_user_id: 7, owner_name: 'sales1',
    title: '极石 01 四驱版 · 置换', stage: 'quoted', stage_name: '已报价',
    stage_entered_at: '2026-09-18T10:00:00+08:00', stalled_days: 4, amount_cents: 2114500,
    source: 'acquisition', source_code: 'AC123456', expected_close_at: '', won_at: '', lost_at: '',
    lost_reason: '', lost_reason_name: '', quote_count: 1,
    open_quote: { id: 88, version: 2, status: 'draft', status_name: '草稿', total_cents: 1990000, valid_until: '2026-10-01T00:00:00+08:00', sent_at: '', decided_at: '', created_at: '2026-09-20T09:00:00+08:00' },
    created_at: '2026-09-10T09:00:00+08:00',
    ...over,
  }
}

const OK: DealWriteResult = { ok: true, message: '' }

// 打开「开一张商机」弹窗
function openCreate() {
  fireEvent.click(screen.getByTestId('deal-open-btn'))
}

describe('DealCard 顾问台商机卡', () => {
  it('一张单都没有时给开单入口，并留一句为什么要开单', () => {
    render(<DealCard deals={[]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    expect(screen.getByTestId('deal-open-btn')).toBeTruthy()
    expect(screen.getByText(/还没开过单/)).toBeTruthy()
    // 反证：没有单子就不能凭空长出"在途提示"或商机行
    expect(screen.queryByTestId('deal-open-hint')).toBeNull()
    expect(screen.queryAllByTestId(/^deal-row-/).length).toBe(0)
  })

  it('已有在途单时收起开单入口；终局单仍列出但不给推进/出报价', () => {
    const closed = row({ id: 500, title: '上一次的置换单', stage: 'lost', stage_name: '流失', stalled_days: 0, lost_reason: 'price', lost_reason_name: '价格没谈拢', open_quote: null, quote_count: 0 })
    const live = row({})
    render(<DealCard deals={[closed, live]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    expect(screen.queryByTestId('deal-open-btn')).toBeNull()
    expect(screen.getByTestId('deal-open-hint')).toBeTruthy()
    // 终局单**照列**（藏起来他就会重复开一张一样的单），但两个写入口都不给
    expect(screen.getByTestId('deal-row-500')).toBeTruthy()
    expect(screen.queryByTestId('deal-move-500')).toBeNull()
    expect(screen.queryByTestId('deal-newquote-500')).toBeNull()
    // 在途单两个入口都在
    expect(screen.getByTestId('deal-move-501')).toBeTruthy()
    expect(screen.getByTestId('deal-newquote-501')).toBeTruthy()
  })

  it('金额按元展示（千分位），终局单不显示停滞天数，流失原因读后端中文名', () => {
    const closed = row({ id: 502, stage: 'lost', stage_name: '流失', stalled_days: 0, lost_reason: 'price', lost_reason_name: '价格没谈拢', open_quote: null })
    render(<DealCard deals={[row({}), closed]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    const liveText = screen.getByTestId('deal-row-501').textContent || ''
    expect(liveText).toContain('21,145.00')
    expect(liveText).toContain('停 4 天')
    const closedText = screen.getByTestId('deal-row-502').textContent || ''
    expect(closedText).not.toContain('停 0 天')
    expect(closedText).toContain('价格没谈拢')
  })

  it('当前活报价直接读 open_quote（版本/状态/合计都不在前端算）', () => {
    render(<DealCard deals={[row({})]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    const t = screen.getByTestId('deal-quote-501').textContent || ''
    expect(t).toContain('v2')
    expect(t).toContain('草稿')
    expect(t).toContain('19,900.00')
    expect(t).toContain('2026-10-01')
    // 按钮文案跟着"有没有活报价"走：有一版草稿时再点就是另出一版
    expect(screen.getByTestId('deal-newquote-501').textContent).toBe('另出一版报价')
  })

  it('开单交出去的表单：不含 source、金额保持用户输入的元字符串', async () => {
    const onCreate = vi.fn().mockResolvedValue(OK)
    render(<DealCard deals={[]} cfg={CFG} onCreate={onCreate} onMove={vi.fn()} onQuote={vi.fn()} />)
    openCreate()
    fireEvent.change(screen.getByPlaceholderText('如 极石 01 四驱版 · 置换'), { target: { value: '秋季置换单' } })
    fireEvent.change(screen.getByPlaceholderText('还没谈到钱就空着'), { target: { value: '0.29' } })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(onCreate).toHaveBeenCalledTimes(1))
    const form = onCreate.mock.calls[0][0] as DealCreateForm
    expect(form.title).toBe('秋季置换单')
    expect(form.amount_yuan).toBe('0.29') // 转分只发生在父组件那一处
    expect(form.stage).toBe('lead') // 起始阶段默认取在途序列第一格，不是终局两态
    expect('source' in form).toBe(false)
    await waitFor(() => expect(screen.queryByText('开一张商机')).toBeNull())
  })

  it('开单：标题空与金额三位小数都不出卡（不发请求），错误话术留在表单上', async () => {
    const onCreate = vi.fn()
    render(<DealCard deals={[]} cfg={CFG} onCreate={onCreate} onMove={vi.fn()} onQuote={vi.fn()} />)
    openCreate()
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('起个名字')
    expect(onCreate).not.toHaveBeenCalled()

    fireEvent.change(screen.getByPlaceholderText('如 极石 01 四驱版 · 置换'), { target: { value: '秋季置换单' } })
    fireEvent.change(screen.getByPlaceholderText('还没谈到钱就空着'), { target: { value: '1.234' } })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('两位小数')
    expect(onCreate).not.toHaveBeenCalled()
  })

  it('开单被后端拒绝：话术留在窗里；父组件已处理（dismiss）时直接关窗', async () => {
    const onCreate = vi.fn().mockResolvedValue({ ok: false, message: '阶段不能往回退', dismiss: false })
    render(<DealCard deals={[]} cfg={CFG} onCreate={onCreate} onMove={vi.fn()} onQuote={vi.fn()} />)
    openCreate()
    fireEvent.change(screen.getByPlaceholderText('如 极石 01 四驱版 · 置换'), { target: { value: '秋季置换单' } })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(screen.getByTestId('deal-form-err').textContent).toBe('阶段不能往回退'))
    expect(screen.getByText('开一张商机')).toBeTruthy() // 窗还开着，允许改完再交

    onCreate.mockResolvedValue({ ok: false, message: '已有在途单', dismiss: true })
    fireEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(screen.queryByText('开一张商机')).toBeNull())
  })

  it('推进到成交不填金额 → 拦在卡内；推进到流失不选原因同样拦', async () => {
    const onMove = vi.fn().mockResolvedValue(OK)
    render(<DealCard deals={[row({})]} cfg={CFG} onCreate={vi.fn()} onMove={onMove} onQuote={vi.fn()} />)
    fireEvent.click(screen.getByTestId('deal-move-501'))

    const selects = () => Array.from(document.querySelectorAll('select'))
    fireEvent.change(selects()[0], { target: { value: 'won' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('成交金额')
    expect(onMove).not.toHaveBeenCalled()

    fireEvent.change(selects()[0], { target: { value: 'lost' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('流失')
    expect(onMove).not.toHaveBeenCalled()

    // 选了原因才交出去，交的是原因码不是中文名
    fireEvent.change(selects()[1], { target: { value: 'competitor' } })
    fireEvent.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(onMove).toHaveBeenCalledTimes(1))
    expect(onMove.mock.calls[0][0]).toBe(501)
    expect((onMove.mock.calls[0][1] as DealMoveForm).lost_reason).toBe('competitor')
  })

  it('推进目标阶段列表不含当前阶段，但也不自己算顺序（回退由后端判）', () => {
    render(<DealCard deals={[row({})]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    fireEvent.click(screen.getByTestId('deal-move-501'))
    const opts = Array.from(document.querySelectorAll('select')[0].options).map((o) => o.value).filter(Boolean)
    expect(opts).not.toContain('quoted') // 原地移动会白刷停滞天数，后端必拒，索性不给选
    expect(opts).toContain('lead') // 往回那格仍然列着：判据在后端（stage_backward），前端不养第二套顺序
  })

  it('报价：全空行不发；填一行后「保存并发出」交的是元字符串 + send=true', async () => {
    const onQuote = vi.fn().mockResolvedValue(OK)
    render(<DealCard deals={[row({})]} cfg={CFG} onCreate={vi.fn()} onMove={vi.fn()} onQuote={onQuote} />)
    fireEvent.click(screen.getByTestId('deal-newquote-501'))
    fireEvent.click(screen.getByRole('button', { name: '保存并发出' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('至少要有明细行')
    expect(onQuote).not.toHaveBeenCalled()

    fireEvent.change(screen.getByPlaceholderText('如 极石 01 四驱版'), { target: { value: '整车' } })
    fireEvent.change(screen.getByLabelText('单价（元）'), { target: { value: '299000.00' } })
    fireEvent.click(screen.getByRole('button', { name: '保存并发出' }))
    await waitFor(() => expect(onQuote).toHaveBeenCalledTimes(1))
    const [dealId, form, send] = onQuote.mock.calls[0] as [number, DealQuoteForm, boolean]
    expect(dealId).toBe(501)
    expect(send).toBe(true)
    expect(form.lines).toEqual([{ name: '整车', qty: '1', unit_yuan: '299000.00' }])
    await waitFor(() => expect(screen.queryByText('报价 · 极石 01 四驱版 · 置换')).toBeNull())
  })

  it('报价数量非法拦在卡内；明细行数到上限时加一行不可点', async () => {
    const onQuote = vi.fn()
    render(<DealCard deals={[row({})]} cfg={{ ...CFG, max_quote_lines: 1 }} onCreate={vi.fn()} onMove={vi.fn()} onQuote={onQuote} />)
    fireEvent.click(screen.getByTestId('deal-newquote-501'))
    fireEvent.change(screen.getByPlaceholderText('如 极石 01 四驱版'), { target: { value: '整车' } })
    fireEvent.change(screen.getByLabelText('数量'), { target: { value: '0' } })
    fireEvent.change(screen.getByLabelText('单价（元）'), { target: { value: '10' } })
    fireEvent.click(screen.getByRole('button', { name: '存草稿' }))
    expect(screen.getByTestId('deal-form-err').textContent).toContain('正整数')
    expect(onQuote).not.toHaveBeenCalled()
    // 上限 1 行：加一行必须真的禁点（不是藏起来，让人以为没这功能）
    const add = screen.getByTestId('quote-add-line') as HTMLButtonElement
    expect(add.disabled).toBe(true)
  })

  it('cfg 未就位时列表照常显示、但不给写入口（下拉无选项即无意义弹窗）', () => {
    render(<DealCard deals={[row({})]} cfg={null} onCreate={vi.fn()} onMove={vi.fn()} onQuote={vi.fn()} />)
    expect(screen.getByTestId('deal-row-501')).toBeTruthy()
    expect(screen.queryByTestId('deal-move-501')).toBeNull()
    expect(screen.queryByTestId('deal-open-btn')).toBeNull()
  })
})
