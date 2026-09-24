// 商机管道 Tab 单测（商机批 · 前端批1，2026-09-24）。
//
// 这一页管的是"钱"，所以断言全部钉在四条会真赔钱的展示纪律上：
//   1. **中文名一律来自后端 config**：前端写第二套枚举，早晚出现"看板叫商务谈判、
//      下钻标题叫谈判中"；这里故意把 config 里的名字改成与码不同形，前端写死即红。
//   2. **格子字符串原样回传**：下钻 filter 必须用后端下发的 drill_filter（`stage:quoted`），
//      前端自己拼 `"stage"+s` 就是一个点不开的静默缺陷。
//   3. **金额中间步骤全是分**：输入框里的 `0.29` 必须变成整数 29 落进 body，
//      `Number('0.29')*100` 那种浮点往返一旦进了报价单就是少一分钱。
//   4. **写操作后整块回读**：合计由服务端按明细重算，拿表单去更新界面就是自欺。
// 网络层统一 mock AUTH，不发真实请求。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
import { DealsTab } from '../DealsTab'

const authMock = AUTH as ReturnType<typeof vi.fn>

// 阶段中文名故意与"码的直译"不同形（negotiating→"商务谈判"而非"谈判中"）：
// 前端一旦自己写枚举，这些断言立刻对不上。
const CONFIG = {
  stages: ['lead', 'qualified', 'quoted', 'negotiating', 'won', 'lost'],
  stage_names: { lead: '线索确认', qualified: '需求确认', quoted: '已报价', negotiating: '商务谈判', won: '成交', lost: '流失' },
  filters: ['open', 'stuck', 'won', 'lost'],
  filter_labels: { open: '在途', stuck: '停滞', won: '赢单', lost: '输单' },
  lost_reasons: { price: '价格没谈拢', competitor: '输给竞品', timeout: '客户失联' },
  quote_statuses: { draft: '草稿', sent: '已发出', accepted: '已接受', void: '已作废' },
  sources: ['manual', 'acquisition', 'ai'],
  max_amount_cents: 10000000000,
  max_quote_lines: 20,
  default_stuck_days: 7,
}

const BOARD = {
  days: 30, stuck_days: 7,
  stages: [
    { stage: 'lead', stage_name: '线索确认', count: 3, amount_cents: 0, drill_filter: 'stage:lead' },
    { stage: 'quoted', stage_name: '已报价', count: 5, amount_cents: 1234500, drill_filter: 'stage:quoted' },
    { stage: 'negotiating', stage_name: '商务谈判', count: 2, amount_cents: 880000, drill_filter: 'stage:negotiating' },
    { stage: 'won', stage_name: '成交', count: 4, amount_cents: 4500000, drill_filter: 'stage:won' },
    { stage: 'lost', stage_name: '流失', count: 1, amount_cents: 0, drill_filter: 'stage:lost' },
  ],
  open_count: 8, open_amount_cents: 2114500,
  stuck_count: 2, stuck_amount_cents: 300000,
  won_count: 4, won_amount_cents: 4500000,
  lost_count: 1, total_count: 9, win_rate_pct: 80,
  truncated: false, note: '统计窗口按商机建档时间', config: CONFIG,
}

const DEAL = {
  id: 12, customer_id: 1024, customer_name: '王女士', customer_phone: '138****0000',
  customer_journey_stage: '到店体验', owner_user_id: 3, owner_name: '李顾问',
  title: '极石 01 四驱版 · 置换', stage: 'quoted', stage_name: '已报价',
  stage_entered_at: '2026-09-20T10:00:00Z', stalled_days: 3, amount_cents: 1280050,
  source: 'acquisition', source_code: 'ABCD2345', expected_close_at: '2026-10-01',
  won_at: '', lost_at: '', lost_reason: '', lost_reason_name: '',
  quote_count: 2, open_quote: { id: 41, version: 2, status: 'draft', status_name: '草稿', total_cents: 1280050, valid_until: '', sent_at: '', decided_at: '', created_at: '2026-09-21T09:00:00Z' },
  created_at: '2026-09-18T08:00:00Z',
}

// 名单 total=12 而本页只有 1 行：横幅写条数就是"名单 1 张冒充看板 12 张"
const DRILL = { filter: 'stage:quoted', label: '已报价', days: 30, stuck_days: 7, total: 12, list: [DEAL], truncated: false, note: '与看板格子同一份谓词', page: 1, page_size: 20 }

