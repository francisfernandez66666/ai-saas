// 超管·包质量视图（D9）：跨租户查看行业包/企业包模板效果
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）。样本 <50 打「积累中」标签，只作趋势参考不作迭代结论
import { Button, Select, Table, Tag } from 'tdesign-react'
import type { CellProps, TableRowData } from '../../types'
import { Section, type PackQualityRow } from './shared'

export default function PackQualityTab({ rows, days, onDays, onRefresh }: {
  rows: PackQualityRow[]
  days: string
  onDays: (v: string) => void
  onRefresh: () => void
}) {
  return (
    <Section title="包质量视图" desc="跨租户查看行业包/企业包模板效果；样本 <50 仅作趋势参考，不直接作为包迭代结论。">
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <Select value={days} onChange={(v) => onDays(String(v))} options={[{ label: '近7天', value: '7' }, { label: '近30天', value: '30' }, { label: '近90天', value: '90' }]} style={{ width: 130 }} />
        <Button theme="primary" variant="outline" onClick={onRefresh}>刷新</Button>
      </div>
      <Table rowKey="key" data={rows as TableRowData[]} columns={[
        { colKey: 'tenant_id', title: '租户', width: 90 },
        { colKey: 'pack_code', title: '包', width: 150 },
        { colKey: 'pack_version', title: '版本', width: 110, cell: (p: CellProps) => <Tag>{p.row.pack_version || '-'}</Tag> },
        { colKey: 'template_id', title: '模板', width: 220 },
        { colKey: 'sample_count', title: '样本', width: 100, cell: (p: CellProps) => Number(p.row.sample_count || 0) < 50 ? <Tag theme="warning">积累中 {p.row.sample_count}</Tag> : p.row.sample_count },
        { colKey: 'hook_rate', title: '接钩率', width: 100, cell: (p: CellProps) => (Number(p.row.hook_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'lead_rate', title: '留资率', width: 100, cell: (p: CellProps) => (Number(p.row.lead_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'pending_human_rate', title: '待人工', width: 100, cell: (p: CellProps) => (Number(p.row.pending_human_rate || 0) * 100).toFixed(1) + '%' },
        { colKey: 'avg_eval_score', title: '质量分', width: 90, cell: (p: CellProps) => typeof p.row.avg_eval_score === 'number' ? p.row.avg_eval_score.toFixed(0) : '-' },
      ]} size="small" empty="暂无包质量数据" />
    </Section>
  )
}
