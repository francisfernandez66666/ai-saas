// 获客活码 Tab 单测（获客批 · 批次4，2026-09-23）。
//
// 这一页是"钱从哪个渠道来"的唯一后台入口，断言集中在四条会真出事的展示纪律上：
//   1. 窗口与筛选**必须真进了请求**——切到 90 天还发 days=30，数字看着变了其实没变；
//   2. 漏斗格子的标签、顺序、数字全部来自后端（config.metrics/labels + row.funnel），
//      前端写死五格就会在新增指标那天悄悄少一格；
//   3. 下钻用**数字 ID**（路由 :id 是 ParseUint）——用短码发过去恒 400，页面看起来"点了没反应"；
//   4. 横幅 total 逐字取后端值，前端绝不用本页条数顶替（那样"名单 20 条 / 卡片 27 人"就看不见了）。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { apiFetch, AUTH } from '../../../lib/api'
import { AcquisitionTab } from '../AcquisitionTab'

const authMock = AUTH as ReturnType<typeof vi.fn>
const fetchMock = apiFetch as ReturnType<typeof vi.fn>

const CONFIG = {
  channels: ['抖音', '小红书', '微信', '百度', '门店自然', '老客转介绍', '其它'],
  metrics: ['new', 'spoke', 'lead', 'arrived', 'ordered'],
  labels: { new: '新增客户', spoke: '开过口', lead: '已留资', arrived: '已到店', ordered: '已成交' },
  link_base_configured: true,
}

// funnel 五格刻意摆成"递减但不互不相同"（ordered 也算到店），前端不得自己重算口径
const ROW = {
  id: 7, code: 'ABCD2345', name: '门店前台立牌', channel: '门店自然', status: 'active',
  remark: '', owner_user_id: 3, created_at: '2026-09-20T10:00:00Z',
  link: 'https://acme.example.com/client?code=ABCD2345', scans: 12,
  funnel: { new: 4, spoke: 2, lead: 3, arrived: 2, ordered: 1 },
}

const LIST_OK = { list: [ROW], total: 1, days: 30, config: CONFIG }

// 下钻：total=27 而本页只有 1 行，用来抓"横幅写条数"这种张冠李戴
const DRILL_OK = {
  metric: 'arrived', label: '已到店', total: 27, page: 1, page_size: 20,
  list: [{ id: 88, name: '访客_1234', phone: '138****0000', journey_stage: '已到店', intent_score: 0.7, spoke: true, created_at: '2026-09-21 11:00' }],
  note: '时间窗打在客户创建时间上',
}

/** 按 URL 分流（列表 / 下钻 / 状态），并记录每次调用的 url + body。 */
function route(opts: { list?: unknown; drill?: unknown } = {}) {
  const calls: Array<{ url: string; body: unknown }> = []
  authMock.mockImplementation(async (url: string, o?: { method?: string; body?: unknown }) => {
    calls.push({ url, body: o?.body })
    if (url.includes('/customers')) return { code: 0, data: opts.drill ?? DRILL_OK }
    if (url.includes('/status')) return { code: 0, data: { id: 7, status: 'disabled' } }
    if (o?.method === 'POST') return { code: 0, data: { ...ROW, id: 8, code: 'ZZZZ1111', funnel: { new: 0, spoke: 0, lead: 0, arrived: 0, ordered: 0 }, scans: 0 } }
    const d = opts.list ?? LIST_OK
    return { code: 0, data: d }
  })
  return calls
}

beforeEach(() => {
  authMock.mockReset()
  fetchMock.mockReset()
  fetchMock.mockResolvedValue({ ok: true, blob: async () => new Blob(['x']) })
})

