// 额度与账单提醒卡片单测（D3，2026-09-23）。
//
// 这张卡片讲的是"接下来会发生什么"（越档降级、到期停登录），说错的代价是纠纷，
// 所以断言集中在四条展示纪律上：
//   1. 比率与天数一律逐字用后端值——前端复算就会出现"卡片 66%、邮件说已越 80% 档"
//   2. 没有分母的指标不画进度条（余额桶画 0% 等于谎报宽裕）
//   3. 两个端点各自记失败：催缴读不到不许把额度进度一起清空（反之同理）
//   4. 通道未就绪必须明说"不会送达"，否则管理员以为有人在盯
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
import { QuotaAlertCard } from '../QuotaAlertCard'

const authMock = AUTH as ReturnType<typeof vi.fn>

/** 额度端点正常返回：pct 故意与 used/max 的整除值不一致，用来抓前端复算。 */
const USAGE_OK = {
  period_key: '2026-09',
  config: { enabled: true, thresholds: [80, 95, 100], token_balance_below: 50000, group_ready: true, email_ready: true },
  progress: [
    { metric: 'monthly_calls', label: '月度调用次数', used: 1000, max: 1500, pct: 79, remaining: 500, unlimited: false, warn_below: 0 },
    { metric: 'monthly_tokens', label: '月度token', used: 0, max: 0, pct: 0, remaining: 0, unlimited: true, warn_below: 0 },
    { metric: 'token_balance', label: '预充值余额', used: 0, max: 0, pct: 0, remaining: 200000, unlimited: true, warn_below: 50000 },
  ],
  list: [
    { metric: 'monthly_calls', threshold: 80, period_key: '2026-08', usage_pct: 81, remaining: 285, channels: 'email,group', created_at: '2026-08-21T10:00:00+08:00' },
    { metric: 'token_balance', threshold: 0, period_key: '2026-08', usage_pct: 0, remaining: 40000, channels: '', created_at: '2026-08-25T10:00:00+08:00' },
  ],
}

/** 催缴端点正常返回：本租户正在被催（第 2/3 档、宽限期到 9 月 30 日）。 */
const DUNNING_RUNNING = {
  dunning: { exists: true, status: 'running', stage: 2, total_stages: 3, day_past: 5, due_at: '2026-09-18T00:00:00+08:00', grace_end: '2026-09-30T00:00:00+08:00', suspended: false },
  config: { enabled: true, steps: [1, 5, 15], suspend_after_days: 15 },
}

const OK = (d: unknown) => ({ code: 0, data: d })

/** 按 URL 分流两个端点的返回值（卡片发两路独立请求，不能共用一个 mock 实现）。 */
function route(usage: unknown, dunning: unknown) {
  authMock.mockImplementation(async (url: string) => {
    if (url.startsWith('/api/v1/admin/usage/alerts')) return usage
    if (url.startsWith('/api/v1/admin/billing/dunning')) return dunning
    throw new Error('未预期的请求: ' + url)
  })
}

beforeEach(() => {
  authMock.mockReset()
})

