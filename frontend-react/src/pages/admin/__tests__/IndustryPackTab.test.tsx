// F6/T6 行业包绑定页冒烟测试。
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import IndustryPackTab from '../IndustryPackTab'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
}))

import { AUTH } from '../../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>

// 夹具提到具名常量：断言期望串一律从这些字段现推，不抄页面读数
const INDUSTRY_PACK = { id: 1, code: 'auto', name: '汽车', version: '1.0.0', pack_level: 'industry', parent_code: '', status: 'active' }
const ENTERPRISE_PACK = { id: 2, code: 'auto_rox', name: '极石汽车', version: '1.1.0', pack_level: 'enterprise', parent_code: 'auto', status: 'active' }
const STAT_CMP = { template_id: 'tpl_compare', pack_code: 'auto_rox', pack_version: '1.1.0', sample_count: 60, hook_rate: 0.5, lead_rate: 0.2, pending_human_rate: 0.1, avg_intent_delta: 0.03, avg_eval_score: 82 }
const STAT_NEW = { template_id: 'tpl_new', pack_code: 'auto_rox', pack_version: '1.1.0', sample_count: 8, hook_rate: 0.25, lead_rate: 0, pending_human_rate: 0, avg_intent_delta: 0.01, avg_eval_score: 58 }
const STAT_HIST = { template_id: 'tpl_compare', pack_code: 'auto_rox', pack_version: '1.0.0', sample_count: 55, hook_rate: 0.4, lead_rate: 0.1, pending_human_rate: 0.08, avg_intent_delta: 0.02, avg_eval_score: 70 }
// 与组件同款格式化（IndustryPackTab.tsx:152/:表内 delta 表达式）：期望串按夹具重算
const fmtPct = (v: number) => `${(Number(v) * 100).toFixed(1)}%`
const fmtDelta = (cur: number, prev: number) => {
  const d = Number(cur || 0) - Number(prev || 0)
  return `${d >= 0 ? '+' : ''}${(d * 100).toFixed(1)}%`
}

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
            industry: INDUSTRY_PACK,
            enterprise: ENTERPRISE_PACK,
            departments: [],
          },
        }
      }
      if (url.includes('level=enterprise')) return { code: 0, data: [ENTERPRISE_PACK] }
      if (url.includes('level=department')) return { code: 0, data: [] }
      if (url.includes('level=industry')) return { code: 0, data: [INDUSTRY_PACK] }
      if (url.includes('/org/departments/tree')) return { code: 0, data: [{ id: 10, name: '总部', children: [] }] }
      if (url.includes('/admin/packs/stats')) {
        return {
          code: 0,
          data: {
            sample_min: 50,
            list: [STAT_CMP, STAT_NEW, STAT_HIST],
          },
        }
      }
      return { code: 0, data: [] }
    })
    render(<IndustryPackTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/packs/current'))
    // 原来弱在哪：truthy 只证明「极石汽车」四个字在场，目录里蹭一行也算过；
    // 现在钉什么：钉进「当前绑定」摘要块（:401-407），行业/企业两行整串逐字，
    // 名称与版本 Tag 相邻（"企业：极石汽车 1.1.0"）。
    const bindLabel = screen.getByText('当前绑定')
    const bindBlock = bindLabel.parentElement as HTMLElement
    await waitFor(() => expect(bindBlock).toHaveTextContent(`企业：${ENTERPRISE_PACK.name} ${ENTERPRISE_PACK.version}`))
    expect(bindBlock).toHaveTextContent(`行业：${INDUSTRY_PACK.name} ${INDUSTRY_PACK.version}`)
    // 原来弱在哪：findAllByText('企业包').length>0 不说明这个标签属于谁；
    // 现在钉什么：'企业包'=statusLabel(enterprise)（:145），钉在目录里 auto_rox
    // 那一行的层级 Tag 上（编码列与层级列同排，:540-543）。
    const entCatalogRow = screen.getByText(ENTERPRISE_PACK.code).closest('tr') as HTMLElement
    expect(within(entCatalogRow).getByText('企业包')).toBeInTheDocument()
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/packs/stats?days=90')))
    // 归因区块标题确是 h3（:465），不是别处蹭到的同名文案
    const statsTitle = await screen.findByText('包效果归因')
    expect(statsTitle.tagName).toBe('H3')
    // 原来弱在哪：'60'/'82' 全页撒网——汇总卡、别的模板行都可能撞上；
    // 现在钉什么：先按模板码定位到 tpl_compare 那一行，样本数与质量分
    // 都取自 STAT_CMP 字段（期望串 String(sample_count)/String(avg_eval_score)）。
    const cmpRow = screen.getByText(STAT_CMP.template_id).closest('tr') as HTMLElement
    expect(within(cmpRow).getByText(String(STAT_CMP.sample_count))).toBeInTheDocument()
    expect(within(cmpRow).getByText(String(STAT_CMP.avg_eval_score))).toBeInTheDocument()
    // 样本不足 50 走「积累中 N」标签（表内 <50 分支）：钉在 tpl_new 自己那行
    const newRow = screen.getByText(STAT_NEW.template_id).closest('tr') as HTMLElement
    expect(within(newRow).getByText(`积累中 ${STAT_NEW.sample_count}`)).toBeInTheDocument()
    // 历史对比列：表头带旧版本号，且 tpl_compare 行内的历史钩率/增幅整串由
    // STAT_HIST/STAT_CMP 现推（组件同款格式化），不是"有个 1.0.0 字样"就算过
    expect(screen.getByText(`历史对比 ${STAT_HIST.pack_version}`)).toBeInTheDocument()
    expect(within(cmpRow).getByText(`钩 ${fmtPct(STAT_HIST.hook_rate)} · Δ ${fmtDelta(STAT_CMP.hook_rate, STAT_HIST.hook_rate)}`)).toBeInTheDocument()
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
    // 前置守卫改抛错：truthy 红了说不出"哪个控件没渲染"
    if (!input) throw new Error('行业包下拉（placeholder=选择行业包）未渲染')
    fireEvent.click(input)
    // 原来弱在哪：waitFor 里 options>0 只等"浮层开了"；现在等目标选项真出现，
    // 错误信息带选项名，红了即知是渲染慢还是数据没到。
    await waitFor(() => {
      if (!optionByText('汽车')) throw new Error('下拉浮层未出现「汽车」选项')
    })
    return input
  }

  const optionByText = (text: string) =>
    Array.from(document.querySelectorAll<HTMLElement>('.t-select-option'))
      .find((o) => (o.textContent || '').includes(text))

  it('档位不够的包照常列出但点不动，并给一句升级引导', async () => {
    const lockedPack = packRow({ id: 9, code: 'lux', name: '奢侈品', version: '1.0.0', min_tier: 'enterprise', tier_locked: true, tier_reason: 'pack_tier_required' })
    const industryList = [packRow({}), lockedPack]
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/packs/current')) return { code: 0, data: { bound: false } }
      if (url.includes('level=industry')) return { code: 0, data: industryList }
      if (url.includes('level=enterprise')) return { code: 0, data: [] }
      if (url.includes('/org/departments/tree')) return { code: 0, data: [] }
      return { code: 0, data: [] }
    })
    render(<IndustryPackTab />)
    // 原来弱在哪：半句正则 + 写死「1 个」——数字与夹具脱钩，拦了 2 个也过；
    // 现在钉什么：整句逐字，且数字按夹具里 tier_locked 的行数现推（:412-415 的 lockedSummary）。
    const lockedCount = industryList.filter((p) => p.tier_locked).length
    expect(await screen.findByText(
      `另有 ${lockedCount} 个包对你当前套餐未开放，下拉里已置灰并写明需要升到哪一档，升级后即可绑定。`,
    )).toBeInTheDocument()
    const input = await openIndustryPanel()
    // 原来弱在哪：find 命中即 truthy + toContain('企业版') 半句；
    // 现在钉什么：整条 option 文案逐字=packLabel(name·version·code) + 「(企业版专属，需升级套餐后才可绑定)」
    // 后缀（镜像 packOption :132-137 的拼接，档位词表 :119-126），改任何一段都会红。
    const locked = optionByText(`${lockedPack.name} ${lockedPack.version}（${lockedPack.code}）（企业版专属，需升级套餐后才可绑定）`)
    if (!locked) throw new Error('被拦的包没有以带完整升级引导的选项出现在下拉里')
    fireEvent.click(locked)
    expect(input.value).not.toContain(lockedPack.name)
    // 反证：门禁必须证明能放行——同列表里档位够的包要点得动
    const ok = optionByText(`${INDUSTRY_PACK.name} ${INDUSTRY_PACK.version}（${INDUSTRY_PACK.code}）`)
    if (!ok) throw new Error('档位够的包「汽车」没有出现在下拉里')
    fireEvent.click(ok)
    await waitFor(() => expect(input.value).toContain(INDUSTRY_PACK.name))
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
