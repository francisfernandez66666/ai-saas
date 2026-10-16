// 超管·租户管理视图：搜索 + 租户表 + 换套餐弹窗
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分），列定义与弹窗随视图一起下沉，写操作由父组件回调下发
import { Button, Dialog, Input, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { type PlanOpt, type Tenant } from './shared'
// FIX-8(2026-09-27)：套餐月费展示走单点，不再 toFixed(0) 舍掉分位。
import { fenToYuanCompact } from '../../lib/money'

// TenantsTab 平台后台「租户管理」页：租户列表、关键词检索、授权/状态/套餐调整。
export default function TenantsTab({ tenants, kw, onKw, onGrant, onSetStatus, onOpenPlan,
  planDlg, planOpts, onClosePlan, onConfirmPlan }: {
  tenants: Tenant[]
  kw: string
  onKw: (v: string) => void
  onGrant: (id: number) => void
  onSetStatus: (id: number, st: string) => void
  onOpenPlan: (t: Tenant) => void
  planDlg: Tenant | null
  planOpts: PlanOpt[]
  onClosePlan: () => void
  onConfirmPlan: () => void
}) {
  const { t } = useI18n()
  // 按关键字过滤租户（名称或标识模糊匹配）
  const filtered = tenants.filter((x) => !kw || x.name.includes(kw) || x.code.includes(kw))

  // 租户列表列定义（含状态标签、用量、停用/恢复与发试用操作）
  const tenantCols = [
    { colKey: 'id', title: 'ID', width: 60 },
    { colKey: 'name', title: t('super.tenant.colCompany'), width: 160 },
    { colKey: 'code', title: t('super.tenant.colCode'), width: 120 },
    { colKey: 'plan_name', title: t('super.tenant.colPlan'), width: 100, cell: (p: CellProps) => p.row.plan_name || '-' },
    { colKey: 'used', title: t('super.tenant.colCustUsage'), width: 100, cell: (p: CellProps) => `${p.row.used_customers}/${p.row.max_customers || '∞'}` },
    { colKey: 'seats', title: t('super.tenant.colSeats'), width: 80, cell: (p: CellProps) => `${p.row.used_users ?? 0}/${p.row.max_users || '∞'}` },
    { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <Tag theme={p.row.status === 'suspended' ? 'danger' : 'success'}>{p.row.status}</Tag> },
    { colKey: 'created_at', title: t('super.tenant.colCreated'), width: 120 },
    { colKey: 'op', title: t('common.op'), width: 260, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        {p.row.status === 'suspended'
          ? <Button size="small" theme="success" onClick={() => onSetStatus(p.row.id, 'active')}>{t('super.tenant.resume')}</Button>
          : <Button size="small" theme="danger" variant="outline" onClick={() => onSetStatus(p.row.id, 'suspended')}>{t('super.tenant.suspend')}</Button>}
        <Button size="small" variant="outline" onClick={() => onGrant(p.row.id)}>{t('super.tenant.grantTrial')}</Button>
        <Button size="small" variant="outline" onClick={() => onOpenPlan(p.row as Tenant)}>{t('super.tenant.changePlan')}</Button>
      </div>
    ) },
  ]

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0, fontSize: 18 }}>{t('super.tenant.title')}</h2>
        <Input value={kw} onChange={(v) => onKw(v)} placeholder={t('super.tenant.phSearch')} style={{ maxWidth: 280 }} />
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden"><Table rowKey="id" data={filtered} columns={tenantCols} size="small" /></div>
      {/* 换套餐弹窗：下拉展示席位/客户/月价，确认后配额快照同步并刷新列表 */}
      <Dialog
        visible={!!planDlg}
        header={planDlg ? t('super.tenant.dlgHeaderName', { name: planDlg.name }) : t('super.tenant.dlgHeader')}
        onClose={onClosePlan}
        onConfirm={onConfirmPlan}
        confirmBtn={t('super.tenant.confirmChange')}
        width={420}
      >
        <div style={{ fontSize: 13, color: '#475569', marginBottom: 8 }}>
          {t('super.tenant.curPlan', { name: planDlg?.plan_name || '-', used: planDlg?.used_users ?? 0, max: planDlg?.max_users || '∞' })}
        </div>
        <select id="spPlan" defaultValue={String(planDlg?.plan_id || '')} style={{ width: '100%', padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }}>
          {planOpts.map((p) => (
            <option key={p.id} value={String(p.id)}>
              {p.name} · {t('super.tenant.seatLabel', { n: p.max_users || '∞' })} · {t('super.tenant.custLabel', { n: p.max_customers || '∞' })} · ¥{fenToYuanCompact(p.price_monthly_cents)}/月
            </option>
          ))}
        </select>
      </Dialog>
    </>
  )
}