describe('QuotaAlertCard', () => {
  it('两个端点各拉一次，进度数字与进度条宽度逐字用后端值', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    const { container } = render(<QuotaAlertCard />)

    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/usage/alerts?limit=10'))
    expect(authMock).toHaveBeenCalledWith('/api/v1/admin/billing/dunning')

    // 1000/1500 前端复算=66%，后端给 79% → 文字与进度条都必须是 79
    const value = await screen.findByText('1,000 / 1,500（79%）')
    expect(value.textContent).not.toContain('66')
    const bar = container.querySelector<HTMLElement>('.t-progress__inner')
    expect(bar?.style.width).toBe('79%')
  })

  it('无分母的指标不画进度条，改说绝对量与预警线', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    const { container } = render(<QuotaAlertCard />)
    await screen.findByText('剩 200,000')

    expect(screen.getByText(/余额低于 50,000 时通知管理员/)).toBeTruthy()
    // 三条指标里只有 monthly_calls 有分母 → 全卡片只有一根进度条
    expect(container.querySelectorAll('.t-progress__inner').length).toBe(1)
    expect(screen.getByText('月度token').parentElement?.textContent).not.toContain('%')
  })

  it('催缴进行中：横幅用后端的逾期天数/档位/宽限期，并给出续费入口', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    const banner = await screen.findByText(/正在按档催缴/)
    // day_past=5、stage=2、total=3 全部取自后端，前端不按本地时区推算
    expect(banner.textContent).toContain('已到期第 5 天')
    expect(banner.textContent).toContain('第 2/3 档')
    expect(banner.textContent).toContain('2026/9/30')
    expect(banner.querySelector('a')?.getAttribute('href')).toBe('/billing')
    expect(screen.getByText(/到期后第 1\/5\/15 天各提醒一次/)).toBeTruthy()
  })

  it('已停用只说"到账后自动恢复"、不给续费假入口；从未欠费不出横幅', async () => {
    route(OK(USAGE_OK), OK({ dunning: { exists: true, status: 'exhausted', stage: 3, total_stages: 3, day_past: 15, due_at: null, grace_end: null, suspended: true }, config: DUNNING_RUNNING.config }))
    const { unmount } = render(<QuotaAlertCard />)
    const banner = await screen.findByText(/已因欠费停止登录/)
    expect(banner.textContent).toContain('第 3/3 档')
    expect(screen.queryByText('立即续费')).toBeNull()
    unmount()

    route(OK(USAGE_OK), OK({ dunning: { exists: false, status: '', stage: 0, total_stages: 0, day_past: 0, due_at: null, grace_end: null, suspended: false }, config: { enabled: false, steps: [], suspend_after_days: 0 } }))
    render(<QuotaAlertCard />)
    await screen.findByText('1,000 / 1,500（79%）')
    // 副标题本身就写着"欠费催缴走到哪一步"，故只认横幅的两条具体文案
    expect(screen.queryByText(/正在按档催缴|已因欠费停止登录/)).toBeNull()
  })

  it('催缴端点挂掉不得清空额度进度，失败必须显式可见', async () => {
    route(OK(USAGE_OK), { code: 500, message: 'boom' })
    render(<QuotaAlertCard />)
    expect(await screen.findByText(/账单状态读取失败/)).toBeTruthy()
    expect(screen.getByText('1,000 / 1,500（79%）')).toBeTruthy() // 额度侧照常展示
    expect(screen.queryByText(/暂无额度数据/)).toBeNull()
  })

  it('额度端点挂掉不得伪装成"额度为零"', async () => {
    route({ code: 403, message: 'forbidden' }, OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    expect(await screen.findByText(/额度数据读取失败/)).toBeTruthy()
    expect(screen.queryByText('1,000 / 1,500（79%）')).toBeNull()
    expect(screen.queryByText(/暂无额度数据/)).toBeNull() // 失败态不是空态
    expect(screen.getByText(/正在按档催缴/)).toBeTruthy() // 账单侧不受影响
  })

  it('开关关闭与 SMTP 未配：文案各自如实说明，不假装有人在盯', async () => {
    route(OK({ ...USAGE_OK, config: { ...USAGE_OK.config, enabled: false } }), OK(DUNNING_RUNNING))
    const { unmount } = render(<QuotaAlertCard />)
    expect(await screen.findByText(/额度越档提醒当前未启用/)).toBeTruthy()
    unmount()

    route(OK({ ...USAGE_OK, config: { ...USAGE_OK.config, email_ready: false, group_ready: false } }), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    expect(await screen.findByText(/邮件通道未配置，越档提醒不会送达/)).toBeTruthy()
    expect(screen.queryByText(/当前账期 2026-09/)).toBeNull()
  })

  it('提醒口径原样透出阈值与账期', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    expect(await screen.findByText(/当前账期 2026-09/)).toBeTruthy()
    expect(screen.getByText(/用量达 80%\/95%\/100%/)).toBeTruthy()
  })

  it('历史提醒截到 5 条；空列表说"还没有发过"', async () => {
    const many = Array.from({ length: 8 }, (_, i) => ({ ...USAGE_OK.list[0], period_key: `2026-0${(i % 8) + 1}` }))
    route(OK({ ...USAGE_OK, list: many }), OK(DUNNING_RUNNING))
    const { unmount } = render(<QuotaAlertCard />)
    await screen.findByText('1,000 / 1,500（79%）')
    expect(screen.getAllByText('80% 档').length).toBe(5)
    unmount()

    route(OK({ ...USAGE_OK, list: [] }), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    expect(await screen.findByText(/还没有发过额度提醒/)).toBeTruthy()
  })

  it('留痕的送达通道如实回显：无收件人不给绿标', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    await screen.findByText('1,000 / 1,500（79%）')
    expect(screen.getByText('已送达 email,group')).toBeTruthy()
    expect(screen.getByText('无可用收件人')).toBeTruthy()
  })

  it('半截载荷（有 data 无 config）不得把整个工作台带崩', async () => {
    // 真实现场：DashboardTab 冒烟只 stub 了通用响应，卡片曾在此处 TypeError 崩掉整页
    route({ code: 0, data: { period_key: '2026-09', progress: USAGE_OK.progress } }, { code: 0, data: { dunning: DUNNING_RUNNING.dunning } })
    render(<QuotaAlertCard />)
    expect(await screen.findByText('1,000 / 1,500（79%）')).toBeTruthy()
    // 口径缺段就整段不渲染，而不是猜一套默认阈值
    expect(screen.queryByText(/提醒口径|额度越档提醒当前未启用|平台口径/)).toBeNull()
  })

  it('点刷新重新发两路请求', async () => {
    route(OK(USAGE_OK), OK(DUNNING_RUNNING))
    render(<QuotaAlertCard />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(2))
    fireEvent.click(screen.getByRole('button', { name: /刷新/ }))
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(4))
  })
})
