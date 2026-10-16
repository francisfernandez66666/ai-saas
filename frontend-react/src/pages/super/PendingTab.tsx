// 超管·待确认收款视图：租户已扫码付款并点「我已付费」，核对到账后确认发放（接口幂等）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Button, Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { Section, esc, type Pending } from './shared'
// FIX-8(2026-09-27)：金额口径单点（分 → 元字符串，不做浮点除）。
import { fenToYuan } from '../../lib/money'

// PendingTab 平台后台「待确认收款」页：列出待确认订单并支持确认到账发放。
export default function PendingTab({ pendings, onConfirm }: { pendings: Pending[]; onConfirm: (id: number) => void }) {
  const { t } = useI18n()
  // 待确认收款订单列定义（含确认到账发放按钮）
  const pendingCols = [
    { colKey: 'order_no', title: t('super.pending.colOrderNo'), width: 160 }, { colKey: 'tenant', title: t('common.tenant'), width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'package_name', title: t('super.pending.colPack'), width: 140 }, { colKey: 'amount_cents', title: t('common.amount'), width: 100, cell: (p: CellProps) => '¥' + fenToYuan(p.row.amount_cents) },
    { colKey: 'created_at', title: t('super.pending.colSubmitted'), width: 160 }, { colKey: 'op', title: t('common.op'), width: 140, cell: (p: CellProps) => <Button size="small" theme="success" onClick={() => onConfirm(p.row.id)}>{t('super.pending.confirmPay')}</Button> },
  ]

  return (
    <Section title={<>{t('super.pending.title')} {pendings.length ? <span style={{ fontSize: 13, color: '#975a16' }}>{t('super.pending.badge', { n: pendings.length })}</span> : null}</>} desc={t('super.pending.desc')}>
      <Table rowKey="id" data={pendings} columns={pendingCols} size="small" empty={t('super.pending.empty')} />
    </Section>
  )
}
