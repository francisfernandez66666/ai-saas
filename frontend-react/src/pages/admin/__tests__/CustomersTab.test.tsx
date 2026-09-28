// F10/T6 客户线索增强页冒烟测试：列表、详情、编辑/打标入口。
// D4(2026-09-23) 追加下钻态冒烟：请求换真相源、口径横幅随行、前端不得再叠筛选。
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
    // FIX-H：把内联 fixture 提成具名常量，断言期望值从字段推导（不抄字面量）
    const LEAD = { id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', assigned_user_name: '销售' }
    const DETAIL = { customer: { id: 1, name: '张三', phone: '13800001111', journey_stage: 'lead_captured', city: '北京', intent_score: 0.82, assigned_user_id: 2, status: 1 }, tags: [{ id: 11, tag_name: '高意向' }], conversations: [] }
    fetchMock.mockImplementation(async (url: string) => {
      const data = url.includes('/advisor/customers')
        ? { code: 0, data: { list: [LEAD], total: 1 } }
        : { code: 0, data: DETAIL }
      return { ok: true, status: 200, json: async () => data }
    })
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/admin/tags')) return { code: 0, data: { list: [{ id: 1, name: '高意向', code: 'high_intent' }], total: 1 } }
      if (url.includes('/org/users')) return { code: 0, data: [{ id: 2, username: 'sales1', real_name: '销售' }] }
      return { code: 0, data: [] }
    })
    render(<CustomersTab />)
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining('/advisor/customers')))
    // 原来弱在哪：truthy 只证明页面上有"张三"三个字，蹭进任何重名文案都算过；
    // 现在钉什么：姓名↔阶段↔归属人同排（行头 div 直挂三个 span，CustomersTab.tsx:366-369；
    // '已留资' = STAGE_LABELS.lead_captured 的映射，:13）。
    const nameEl = await screen.findByText(LEAD.name)
    expect(nameEl.parentElement).toHaveTextContent(`${LEAD.name}已留资${LEAD.assigned_user_name}`)
    fireEvent.click(screen.getByRole('button', { name: '详情' }))
    // 原来弱在哪：truthy 只证明"编辑资料"字样出现，抽屉数据没加载也能过；
    // 现在钉什么：按钮确在详情抽屉（.t-drawer）里，且抽屉按 fixture 渲染了详情字段——
    // 姓名↔张三、意向分↔82% 各自同格（:411/:415，'82%' 按组件公式
    // ((intent_score*100).toFixed(0) 从 DETAIL.customer.intent_score 现推，不抄读数）。
    const editBtn = await screen.findByRole('button', { name: '编辑资料' })
    expect(editBtn.closest('.t-drawer')).not.toBeNull()
    const drawerEl = editBtn.closest('.t-drawer') as HTMLElement
    const drawerNameCell = within(drawerEl).getByText(DETAIL.customer.name).parentElement as HTMLElement
    expect(drawerNameCell).toHaveTextContent(`姓名${DETAIL.customer.name}`)
    const intentText = `${(DETAIL.customer.intent_score * 100).toFixed(0)}%`
    const intentCell = within(drawerEl).getByText(intentText).parentElement as HTMLElement
    expect(intentCell).toHaveTextContent(`意向分${intentText}`)
    fireEvent.click(screen.getByRole('button', { name: '管理标签' }))
    // 原来弱在哪：truthy 只证明提示语在场，弹窗没开、话挂在别处都过；
    // 现在钉什么：提示是 <p> 且落在「管理标签」对话框里（.t-dialog 头部含同名标题，:484/:495）。
    const tagHint = await screen.findByText('保存后会覆盖当前客户标签；如需删除某个标签，取消勾选即可。')
    expect(tagHint.tagName).toBe('P')
    const tagDlg = tagHint.closest('.t-dialog') as HTMLElement
    expect(tagDlg).not.toBeNull()
    expect(tagDlg).toHaveTextContent('管理标签')
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

    // 原来弱在哪：四句 truthy 各钉各的字样，横幅把 label/窗口/total 拼错顺序照样过；
    // 现在钉什么：横幅整段文案按 DRILL_PAYLOAD 三字段现推（CustomersTab.tsx:331 的
    // 拼接顺序=标签→天数→人数），且 label 那个 span 确实嵌在横幅段落里（不是别处蹭到）。
    const bannerP = await screen.findByText(/AI 贡献度下钻/)
    expect(bannerP).toHaveTextContent(
      `AI 贡献度下钻：${DRILL_PAYLOAD.label} · 近 ${DRILL_PAYLOAD.period_days} 天 · 共 ${DRILL_PAYLOAD.total} 位客户`,
    )
    expect(screen.getByText(DRILL_PAYLOAD.label).closest('p')).toBe(bannerP)
    // 口径说明逐字透出后端 note（:333），且与横幅同在标题区块内
    const noteEl = screen.getByText(DRILL_PAYLOAD.note)
    expect(noteEl).toBeInTheDocument()
    expect(bannerP.parentElement).toContainElement(noteEl)

    // 原来弱在哪：五行 truthy 只证明页面上散落着这些字样，串行（A 的手机配到 B 的行）也过；
    // 现在钉什么：每行的归属字段钉在同一条目容器里（行 div 类名 hover:bg-gray-50 是
    // :362 的锚），姓名→阶段→归属人→手机·意向车型→接待方·意向分逐格核对。
    const rowOf = (name: string) => within(screen.getByText(name).closest('.hover\\:bg-gray-50') as HTMLElement)
    const li = DRILL_PAYLOAD.list[0]
    const liRow = rowOf(li.name)
    expect(liRow.getByText(li.name).parentElement).toHaveTextContent(`${li.name}已留资${li.assigned_user_name}`)
    // '已留资'=STAGE_LABELS.lead_captured（:13）；手机行按 :371 模板 phone + ' · ' + 车型
    expect(liRow.getByText(`${li.phone} · ${li.interest_model}`)).toBeInTheDocument()
    // 意向分按组件公式 (intent_score*100).toFixed(0)（:379）现推，不抄 63 这个读数
    const intentLi = `${(li.intent_score * 100).toFixed(0)}%`
    expect(liRow.getByText(`意向 ${intentLi}`)).toBeInTheDocument()
    expect(liRow.getByText('AI 独立接待')).toBeInTheDocument() // served_by='ai'（:378 二分）

    // 访客占位名：原来弱在哪：getAllByText('客户').length>0，任何「客户」二字文案都算过；
    // 现在钉什么：本夹具恰有一行 访客_ 前缀（list[1]），nameOf(:27) 统一显示为「客户」
    // 恰好一处；头像取 initialOf(:29) 的首字「客」；归属/阶段/意向分同样钉在该行内。
    const v = DRILL_PAYLOAD.list[1]
    expect(screen.getAllByText('客户')).toHaveLength(1)
    const vRow = rowOf('客户')
    expect(vRow.getByText('客')).toBeInTheDocument()
    expect(vRow.getByText('客户').parentElement).toHaveTextContent('客户已下单') // '已下单'=STAGE_LABELS.ordered
    expect(vRow.getByText('人工参与接待')).toBeInTheDocument() // served_by='human'
    const intentV = `${(v.intent_score * 100).toFixed(0)}%`
    expect(vRow.getByText(`意向 ${intentV}`)).toBeInTheDocument()
  })

  it('下钻态隐藏阶段筛选与新建/导出（前端再叠一层就会和卡片数字对不上）', async () => {
    mockDrill()
    render(<CustomersTab drill={{ metric: 'ai_lead', days: 30 }} />)
    await screen.findByText(/AI 贡献度下钻/)
    expect(screen.queryByLabelText('客户阶段筛选')).toBeNull()
    expect(screen.queryByText('+ 新建线索')).toBeNull()
    expect(screen.queryByText('导出客户')).toBeNull()
    // 原来弱在哪：truthy 只证明「返回看板」按钮存在，飘在页面别处也算过；
    // 现在钉什么：它与「刷新」同处下钻横幅容器（CustomersTab.tsx:342-343，
    // 横幅是唯一带 shadow-sm 的顶栏区块），退出入口就在名单抬头而不是列表底部。
    const backBtn = screen.getByRole('button', { name: '返回看板' })
    const refreshBtn = screen.getByRole('button', { name: '刷新' })
    expect(backBtn.closest('.shadow-sm')).not.toBeNull()
    expect(backBtn.closest('.shadow-sm')).toBe(refreshBtn.closest('.shadow-sm'))
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
    // 原来弱在哪：/第 1\/3 页/ 是硬编码正则，total 或页大小一改它悄悄失真也过；
    // 现在钉什么：页数按 many.total / 20（DRILL_PAGE_SIZE=20，:22）现推=3，横幅副行
    // 整段（窗口日期取自 payload.since/until 前 10 字符 + 页码，:336-337）逐字回环；
    // 底部翻页条「第 1 / 3 页」（:396，空格形态与横幅不同）同时在场且上一页置灰。
    const pageCount = Math.ceil(many.total / 20)
    expect(pageCount).toBe(3)
    expect(
      screen.getByText(`窗口 ${many.since.slice(0, 10)} ~ ${many.until.slice(0, 10)} · 第 ${many.page}/${pageCount} 页`),
    ).toBeInTheDocument()
    expect(screen.getByText(`第 ${many.page} / ${pageCount} 页`)).toBeInTheDocument()
    // 上一页置灰：TDesign 禁用态把 <button> 换成 div.t-is-disabled（role=button 消失，
    // getByRole 会「找不到」而误判成控件缺失），故按类名路径钉禁用状态。
    const prevBtn = screen.getByText('上一页').closest('.t-button') as HTMLElement
    expect(prevBtn.classList.contains('t-is-disabled')).toBe(true)

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
    // 原来弱在哪：truthy 只证明有这句提示，且正则只截半句；
    // 现在钉什么：整句逐字（:360 下钻空态专用文案），且不是普通态的「暂无客户线索」
    // ——两条空态文案不能互串（串了说明 inDrill 判定没生效）。
    expect(await screen.findByText('该窗口内没有命中这个指标的客户')).toBeInTheDocument()
    expect(screen.queryByText('暂无客户线索')).toBeNull()
    expect(screen.queryByText('李四')).toBeNull()
  })
})
