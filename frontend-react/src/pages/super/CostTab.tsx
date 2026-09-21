// 超管·模型成本核算视图：近 N 天各模型调用/token/成本与占比条
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { Section, type Cost } from './shared'

// CostTab 平台后台「成本」页：展示 AI 调用等成本口径统计。
export default function CostTab({ cost }: { cost: Cost | null }) {
  return (
    <Section title={<>模型成本核算 <span style={{ fontSize: 13, color: '#718096' }}>{cost ? `近${cost.days}天 · 共${cost.total_calls}次 / ${cost.total_tokens} tokens / ¥${cost.total_cost_yuan}` : ''}</span></>}>
      <Table rowKey="model" data={cost?.models || []} columns={[
        { colKey: 'provider', title: '供应商', width: 120 }, { colKey: 'model', title: '模型', width: 160 }, { colKey: 'calls', title: '调用次数', width: 100 },
        { colKey: 'tokens', title: 'Tokens', width: 120 }, { colKey: 'cost_yuan', title: '成本(¥)', width: 110, cell: (p: CellProps) => '¥' + p.row.cost_yuan.toFixed(4) },
        { colKey: 'cost_share_pct', title: '占比', width: 140, cell: (p: CellProps) => <div style={{ background: '#edf2f7', borderRadius: 4, overflow: 'hidden', width: 90, height: 8 }}><div style={{ background: '#4f46e5', height: '100%', width: Math.min(100, p.row.cost_share_pct) + '%' }} /></div> },
      ]} size="small" empty="暂无用量数据（真实AI对话后生成）" />
    </Section>
  )
}
