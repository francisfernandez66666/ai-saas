// F6/T6 行业包绑定页冒烟测试。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
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

  // G-22c（2026-09-24）档位门槛：后端在 /admin/packs 逐行下发 tier_locked/tier_reason，
  // 界面**照常列出但置灰**。这里锁三件事——引导文案在、被拦的包点不动、能放的包真能选。
  const packRow = (over: Record<string, unknown>) => ({
    id: 1, code: 'auto', name: '汽车', industry: 'auto', version: '1.0.0',
    pack_level: 'industry', parent_code: '', status: 'active',
    min_tier: '', tier_locked: false, tier_reason: '', ...over,
  })

  /** 行业包下拉：jsdom 下 TDesign 渲染成只读 input，必须点 input 才出浮层。 */
  async function openIndustryPanel() {
    const input = Array.from(document.querySelectorAll<HTMLInputElement>('input.t-input__inner'))
      .find((i) => i.placeholder === '选择行业包')
    expect(input).toBeTruthy()
    fireEvent.click(input as HTMLInputElement)
    await waitFor(() => expect(document.querySelectorAll('.t-select-option').length).toBeGreaterThan(0))
    return input as HTMLInputElement
  }

  const optionByText = (text: string) =>
    Array.from(document.querySelectorAll<HTMLElement>('.t-select-option'))
      .find((o) => (o.textContent || '').includes(text))

  it('档位不够的包照常列出但点不动，并给一句升级引导', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/packs/current')) return { code: 0, data: { bound: false } }
      if (url.includes('level=industry')) {
        return {
          code: 0,
          data: [
            packRow({}),
            packRow({ id: 9, code: 'lux', name: '奢侈品', version: '1.0.0', min_tier: 'enterprise', tier_locked: true, tier_reason: 'pack_tier_required' }),
          ],
        }
      }
      if (url.includes('level=enterprise')) return { code: 0, data: [] }
      if (url.includes('/org/departments/tree')) return { code: 0, data: [] }
      return { code: 0, data: [] }
    })
    render(<IndustryPackTab />)
    // 引导文案数字与后端标注行数逐字相等（不是写死的"若干"）
    expect(await screen.findByText(/另有 1 个包对你当前套餐未开放/)).toBeTruthy()
    const input = await openIndustryPanel()
    const locked = optionByText('奢侈品')
    expect(locked).toBeTruthy()
    // 置灰的语义是"点不动"，不是"看着灰"：标签还得说清要升到哪一档
    expect((locked as HTMLElement).textContent).toContain('企业版')
    fireEvent.click(locked as HTMLElement)
    expect(input.value).not.toContain('奢侈品')
    // 反证：门禁必须证明能放行——同列表里档位够的包要点得动
    const ok = optionByText('汽车')
    expect(ok).toBeTruthy()
    fireEvent.click(ok as HTMLElement)
    await waitFor(() => expect(input.value).toContain('汽车'))
  })

  it('没有任何包被档位拦时，引导文案不出现', async () => {
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/packs/current')) return { code: 0, data: { bound: false } }
      if (url.includes('level=industry')) return { code: 0, data: [packRow({})] }
      return { code: 0, data: [] }
    })
    render(<IndustryPackTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/packs?level=industry')))
    // 缺这条负向断言，"永远显示引导"的实现也能把上一条用例跑绿（写死文案就通过了）
    expect(screen.queryByText(/对你当前套餐未开放/)).toBeNull()
  })
})