const QUOTE_DRAFT = { id: 41, opportunity_id: 12, version: 2, status: 'draft', status_name: '草稿', total_cents: 1280050, valid_until: '', sent_at: '', decided_at: '', created_at: '2026-09-21T09:00:00Z', updated_at: '', note: '', created_by: 3, lines: [{ name: '整车', qty: 1, unit_cents: 1280000, total_cents: 1280000 }] }
const QUOTE_SENT = { ...QUOTE_DRAFT, id: 40, version: 1, status: 'sent', status_name: '已发出', sent_at: '2026-09-19T10:00:00Z' }
const DETAIL = { deal: DEAL, quotes: [QUOTE_DRAFT, QUOTE_SENT], config: CONFIG }

type RouteOpts = { board?: unknown; drill?: unknown; detail?: unknown; create?: unknown }

/** 按 URL 分流，并记录每次调用的 url + body（断言"请求真带上了筛选条件"）。 */
function route(opts: RouteOpts = {}) {
  const calls: Array<{ url: string; body?: unknown; method?: string }> = []
  authMock.mockImplementation(async (url: string, o?: { method?: string; body?: unknown }) => {
    calls.push({ url, body: o?.body, method: o?.method })
    if (url.includes('/board')) return { code: 0, data: opts.board ?? BOARD }
    if (url.startsWith('/api/v1/admin/deals?')) return { code: 0, data: opts.drill ?? DRILL }
    if (url === '/api/v1/admin/deals') return opts.create ?? { code: 0, data: { deal: DEAL, config: CONFIG } }
    if (/^\/api\/v1\/admin\/deals\/\d+\/move$/.test(url)) return { code: 0, data: { deal: { ...DEAL, stage: 'won', stage_name: '成交' }, config: CONFIG } }
    if (/^\/api\/v1\/admin\/deals\/\d+\/quotes$/.test(url)) return { code: 0, data: { quote: QUOTE_DRAFT } }
    if (/^\/api\/v1\/admin\/quotes\/\d+\/(send|accept|decline|void)$/.test(url)) return { code: 0, data: { quote: QUOTE_SENT } }
    if (/^\/api\/v1\/admin\/quotes\/\d+$/.test(url)) return { code: 0, data: { quote: QUOTE_DRAFT } }
    if (/^\/api\/v1\/admin\/deals\/\d+$/.test(url)) return { code: 0, data: opts.detail ?? DETAIL }
    return { code: 0, data: {} }
  })
  return calls
}

/** 点开 TDesign Select（jsdom 下它渲染成只读 input，必须点 input 才出浮层）。
 * 选项**只在浮层里找**：阶段名"成交/流失"在看板格子上就出现过，全页 getByText 会 strict-mode 命中多个。 */
async function pickSelect(scope: HTMLElement, triggerLabel: string, optionText: string) {
  const input = Array.from(scope.querySelectorAll<HTMLInputElement>('input.t-input__inner'))
    .find((i) => i.value === triggerLabel || i.placeholder === triggerLabel)
  expect(input).toBeTruthy()
  fireEvent.click(input as HTMLInputElement)
  const opt = await waitFor(() => {
    const el = Array.from(document.querySelectorAll<HTMLElement>('.t-select-option'))
      .find((o) => (o.textContent || '').trim() === optionText)
    expect(el).toBeTruthy()
    return el as HTMLElement
  })
  fireEvent.click(opt)
}

/** 从看板某格展开名单并等横幅出现。 */
async function drillInto(filter: string) {
  fireEvent.click(await screen.findByTestId(`deal-cell-${filter}`))
  await screen.findByTestId('deal-drill-total')
}

/** 打开商机详情弹窗（走列表行的「详情」按钮）。 */
async function openDetail() {
  fireEvent.click(screen.getByRole('button', { name: '详情' }))
  await screen.findByText(/报价版本链/)
}

beforeEach(() => { authMock.mockReset() })

