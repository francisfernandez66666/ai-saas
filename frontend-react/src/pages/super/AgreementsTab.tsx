// 超管·协议签署记录视图：按协议类型筛选（user/privacy）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Select, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { AG_TYPES, Section, esc, type Ag } from './shared'

// AgreementsTab 平台后台「协议管理」页：按协议类型切换并维护协议内容。
export default function AgreementsTab({ ags, type, onType }: {
  ags: Ag[]
  type: string
  onType: (v: string) => void
}) {
  const { t } = useI18n()
  // 协议签署记录列定义（用户/租户/协议类型/版本/状态）
  const agCols = [
    { colKey: 'id', title: 'ID', width: 60 }, { colKey: 'username', title: t('common.user'), width: 120, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'tenant', title: t('common.tenant'), width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'agreement_type', title: t('super.ag.colType'), width: 100, cell: (p: CellProps) => { const k = AG_TYPES[p.row.agreement_type]; return k ? t(k) : p.row.agreement_type } },
    { colKey: 'version', title: t('common.version'), width: 80 }, { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <Tag theme="success">{p.row.status}</Tag> },
    { colKey: 'signed_at', title: t('super.ag.colSignedAt'), width: 160, cell: (p: CellProps) => new Date(p.row.signed_at).toLocaleString() },
  ]

  return (
    <Section title={t('super.ag.title')} desc={t('super.ag.desc')}>
      <div style={{ marginBottom: 12 }}><Select value={type} onChange={(v) => onType(v as string)} options={[{ label: t('super.ag.optAll'), value: '' }, { label: t('super.shared.agUser'), value: 'user' }, { label: t('super.shared.agPrivacy'), value: 'privacy' }]} style={{ width: 160 }} /></div>
      <Table rowKey="id" data={ags} columns={agCols} size="small" empty={t('super.ag.empty')} />
    </Section>
  )
}
