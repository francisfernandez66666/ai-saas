// F5/T6 策略中心冒烟测试：模板列表渲染与试运行输出。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { StrategyTemplateTab, StrategyTestTab } from '../StrategyTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
})

describe('StrategyTemplateTab', () => {
  it('渲染策略模板列表', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/admin/packs/stats')) {
        return {
          code: 0,
          data: {
            sample_min: 50,
            list: [{ template_id: 'tpl_demo', sample_count: 60, hook_rate: 0.4, lead_rate: 0.1, pending_human_rate: 0.05, avg_intent_delta: 0.02, avg_eval_score: 78 }],
          },
        }
      }
      return {
        code: 0,
        data: {
          total: 1,
          page: 1,
          page_size: 20,
          list: [{ id: 'tpl_demo', name: '对比锚话术', anchor_type: 3, category: '对比锚', prompt_template: '你和坦克比过吗？', priority: 5, status: 1 }],
        },
      }
    })
    render(<StrategyTemplateTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/strategy/templates')))
    expect((await screen.findAllByText('对比锚话术')).length).toBeGreaterThan(0)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/packs/stats')))
    expect((await screen.findAllByText('钩 40.0% / 资 10.0%')).length).toBeGreaterThan(0)
    expect(screen.getByText(/分 78/)).toBeTruthy()
  })
})

describe('StrategyTestTab', () => {
  it('选择客户后展示 7 步推理输出', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/customers')) return { code: 0, data: { list: [{ id: 7, name: '张三' }], total: 1 } }
      return {
        code: 0,
        data: {
          anchor_scores: [0, 1, 2, 3, 1, 0, 0],
          anchor_probs: [0, 0.1, 0.2, 0.6, 0.1, 0, 0],
          selected_anchor: 3,
          anchor_confidence: 0.6,
          stage_downgraded: false,
          soft_downgrade: false,
          template_id: 'tpl_demo',
          template_name: '对比锚话术',
          prompt_text: '你和坦克比过吗？',
          hook_text: '要不要我发你对比表？',
          exchange_flag: false,
          exchange_type: '',
          urgency_level: 'L2',
          route_result: 'ai',
          route_reason: 'AI可处理',
          intent_delta: 0.05,
        },
      }
    })
    render(<StrategyTestTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/customers')))
    fireEvent.click(screen.getByRole('button', { name: '运行策略' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/strategy/test', expect.objectContaining({ method: 'POST' })))
    expect(await screen.findByText('你和坦克比过吗？')).toBeTruthy()
    expect((await screen.findAllByText('对比')).length).toBeGreaterThan(0)
    expect(screen.getByText('AI可处理')).toBeTruthy()
  })
})