describe('AcquisitionTab', () => {
  it('首屏带 days=30 拉列表，五格标签与数字全部来自后端', async () => {
    route()
    render(<AcquisitionTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/acquisition/codes?days=30&status='))
    expect(await screen.findByText('门店前台立牌')).toBeTruthy()
    // 五格按 config.metrics 顺序渲染，标签取 config.labels
    const cells = Array.from(document.querySelectorAll('[role="button"]')).filter((el) => (el.textContent || '').includes('新增客户'))
    expect(cells.length).toBe(1)
    expect(document.body.textContent).toContain('已留资')
    expect(screen.getByText('扫码', { selector: '*' })).toBeTruthy() // 表头有扫码列
    expect(document.body.textContent).toContain('ABCD2345')
  })

  it('扫码格不可点：可点击的只有五个客户级指标', async () => {
    route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    const drillable = Array.from(document.querySelectorAll('[role="button"]'))
      .filter((el) => /[1-9]/.test((el.textContent || '').replace(/\D/g, '') || ''))
    // 一码五格：多出来的 role=button 说明有人把"次"当"人"挂上了点击
    expect(drillable.length).toBe(5)
    expect(drillable.some((el) => (el.textContent || '').includes('扫码'))).toBe(false)
  })

  it('切窗口后重新请求，且下钻带的是当前窗口', async () => {
    const calls = route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    // 换窗口：TDesign Select 在 jsdom 下渲染成只读 input（value 即展示文案），
    // 点开它才有浮层里的 option——直接 getByText('近 30 天') 命中的是输入框的值，不是文本节点。
    const daysInput = Array.from(document.querySelectorAll<HTMLInputElement>('input.t-input__inner'))
      .find((i) => i.value === '近 30 天')
    expect(daysInput).toBeTruthy()
    fireEvent.click(daysInput as HTMLInputElement)
    await waitFor(() => expect(document.querySelectorAll('.t-select-option').length).toBeGreaterThan(0))
    fireEvent.click(screen.getByText('近 90 天'))
    await waitFor(() => expect(calls.some((c) => c.url.includes('days=90'))).toBe(true))

    fireEvent.click(screen.getByText('已到店'))
    await waitFor(() => expect(calls.some((c) => c.url.includes('/customers') && c.url.includes('metric=arrived') && c.url.includes('days=90'))).toBe(true))
  })

  it('下钻用数字 ID 且横幅逐字用后端 total（不是本页条数）', async () => {
    const calls = route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    fireEvent.click(screen.getByText('已到店'))
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/admin/acquisition/codes/7/customers?metric=arrived&days=30&page=1&page_size=20')).toBe(true))
    const banner = await screen.findByTestId('acq-drill-total')
    expect(banner.textContent).toBe('27') // 本页只有 1 行，写成 1 就是"名单条数冒充命中数"
    expect(screen.getByText('访客_1234')).toBeTruthy()
    expect(screen.getByText('138****0000')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: /返回活码列表/ }))
    await waitFor(() => expect(screen.queryByTestId('acq-drill-total')).toBeNull())
  })

  it('基址没配好时先说清楚"印出去是废码"，配好了不再啰嗦', async () => {
    route({ list: { ...LIST_OK, config: { ...CONFIG, link_base_configured: false } } })
    const { unmount } = render(<AcquisitionTab />)
    expect(await screen.findByText(/落地基址尚未配置/)).toBeTruthy()
    unmount()

    route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    expect(screen.queryByText(/落地基址尚未配置/)).toBeNull()
  })

  it('建码提交去掉首尾空格，渠道下拉用后端下发的枚举', async () => {
    const calls = route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    fireEvent.click(screen.getByRole('button', { name: /新建活码/ }))
    fireEvent.change(screen.getByPlaceholderText('给运营自己看的名字'), { target: { value: '  春季车展海报  ' } })
    // 渠道下拉：**必须限定在弹窗内找**——TDesign 的 select 永远带 placeholder="请选择"，
    // 全页第一个命中的是筛选行的"统计窗口"，点它开出来的是天数选项（首跑就这么假失败）。
    const dlg = document.querySelector('.t-dialog') as HTMLElement
    expect(dlg).toBeTruthy()
    const chInput = dlg.querySelector('.t-select input.t-input__inner')
    expect(chInput).toBeTruthy()
    fireEvent.click(chInput as HTMLInputElement)
    await waitFor(() => expect(document.querySelectorAll('.t-select-option').length).toBeGreaterThan(0))
    expect(screen.getAllByText('老客转介绍').length).toBeGreaterThan(0)
    fireEvent.click(screen.getByText('抖音'))
    fireEvent.click(screen.getByRole('button', { name: /创\s*建/ }))
    await waitFor(() => {
      const post = calls.find((c) => c.url === '/api/v1/admin/acquisition/codes')
      expect(post?.body).toMatchObject({ name: '春季车展海报', channel: '抖音' })
    })
  })

  it('停用发 {active:false} 并回读列表', async () => {
    const calls = route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    fireEvent.click(screen.getByRole('button', { name: '停用' }))
    await waitFor(() => {
      const patch = calls.find((c) => c.url.endsWith('/status'))
      expect(patch?.url).toBe('/api/v1/admin/acquisition/codes/7/status')
      expect(patch?.body).toEqual({ active: false })
    })
  })

  it('二维码走数字 ID 端点（图片要带登录态，不能用 <img src> 裸链）', async () => {
    route()
    render(<AcquisitionTab />)
    await screen.findByText('门店前台立牌')
    fireEvent.click(screen.getByRole('button', { name: '二维码' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/admin/acquisition/codes/7/qr.png?size=400'))
  })

  it('落地链接原样用后端下发的 link，前端不自己拼域名', async () => {
    route()
    render(<AcquisitionTab />)
    const a = await screen.findByTitle('https://acme.example.com/client?code=ABCD2345')
    expect(a.getAttribute('href')).toBe('https://acme.example.com/client?code=ABCD2345')
  })

  it('空列表出空态；缺 config 的半截载荷不得把页面带崩', async () => {
    route({ list: { list: [], total: 0, days: 30 } })
    render(<AcquisitionTab />)
    expect(await screen.findByText(/还没有活码/)).toBeTruthy()
    expect(screen.queryByText('已到店')).toBeNull() // 没 config 就不该凭空长出五格
  })

  it('列表读失败要显式可见，不能伪装成"一张码都没有"', async () => {
    authMock.mockResolvedValue({ code: 500, message: 'boom' })
    render(<AcquisitionTab />)
    await waitFor(() => expect(screen.getByText(/还没有活码/)).toBeTruthy())
    expect(screen.queryByText('门店前台立牌')).toBeNull()
  })
})
