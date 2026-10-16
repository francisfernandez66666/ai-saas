// 超管·审计日志视图：动作/租户/时间区间筛选（非受控 DOM 读取）+ 50 条分页条（P2-13）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
// 注意：本文件与 admin/AuditTab.tsx 同名不同域——那是租户内审计，这是平台全量审计
import { Button, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { SELECT_STYLE as sel, Section, type Audit } from './shared'

// AuditLogsTab 平台后台「审计日志」页：分页查询平台侧操作审计记录。
export default function AuditLogsTab({ audits, page, total, onPage, onQuery }: {
  audits: Audit[]
  page: number
  total: number
  onPage: (p: number) => void
  onQuery: () => void
}) {
  const { t } = useI18n()
  // 平台审计日志列定义（动作/critical 高亮/操作人/IP）
  const auditCols = [
    { colKey: 'created_at', title: t('common.time'), width: 150 }, { colKey: 'tenant_id', title: t('common.tenant'), width: 70, cell: (p: CellProps) => '#' + p.row.tenant_id },
    { colKey: 'action', title: t('super.audit.action'), width: 200, cell: (p: CellProps) => <Tag theme={p.row.action.includes('critical') ? 'danger' : 'primary'}>{p.row.action}</Tag> },
    { colKey: 'username', title: t('super.audit.colOperator'), width: 100, cell: (p: CellProps) => p.row.username || p.row.user_id }, { colKey: 'resource', title: t('super.audit.colResource'), width: 160 },
    { colKey: 'detail', title: t('common.detail'), width: 260, ellipsis: true }, { colKey: 'ip', title: 'IP', width: 120 },
  ]

  return (
    <Section title={t('super.audit.title')}>
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <select id="aAction" aria-label={t('super.audit.ariaAction')} defaultValue="" style={sel}>
          <option value="">{t('super.audit.optAllAction')}</option>
          <option value="order_manual_confirm_critical">{t('super.audit.optCritical')}</option>
          <option value="order_paid_confirm">{t('super.audit.optOrderPaidConfirm')}</option>
          <option value="order_paid_mock">{t('super.audit.optOrderPaidMock')}</option>
          <option value="super_admin_access">{t('super.audit.optSuperAccess')}</option>
          <option value="super_tenant_status">{t('super.audit.optTenantBan')}</option>
          <option value="tenant_signup">{t('super.audit.optTenantSignup')}</option>
          <option value="apikey_create">{t('super.audit.optApikeyCreate')}</option>
        </select>
        <input id="aTenant" aria-label={t('super.audit.phTenant')} type="number" placeholder={t('super.audit.phTenant')} style={{ ...sel, width: 130 }} />
        <input id="aFrom" aria-label={t('super.audit.phFrom')} type="date" style={sel} /> <span style={{ color: '#718096' }}>{t('super.audit.to')}</span> <input id="aTo" aria-label={t('super.audit.phTo')} type="date" style={sel} />
        <Button theme="primary" onClick={onQuery}>{t('super.audit.query')}</Button>
      </div>
      <Table rowKey="id" data={audits} columns={auditCols} size="small" empty={t('super.audit.empty')} />
      {/* P2-13 翻页条（page 变化经父组件 effect 重拉） */}
      <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 10, padding: '8px 4px 0', fontSize: 13, color: '#718096' }}>
        <span>{t('super.audit.pageInfo', { total, page })}</span>
        <Button size="small" variant="outline" disabled={page <= 1} onClick={() => onPage(Math.max(1, page - 1))}>{t('super.audit.prev')}</Button>
        <Button size="small" variant="outline" disabled={page * 50 >= total} onClick={() => onPage(page + 1)}>{t('super.audit.next')}</Button>
      </div>
    </Section>
  )
}
