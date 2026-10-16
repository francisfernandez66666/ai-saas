// 超管·模型成本核算视图：近 N 天各模型调用/token/成本与占比条
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { Section, type Cost } from './shared'

// CostTab 平台后台「成本」页：展示 AI 调用等成本口径统计。
export default function CostTab({ cost }: { cost: Cost | null }) {
  const { t } = useI18n()
  return (
    <Section title={<>{t('super.cost.title')} {cost ? <span style={{ fontSize: 13, color: '#718096' }}>{t('super.cost.subtitle', { days: cost.days, calls: cost.total_calls, tokens: cost.total_tokens, cost: cost.total_cost_yuan })}</span> : null}</>}>
      <Table rowKey="model" data={cost?.models || []} columns={[
        { colKey: 'provider', title: t('super.cost.colProvider'), width: 120 }, { colKey: 'model', title: t('super.cost.colModel'), width: 160 }, { colKey: 'calls', title: t('super.cost.colCalls'), width: 100 },
        { colKey: 'tokens', title: 'Tokens', width: 120 }, { colKey: 'cost_yuan', title: t('super.cost.colCost'), width: 110, cell: (p: CellProps) => '¥' + p.row.cost_yuan.toFixed(4) },
        { colKey: 'cost_share_pct', title: t('super.cost.colShare'), width: 140, cell: (p: CellProps) => <div style={{ background: '#edf2f7', borderRadius: 4, overflow: 'hidden', width: 90, height: 8 }}><div style={{ background: '#4f46e5', height: '100%', width: Math.min(100, p.row.cost_share_pct) + '%' }} /></div> },
      ]} size="small" empty={t('super.cost.empty')} />
    </Section>
  )
}
