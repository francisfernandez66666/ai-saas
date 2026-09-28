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
    // 命名夹具：断言与 mock 复用同一份数据，期望串从字段真实推导（不复制渲染读数）
    const TEMPLATE = { id: 'tpl_demo', name: '对比锚话术', anchor_type: 3, category: '对比锚', prompt_template: '你和坦克比过吗？', priority: 5, status: 1 }
    const STAT = { template_id: 'tpl_demo', sample_count: 60, hook_rate: 0.4, lead_rate: 0.1, pending_human_rate: 0.05, avg_intent_delta: 0.02, avg_eval_score: 78 }
    // 镜像 StrategyTab.tsx:58 formatPercent 与 PackEffectCell 灰字行（:69-70）的口径
    const fmtPct = (v: number) => `${(v * 100).toFixed(1)}%`
    const EFFECT_LINE = `钩 ${fmtPct(STAT.hook_rate)} / 资 ${fmtPct(STAT.lead_rate)}`
    const SAMPLE_LINE = `${STAT.sample_count} 样本 · 意向 ${fmtPct(STAT.avg_intent_delta)} · 分 ${STAT.avg_eval_score.toFixed(0)}`
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/admin/packs/stats')) {
        return { code: 0, data: { sample_min: 50, list: [STAT] } }
      }
      return {
        code: 0,
        data: { total: 1, page: 1, page_size: 20, list: [TEMPLATE] },
      }
    })
    render(<StrategyTemplateTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/strategy/templates')))
    // 原来弱在哪：length>0 只证明「出现过」；现在钉名称单元格就是夹具的模板名
    expect(await screen.findByText(TEMPLATE.name)).toBeInTheDocument()
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/admin/packs/stats')))
    // 原来弱在哪：length>0；现在钉包效果整行「钩 x% / 资 y%」（由 STAT 推导）
    expect(await screen.findByText(EFFECT_LINE)).toBeInTheDocument()
    // 原来弱在哪：正则 /分 78/ 只命中片段；现在钉灰字整行「60 样本 · 意向 2.0% · 分 78」
    expect(screen.getByText(SAMPLE_LINE)).toBeInTheDocument()
  })
})

describe('StrategyTestTab', () => {
  it('选择客户后展示 7 步推理输出', async () => {
    const CUSTOMER = { id: 7, name: '张三' }
    const CUSTOMER_LABEL = `${CUSTOMER.id} ${CUSTOMER.name}` // 组件里 label=`${id} ${name}`（StrategyTab.tsx:188）
    const RESULT = {
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
    }
    // 镜像组件私有常量 ANCHOR_NAMES（StrategyTab.tsx:19）：selected_anchor=3 的中文标签
    const ANCHOR_NAMES = ['不抛', '同类/场景', '拆解', '对比', '损失', '稀缺', '代价自担']
    const selectedAnchorLabel = ANCHOR_NAMES[RESULT.selected_anchor]
    authMock.mockImplementation(async (url: string) => {
      if (url.includes('/customers')) return { code: 0, data: { list: [CUSTOMER], total: 1 } }
      return { code: 0, data: RESULT }
    })
    render(<StrategyTestTab />)
    await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/customers')))
    // 光等到"/customers 请求发过"不够：组件是在响应回来后自动把第一家客户填进表单的，
    // 而 run() 在 customer_id=0 时只弹一句"请选择测试客户"就返回——抢在这个 effect 落地前点，
    // 断言就会以"没发出 /strategy/test"失败（机器负载高时实测红过一次，用例耗时正好顶满等待窗口）。
    // 原来弱在哪：toBeTruthy 只证明查到；现在用 toBeInTheDocument 明确「表单已填入首条客户（值=7 张三）」这一状态
    await waitFor(() => expect(screen.getByDisplayValue(CUSTOMER_LABEL)).toBeInTheDocument())
    fireEvent.click(screen.getByRole('button', { name: '运行策略' }))
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/strategy/test', expect.objectContaining({ method: 'POST' })))
    // 原来弱在哪：toBeTruthy；现在钉抛话术预览就是回包里的 prompt_text
    expect(await screen.findByText(RESULT.prompt_text)).toBeInTheDocument()
    // 原来弱在哪：findAllByText('对比').length>0 分不清命中哪块；
    // 现在锚定「决策结果 → 选中锚」这一格，钉它显示 selected_anchor 对应的中文标签
    const anchorBlock = screen.getByText('选中锚').closest('div') as HTMLElement
    expect(anchorBlock).toHaveTextContent(selectedAnchorLabel)
    // 原来弱在哪：toBeTruthy；现在钉路由原因整串（route_reason 直读回包）
    expect(screen.getByText(RESULT.route_reason)).toBeInTheDocument()
  })
})
