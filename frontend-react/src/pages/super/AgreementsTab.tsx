// 超管·协议签署记录视图：按协议类型筛选（user/privacy）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Select, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { AG_TYPES, Section, esc, type Ag } from './shared'

// AgreementsTab 平台后台「协议管理」页：按协议类型切换并维护协议内容。
export default function AgreementsTab({ ags, type, onType }: {
  ags: Ag[]
  type: string
  onType: (v: string) => void
}) {
  // 协议签署记录列定义（用户/租户/协议类型/版本/状态）
  const agCols = [
    { colKey: 'id', title: 'ID', width: 60 }, { colKey: 'username', title: '用户', width: 120, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'agreement_type', title: '协议类型', width: 100, cell: (p: CellProps) => AG_TYPES[p.row.agreement_type] || p.row.agreement_type },
    { colKey: 'version', title: '版本', width: 80 }, { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme="success">{p.row.status}</Tag> },
    { colKey: 'signed_at', title: '签署时间', width: 160, cell: (p: CellProps) => new Date(p.row.signed_at).toLocaleString() },
  ]

  return (
    <Section title="协议签署" desc="用户注册即视为同意《用户协议》《隐私政策》">
      <div style={{ marginBottom: 12 }}><Select value={type} onChange={(v) => onType(v as string)} options={[{ label: '全部协议', value: '' }, { label: '用户协议', value: 'user' }, { label: '隐私政策', value: 'privacy' }]} style={{ width: 160 }} /></div>
      <Table rowKey="id" data={ags} columns={agCols} size="small" empty="暂无签署记录" />
    </Section>
  )
}
