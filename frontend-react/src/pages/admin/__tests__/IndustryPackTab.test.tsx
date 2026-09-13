// F6/T6 行业包绑定页冒烟测试。
import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import IndustryPackTab from '../IndustryPackTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

beforeEach(() => {
  localStorage.setItem('scrm_auth_token', 'test-token')
  authMock.mockReset()
  vi.spyOn(window, 'alert').mockImplementation(() => {})
  vi.spyOn(window, 'confirm').mockImplementation(() => true)
})

describe('IndustryPackTab', () => {
  it('展示当前绑定与可选包目录', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/packs/current')) {
        return {
          code: 0,
          data: {
            bound: true,
            industry: { id: 1, code: 'auto', name: '汽车', version: '1.0.0', pack_level: 'industry', parent_code: '', status: 'active' },
            enterprise: { id: 2, code: 'auto_rox', name: '极石汽车', version: '1.1.0', pack_level: 'enterprise', parent_code: 'auto', status: 'active' },
            departments: [],
          },
        }
      }
      if (url.includes('level=enterprise')) return { code: 0, data: [{ id: 2, code: 'auto_rox', name: '极石汽车', version: '1.1.0', pack_level: 'enterprise', parent_code: 'auto', status: 'active' }] }
      if (url.includes('level=department')) return { code: 0, data: [] }
      if (url.includes('level=industry')) return { code: 0, data: [{ id: 1, code: 'auto', name: '汽车', version: '1.0.0', pack_level: 'industry', parent_code: '', status: 'active' }] }
      if (url.includes('/org/departments/tree')) return { code: 0, data: [{ id: 10, name: '总部', children: [] }] }
      if (url.includes('/admin/packs/stats')) {
        return {
          code: 0,
          data: {
            sample_min: 50,
            list: [
              { template_id: 'tpl_compare', pack_code: 'auto_rox', pack_version: '1.1.0', sample_count: 60, hook_rate: 0.5, lead_rate: 0.2, pending_human_rate: 0.1, avg_intent_delta: 0.03, avg_eval_score: 82 },
              { template_id: 'tpl_new', pack_code: 'auto_rox', pack_version: '1.1.0', sample_count: 8, hook_rate: 0.25, lead_rate: 0, pending_human_rate: 0, avg_intent_delta: 0.01, avg_eval_score: 58 },
              { template_id: 'tpl_compare', pack_code: 'auto_rox', pack_version: '1.0.0', sample_count: 55, hook_rate: 0.4, lead_rate: 0.1, pending_human_rate: 0.08, avg_intent_delta: 0.02, avg_eval_score: 70 },
            ],
          },
        }
      }
      return { code: 0, data: [] }
    })
    render(<IndustryPackTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/packs/current'))
    expect(await screen.findByText('极石汽车')).toBeTruthy()
    expect((await screen.findAllByText('企业包')).length).toBeGreaterThan(0)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/packs/stats?days=90')))
    expect(await screen.findByText('包效果归因')).toBeTruthy()
    expect(await screen.findByText('tpl_compare')).toBeTruthy()
    expect(screen.getAllByText('60').length).toBeGreaterThan(0)
    expect(screen.getByText('积累中 8')).toBeTruthy()
    expect(screen.getAllByText('82').length).toBeGreaterThan(0)
    expect(screen.getByText(/历史对比 1\.0\.0/)).toBeTruthy()
  })
})
