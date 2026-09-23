// 超管催缴 Tab 单测（D3，2026-09-23）。
//
// 催缴是"对客户开口"的动作，所以断言重点不是表格好不好看，而是四条边界：
//   1. 「只看在催」开关必须真的换请求参数（切换只改样式不改数据 = 队列读起来永远一样）
//   2. 确认框取消时一个 POST 都不发（人工重发/清序列都不可撤销地影响客户体验）
//   3. 「关闭序列」的确认文案必须写清"不解封、不改到期时间"——这是本 Tab 最容易误读的动作
//   4. 开关未开时明示"当前未启用"，否则运营会把空队列读成"没有一家欠费"
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
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
    const { container } = render(<DunningTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/super/dunning?only_open=true&limit=100'))

    expect(await screen.findByText('极石汽车')).toBeTruthy()
    expect(screen.getByText('roxe')).toBeTruthy()
    // 档位与逾期天数直接用后端值，前端不推算
    expect(screen.getByText('2')).toBeTruthy()
    expect(screen.getByText('5')).toBeTruthy()
    expect(screen.getByText('在催')).toBeTruthy()
    expect(screen.getByText('a***@b.com')).toBeTruthy() // 脱敏收件人原样展示
    expect(container.querySelectorAll('button').length).toBeGreaterThan(0)
  })

  it('切换「只看在催」真的换请求参数（不是只改样式）', async () => {
    const { container } = render(<DunningTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledTimes(1))
    const sw = container.querySelector('.t-switch')
    expect(sw).toBeTruthy()
    fireEvent.click(sw as Element)
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
    expect(await screen.findByText(/催缴自动序列当前未启用/)).toBeTruthy()
    expect(screen.getByText('暂无在催租户')).toBeTruthy()
    // 未启用时不展示"第几天催"的口径（那套节奏此刻并不生效）
    expect(screen.queryByText(/到期后第/)).toBeNull()
  })

  it('只催不封的口径如实说明', async () => {
    authMock.mockResolvedValue({ code: 0, data: { list: [ROW], config: { enabled: true, steps: [1, 5], suspend_after_days: 0 }, usage_alert_config: { ...USAGE_CFG, enabled: false } } })
    render(<DunningTab />)
    expect(await screen.findByText(/当前设置只催不封/)).toBeTruthy()
  })

  it('已停用的租户在队列里标红为「序列已尽/停用」两列各自如实', async () => {
    authMock.mockResolvedValue({
      code: 0,
      data: { list: [{ ...ROW, status: 'exhausted', suspended: true, tenant_status: 'suspended', day_past: 15, stage: 3 }], config: CFG_ON, usage_alert_config: USAGE_CFG },
    })
    render(<DunningTab />)
    expect(await screen.findByText('序列已尽')).toBeTruthy()
    expect(screen.getByText('suspended')).toBeTruthy() // 租户状态列原样透出，不美化
    expect(screen.getByText('15')).toBeTruthy()
  })
})
