// 超管·包质量视图（D9）：跨租户查看行业包/企业包模板效果
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）。样本 <50 打「积累中」标签，只作趋势参考不作迭代结论；P2-14 文案迁移至 i18n
import { Button, Select, Table, Tag } from 'tdesign-react'
import type { CellProps, TableRowData } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { Section, type PackQualityRow } from './shared'

// PackQualityTab 平台后台「包质量」页：按天数窗口刷新行业包质量指标。
export default function PackQualityTab({ rows, days, onDays, onRefresh }: {
  rows: PackQualityRow[]
  days: string
  onDays: (v: string) => void
  onRefresh: () => void
}) {
  const { t } = useI18n()
  return (
    <Section title={t('super.pq.title')} desc={t('super.pq.desc')}>
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <Select value={days} onChange={(v) => onDays(String(v))} options={[{ label: t('super.pq.d7'), value: '7' }, { label: t('super.pq.d30'), value: '30' }, { label: t('super.pq.d90'), value: '90' }]} style={{ width: 130 }} />
        <Button theme="primary" variant="outline" onClick={onRefresh}>{t('super.pq.refresh')}</Button>
      </div>
      <Table rowKey="key" data={rows as TableRowData[]} columns={[
        { colKey: 'tenant_id', title: t('common.tenant'), width: 90 },
        { colKey: 'pack_code', title: t('super.pq.colPack'), width: 150 },
        { colKey: 'pack_version', title: t('common.version'), width: 110, cell: (p: CellProps) => <Tag>{p.row.pack_version || '-'}</Tag> },
        { colKey: 'template_id', title: t('super.pq.colTemplate'), width: 220 },
        { colKey: 'sample_count', title: t('super.pq.colSample'), width: 100, cell: (p: CellProps) => Number(p.row.sample_count || 0) < 50 ? <Tag theme="warning">{t('super.pq.accumulating', { n: p.row.sample_count })}</Tag> : p.row.sample_count },
        { colKey: 'hook_rate', title: t('super.pq.colHook'), width: 100, cell: (p: CellProps) => (Number(p.row.hook_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'lead_rate', title: t('super.pq.colLead'), width: 100, cell: (p: CellProps) => (Number(p.row.lead_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'pending_human_rate', title: t('super.pq.colPendingHuman'), width: 100, cell: (p: CellProps) => (Number(p.row.pending_human_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'avg_eval_score', title: t('super.pq.colScore'), width: 90, cell: (p: CellProps) => typeof p.row.avg_eval_score === 'number' ? p.row.avg_eval_score.toFixed(0) : '-' },
      ]} size="small" empty={t('super.pq.empty')} />
    </Section>
  )
}
