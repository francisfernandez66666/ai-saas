// F9/T6 工作台指标看板冒烟测试。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { DashboardTab } from '../DashboardTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  vi.stubGlobal('fetch', vi.fn(async () => ({ json: async () => ({ code: 0, data: { list: [{ id: 1, name: '张三', journey_stage: 'lead_captured', intent_score: 0.86 }] } }) })))
})

describe('DashboardTab', () => {
  it('渲染经营指标和模型健康', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/stats/overview')) return { code: 0, data: { total_customers: 12, new_customers_today: 3, active_conversations: 4, conversion_rate: 0.25, avg_intent_score: 0.67, human_transfer_rate: 0.18 } }
      if (url.includes('/advisor/stats')) return { code: 0, data: [{ label: '跟进中', value: 8, color: 'blue' }] }
      if (url.includes('/admin/models')) return { code: 0, data: { models: [{ display_name: 'GLM-4-9B (硅基流动免费)', provider: 'siliconflow', available: false, consecutive_fails: 5, cooldown_left_sec: 120 }] } }
      return { code: 0, data: { list: [] } }
    })
    render(<DashboardTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/stats/overview'))
    expect(await screen.findByText('12')).toBeTruthy()
    expect(screen.getByText('25.0%')).toBeTruthy()
    expect(screen.getByText('跟进中')).toBeTruthy()
    expect(screen.getByText('GLM-4-9B (硅基流动免费)')).toBeTruthy()
    expect((await screen.findAllByText('冷却')).length).toBeGreaterThan(0)
  })
})
