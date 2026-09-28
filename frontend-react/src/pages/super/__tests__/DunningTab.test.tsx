// 超管催缴 Tab 单测（D3，2026-09-23）。
//
// 催缴是"对客户开口"的动作，所以断言重点不是表格好不好看，而是四条边界：
//   1. 「只看在催」开关必须真的换请求参数（切换只改样式不改数据 = 队列读起来永远一样）
//   2. 确认框取消时一个 POST 都不发（人工重发/清序列都不可撤销地影响客户体验）
//   3. 「关闭序列」的确认文案必须写清"不解封、不改到期时间"——这是本 Tab 最容易误读的动作
//   4. 开关未开时明示"当前未启用"，否则运营会把空队列读成"没有一家欠费"
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { MessagePlugin } from 'tdesign-react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  getToken: () => 'test-token',
}))
vi.mock('../../../lib/confirm', () => ({
  confirmDialog: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
import { confirmDialog } from '../../../lib/confirm'
import { DunningTab } from '../DunningTab'

const authMock = AUTH as ReturnType<typeof vi.fn>
const confirmMock = confirmDialog as ReturnType<typeof vi.fn>

/** 队列一行：一家到期第 5 天、正在第 2 档、已脱敏收件人。 */
const ROW = {
  tenant_id: 7, tenant_name: '极石汽车', code: 'roxe', tenant_status: 'expired',
  status: 'running', stage: 2, day_past: 5,
  due_at: '2026-09-18T00:00:00+08:00', next_notify_at: '2026-09-28T09:00:00+08:00',
  last_notified_at: '2026-09-23T09:00:00+08:00', suspended: false,
  grace_end: '2026-09-30T00:00:00+08:00', sent_to: 'a***@b.com',
}
const CFG_ON = { enabled: true, steps: [1, 5, 15], suspend_after_days: 15 }
const USAGE_CFG = { enabled: true, thresholds: [80, 95, 100], token_balance_below: 50000, group_ready: true, email_ready: true }

/** 最后一次 GET 请求的 URL（切换开关/刷新都会发新请求）。 */
function getLastGet() {
  const call = authMock.mock.calls.filter((c) => String(c[0]).startsWith('/api/v1/super/dunning?')).slice(-1)[0]
  return String(call?.[0] ?? '')
}

beforeEach(() => {
  authMock.mockReset()
  confirmMock.mockReset()
  confirmMock.mockResolvedValue(false) // 默认不确认：负向用例的底座
  authMock.mockResolvedValue({ code: 0, data: { list: [ROW], config: CFG_ON, usage_alert_config: USAGE_CFG } })
})

describe('DunningTab（超管催缴受理）', () => {
  it('默认只看在催，队列逐字回显后端字段', async () => {
    render(<DunningTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/dunning?only_open=true&limit=100'))

    // 原来弱在哪：六个 truthy 只证明这些字样散落在页面上——编码挂到别的行、
    // 档位与逾期天数串列，全都照样过；现在钉什么：先拿租户名定位到**它自己那行**，
    // 行内逐格核对 code/stage/day_past/序列标签/脱敏收件人（期望值全部取自 ROW 字段）。
    const nameEl = await screen.findByText(ROW.tenant_name)
    const row = nameEl.closest('tr') as HTMLElement
    expect(row).not.toBeNull()
    expect(within(row).getByText(ROW.code)).toBeInTheDocument()
    expect(within(row).getByText(String(ROW.stage))).toBeInTheDocument()
    expect(within(row).getByText(String(ROW.day_past))).toBeInTheDocument()
    // 档位与逾期天数直接用后端值，前端不推算；'在催'=STATUS_META.running.label（:21）
    const statusTag = within(row).getByText('在催')
    expect(statusTag.closest('.t-tag')?.className).toContain('t-tag--warning')
    expect(within(row).getByText(ROW.sent_to)).toBeInTheDocument() // 脱敏收件人原样展示
    // 原来弱在哪：button>0 只证明页面有按钮（可能是「刷新」一个就够）；
    // 现在钉什么：操作列恰两枚动作钮（立刻再催/关闭序列，:95-100），不多不少。
    expect(within(row).getAllByRole('button').map((b) => b.textContent)).toEqual(['立刻再催', '关闭序列'])
  })

  it('切换「只看在催」真的换请求参数（不是只改样式）', async () => {
    const { container } = render(<DunningTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(1))
    const sw = container.querySelector('.t-switch')
    // 前置守卫：truthy 红了也说不出"哪个控件没渲染"，改抛带选择器的错误
    if (!sw) throw new Error('「只看在催」开关（.t-switch）未渲染')
    fireEvent.click(sw)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(2))
    expect(getLastGet()).toContain('only_open=false')
  })

  it('确认框取消时不发任何 POST', async () => {
    render(<DunningTab />)
    await screen.findByText('极石汽车')
    const before = authMock.mock.calls.length

    for (const label of ['立刻再催', '关闭序列']) {
      fireEvent.click(screen.getByText(label))
      await waitFor(() => expect(confirmMock).toHaveBeenCalled())
    }
    expect(authMock.mock.calls.length).toBe(before)
    expect(getLastGet()).toContain('only_open=true') // 只有 GET，没有动作请求
  })

  it('「关闭序列」确认文案必须写清不解封、不改到期时间', async () => {
    render(<DunningTab />)
    await screen.findByText('极石汽车')
    fireEvent.click(screen.getByText('关闭序列'))
    await waitFor(() => expect(confirmMock).toHaveBeenCalled())
    const msg = String(confirmMock.mock.calls[0][0])
    expect(msg).toContain('不会解除停用')
    expect(msg).not.toMatch(/解封|恢复登录成功/)
  })

  it('确认后打对应端点并刷新队列', async () => {
    confirmMock.mockResolvedValue(true)
    authMock.mockResolvedValue({ code: 0, data: { list: [ROW], config: CFG_ON, usage_alert_config: USAGE_CFG } })
    render(<DunningTab />)
    await screen.findByText('极石汽车')
    const getsBefore = authMock.mock.calls.filter((c) => String(c[0]).startsWith('/api/v1/super/dunning?')).length

    fireEvent.click(screen.getByText('立刻再催'))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/dunning/7/nudge', { method: 'POST' }))
    // 动作成功后重拉队列（否则档位停在旧值）
    await waitFor(() => expect(
      authMock.mock.calls.filter((c) => String(c[0]).startsWith('/api/v1/super/dunning?')).length,
    ).toBe(getsBefore + 1))

    fireEvent.click(screen.getByText('关闭序列'))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/dunning/7/reset', { method: 'POST' }))
  })

  it('后端拒绝时把原因回显出来、且失败后不刷队列', async () => {
    confirmMock.mockResolvedValue(true)
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/nudge')) return { code: 400, message: '该租户当前没有催缴序列' }
      return { code: 0, data: { list: [ROW], config: CFG_ON, usage_alert_config: USAGE_CFG } }
    })
    const errSpy = vi.spyOn(MessagePlugin, 'error').mockImplementation(() => ({ close: () => {} }) as never)
    render(<DunningTab />)
    await screen.findByText('极石汽车')
    const before = authMock.mock.calls.length
    fireEvent.click(screen.getByText('立刻再催'))
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(before + 1))
    expect(errSpy).toHaveBeenCalledWith('该租户当前没有催缴序列')
    // 失败后不得再拉队列刷新（那会把失败伪装成"处理完了"）
    expect(getLastGet()).toContain('only_open=true')
  })

  it('序列开关未开：横幅说明未启用，而不是"没有欠费的"', async () => {
    authMock.mockResolvedValue({ code: 0, data: { list: [], config: { ...CFG_ON, enabled: false }, usage_alert_config: { ...USAGE_CFG, enabled: false } } })
    render(<DunningTab />)
    // 原来弱在哪：truthy 半句正则，「dunning_enabled」键名和"下方有行是历史序列"
    // 这两段口径丢了也过；现在钉什么：整句逐字（:121）。
    expect(await screen.findByText('催缴自动序列当前未启用（平台开关 dunning_enabled）。下方若有行，是历史巡检留下的序列。')).toBeInTheDocument()
    // 空态文案要跟着开关走：只看在催=「暂无在催租户」，切全量应换成「暂无催缴序列」（:133）
    expect(screen.getByText('暂无在催租户')).toBeInTheDocument()
    // 未启用时不展示"第几天催"的口径（那套节奏此刻并不生效）
    expect(screen.queryByText(/到期后第/)).toBeNull()
  })

  it('只催不封的口径如实说明', async () => {
    const cfg = { enabled: true, steps: [1, 5], suspend_after_days: 0 }
    authMock.mockResolvedValue({ code: 0, data: { list: [ROW], config: cfg, usage_alert_config: { ...USAGE_CFG, enabled: false } } })
    render(<DunningTab />)
    // 原来弱在哪：truthy 只截「只催不封」四个字，阶梯串/额度预警段丢了都过；
    // 现在钉什么：整段口径逐块核对——阶梯天数由 cfg.steps 现推（:126），
    // suspend_after_days=0 走「只催不封」分支（:127），额度侧未启用如实说未启用（:128）。
    const policy = await screen.findByText(/当前设置只催不封/)
    expect(policy.textContent).toContain(`平台口径：到期后第 ${cfg.steps.join('/')} 天各发一次催缴`)
    expect(policy.textContent?.replace(/\s+/g, ' ')).toContain('当前设置只催不封。 额度预警：未启用。')
  })

  it('已停用的租户在队列里标红为「序列已尽/停用」两列各自如实', async () => {
    // 夹具从 ROW 派生：断言期望值全部取这个对象的字段，不抄读数
    const ROW_EX = { ...ROW, status: 'exhausted', suspended: true, tenant_status: 'suspended', day_past: 15, stage: 3 }
    authMock.mockResolvedValue({
      code: 0,
      data: { list: [ROW_EX], config: CFG_ON, usage_alert_config: USAGE_CFG },
    })
    render(<DunningTab />)
    // 原来弱在哪：三句 truthy 全页撒网——「15」挂在别的行也过；
    // 现在钉什么：同一行内序列标签（danger 配色）/租户状态原样透出/逾期天数逐格核对。
    const nameEl = await screen.findByText(ROW_EX.tenant_name)
    const row = nameEl.closest('tr') as HTMLElement
    const seqTag = within(row).getByText('序列已尽') // STATUS_META.exhausted.label（:22）
    expect(seqTag.closest('.t-tag')?.className).toContain('t-tag--danger')
    expect(within(row).getByText(ROW_EX.tenant_status)).toBeInTheDocument() // 租户状态列原样透出，不美化
    expect(within(row).getByText(String(ROW_EX.day_past))).toBeInTheDocument()
    expect(within(row).getByText(String(ROW_EX.stage))).toBeInTheDocument()
  })
})