describe('DealsTab', () => {
  it('首屏窗口与停滞阈值进了请求，格子标签与金额全部来自后端', async () => {
    route()
    render(<DealsTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/deals/board?days=30&stuck_days=7'))
    // 阶段名取 config.stage_names：写死"谈判中"就找不到"商务谈判"
    expect(await screen.findByText('商务谈判')).toBeTruthy()
    expect(screen.getByText('在途')).toBeTruthy()
    // 1234500 分 = 12,345.00 元（千分位、两位小数，不做浮点）
    expect(screen.getByText('¥ 12,345.00')).toBeTruthy()
    expect(screen.getByText('赢单率')).toBeTruthy()
    expect(screen.getByText('80%')).toBeTruthy()
  })

  it('换窗口后重新取数，下钻带的是当前窗口而不是首屏的 30 天', async () => {
    const calls = route()
    render(<DealsTab />)
    await screen.findByTestId('deal-cell-open')
    await pickSelect(document.body, '近 30 天', '近 90 天')
    await waitFor(() => expect(calls.some((c) => c.url.includes('/board?days=90'))).toBe(true))
    await drillInto('stage:quoted')
    expect(calls.some((c) => c.url.includes('filter=stage%3Aquoted') && c.url.includes('days=90') && c.url.includes('stuck_days=7'))).toBe(true)
  })

  it('横幅数字逐字用后端 total（本页只有 1 行，写条数就是 12 变 1）', async () => {
    route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    expect(screen.getByTestId('deal-drill-total').textContent).toBe('12')
    expect(screen.getByText('极石 01 四驱版 · 置换')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /返回看板/ }))
    await waitFor(() => expect(screen.queryByTestId('deal-drill-total')).toBeNull())
  })

  it('后端标了 truncated 就必须把"数字偏小"说在明面上', async () => {
    route({ board: { ...BOARD, truncated: true } })
    const { unmount } = render(<DealsTab />)
    expect(await screen.findByText(/下面的数字比真实值小/)).toBeTruthy()
    unmount()

    route()
    render(<DealsTab />)
    await screen.findByTestId('deal-cell-open')
    expect(screen.queryByText(/下面的数字比真实值小/)).toBeNull()
  })

  it('建单：金额以「分」提交，0.29 元就是 29 分；空金额是 0 不是 null', async () => {
    const calls = route()
    render(<DealsTab />)
    await screen.findByTestId('deal-cell-open')
    fireEvent.click(screen.getByRole('button', { name: /新建商机/ }))
    fireEvent.change(screen.getByPlaceholderText('如 1024'), { target: { value: '1024' } })
    fireEvent.change(screen.getByPlaceholderText('给复盘时看的名字'), { target: { value: '  秋季置换单  ' } })
    fireEvent.change(screen.getByPlaceholderText('还没谈到钱就空着'), { target: { value: '0.29' } })
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => {
      const post = calls.find((c) => c.url === '/api/v1/admin/deals' && c.method === 'POST')
      expect(post?.body).toMatchObject({ customer_id: 1024, title: '秋季置换单', amount_cents: 29 })
    })
    // Number('0.29')*100 === 28.999999999999996：出现小数即浮点串到了提交体里
    expect(JSON.stringify((postOf(calls)?.body))).not.toMatch(/"amount_cents":\s*\d+\./)
  })

  it('建单缺客户 ID 不发请求；撞「已有一张在途单」直接把原单开出来', async () => {
    const calls = route()
    render(<DealsTab />)
    await screen.findByTestId('deal-cell-open')
    fireEvent.click(screen.getByRole('button', { name: /新建商机/ }))
    fireEvent.change(screen.getByPlaceholderText('给复盘时看的名字'), { target: { value: '无客户名的单' } })
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }))
    expect(await screen.findByText(/请填写正确的客户 ID/)).toBeTruthy()
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/admin/deals')).toBe(false)

    // 第二路：后端拒 deal_already_open —— 页面不该留一张红字报错的窗
    route({ create: { code: 409, reason: 'deal_already_open', message: '该客户已有在途商机 #12' } })
    fireEvent.click(screen.getByRole('button', { name: /新建商机/ }))
    fireEvent.change(screen.getByPlaceholderText('如 1024'), { target: { value: '1024' } })
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => expect(screen.queryByText('新建商机')).toBeNull())
    // 提示走 toast 而不是窗内红字（窗已关，红字无处可留）。
    // 这里**不能**断"这段文案不在文档里"——toast 恰好在屏上挂着，3s 后才自己消失，
    // 那样写等于把"该有的提示"当成缺陷来断言，且快慢机上结果不一致（首次跑就是它给的假红）。
    expect(await screen.findByText(/该客户已有在途商机/)).toBeTruthy()
  })

  it('详情每次写操作后整块回读（界面不拿表单自更新）', async () => {
    const calls = route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    await openDetail()
    const before = calls.filter((c) => c.url === '/api/v1/admin/deals/12').length
    expect(before).toBeGreaterThanOrEqual(1)

    await pickSelect(document.querySelector('.t-dialog') as HTMLElement, '选择目标阶段', '成交')
    fireEvent.change(screen.getByPlaceholderText('必填'), { target: { value: '12800.50' } })
    fireEvent.click(screen.getByRole('button', { name: /推\s*进/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/admin/deals/12/move')).toBe(true))
    await waitFor(() => expect(calls.filter((c) => c.url === '/api/v1/admin/deals/12').length).toBeGreaterThan(before))
    const body = calls.find((c) => c.url === '/api/v1/admin/deals/12/move')?.body as Record<string, unknown>
    expect(body).toMatchObject({ to: 'won', amount_cents: 1280050, change_amount: true })
  })

  it('标成交必须带金额、标流失必须选原因，缺一样请求根本不发', async () => {
    const calls = route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    await openDetail()
    const dlg = document.querySelector('.t-dialog') as HTMLElement
    await pickSelect(dlg, '选择目标阶段', '成交')
    fireEvent.click(screen.getByRole('button', { name: /推\s*进/ }))
    expect(await screen.findByText(/标记成交必须填成交金额/)).toBeTruthy()

    await pickSelect(dlg, '选择目标阶段', '流失')
    fireEvent.click(screen.getByRole('button', { name: /推\s*进/ }))
    expect(await screen.findByText(/标记流失必须选一个原因/)).toBeTruthy()
    expect(calls.some((c) => c.url === '/api/v1/admin/deals/12/move')).toBe(false)
  })

  it('「客户已接受」不等于成交：接受之后不会自动把单子推进到 won', async () => {
    const calls = route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    await openDetail()
    expect(screen.getByText(/不等于成交/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /客户已接受/ }))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/admin/quotes/40/accept')).toBe(true))
    expect(screen.queryByText(/报价已发出并锁死/)).toBeNull()
    expect(calls.some((c) => c.url === '/api/v1/admin/deals/12/move')).toBe(false)
  })

  it('已发出的报价不给「编辑」入口，只有草稿能改', async () => {
    route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    await openDetail()
    // v2 是草稿 → 有编辑；v1 已发出 → 只有作废/拒绝，没有编辑
    expect(screen.getAllByRole('button', { name: '编辑' }).length).toBe(1)
    expect(screen.getByRole('button', { name: '发出' })).toBeTruthy()
    expect(screen.getByRole('button', { name: '作废' })).toBeTruthy()
  })

  it('报价明细以分回显、提交带 set_valid 显式声明有效期是否改动', async () => {
    const calls = route()
    render(<DealsTab />)
    await drillInto('stage:quoted')
    await openDetail()
    fireEvent.click(screen.getByRole('button', { name: '编辑' }))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/admin/quotes/41')).toBe(true))
    // 草稿回填：1280000 分 → "12800.00"，不是浮点串出来的 12800 / 12799.999
    const unit = await screen.findByDisplayValue('12800.00')
    expect(unit).toBeTruthy()
    fireEvent.change(unit, { target: { value: '0.10' } })
    const qty = screen.getByDisplayValue('1')
    fireEvent.change(qty, { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: /存\s*草\s*稿/ }))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/admin/quotes/41')
      expect(put?.body).toMatchObject({
        lines: [{ name: '整车', qty: 3, unit_cents: 10 }],
        set_valid: false,
        send: false,
      })
    })
  })

  it('看板加载失败要显式可见，不能伪装成"一格都没有"', async () => {
    authMock.mockResolvedValue({ code: 500, message: 'boom' })
    render(<DealsTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalled())
    expect(screen.queryByTestId('deal-cell-open')).toBeNull()
    expect(screen.getByText(/点上面任意一格/)).toBeTruthy()
  })
})

/** 取最后一次建单调用（前面的失败分支不该有 POST，见上一用例）。 */
function postOf(calls: Array<{ url: string; method?: string; body?: unknown }>) {
  return calls.filter((c) => c.url === '/api/v1/admin/deals' && c.method === 'POST').pop()
}
