// 退款受理 Tab（残项收口批 2026-09-19，B7 双轨退款的平台审批 UI 落点）：
// 租户侧非 mock 模式只能"申请退款"（refund_requested=true），执行/驳回必须经超管端点。
// 依赖接口（仅 super_admin，平台路径无需 X-Tenant-ID）：
//   GET  /api/v1/super/billing/refund-requests（待受理队列：refund_requested=true 且 status=paid，上限 200）
//   POST /api/v1/super/billing/orders/:id/refund（按比例退款+权益回收+PSP 出款，全消耗拒 409）
//   POST /api/v1/super/billing/orders/:id/refund/reject（驳回：仅清申请标记，留审计）
import { useCallback, useEffect, useState } from 'react'
import { Button, Table, Tag, MessagePlugin } from 'tdesign-react'
import type { CellProps, TableRowData } from '../../types'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
// FIX-8(2026-09-27)：退款金额是**要给超管看清楚的数**（二次确认弹窗里就写着它），口径必须与列表同源。
import { fenToYuan } from '../../lib/money'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'

// RefundRow 申请行（对齐 billing.go SuperListRefundRequests 的 gin.H 字段；tenant_id 为指针列可为 null）
type RefundRow = {
  order_id: number
  order_no: string
  tenant_id: number | null
  package_id: number
  amount_cents: number
  channel: string
  period: string
  paid_at: string | null
}
// Envelope AUTH 响应信封（data 为裸数组，对齐 SuperPackList 口径）
type RefundResp = { code: number; message?: string; data?: RefundRow[] }

const CHANNEL_LABELS: Record<string, I18nKey> = { manual: 'super.refund.chanManual', wechat: 'super.refund.chanWechat', alipay: 'super.refund.chanAlipay' }

/** 退款受理：待受理队列 + 行内执行退款（二次确认）/驳回。 */
export function RefundTab() {
  const { t } = useI18n()
  const [rows, setRows] = useState<RefundRow[]>([])
  const [loading, setLoading] = useState(false)
  // 正在处理的订单 id（按钮置灰防连点；退款是资金操作，双提交靠后端行锁但 UI 先行拦截）
  const [busyId, setBusyId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    const j = await AUTH<RefundResp>('/api/v1/super/billing/refund-requests')
    if (j?.code === 0) setRows(j.data || [])
    setLoading(false)
  }, [])
  useEffect(() => { load() }, [load])

  // 执行退款：真钱场景，弹窗明示"按比例退款+权益回收"后果；已全消耗后端拒 409
  async function doRefund(r: RefundRow) {
    const ok = await confirmDialog(
      t('super.refund.confirmRefund', { order_no: r.order_no, tenant_id: r.tenant_id ?? '-', amount: fenToYuan(r.amount_cents) }) +
      t('super.refund.refundWarn'), t('super.refund.execRefund'))
    if (!ok) return
    setBusyId(r.order_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/billing/orders/${r.order_id}/refund`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.refund.done')); load() }
  }
  // 驳回：不符合退款条件（如已全消耗）时清掉申请，租户可后续重提
  async function doReject(r: RefundRow) {
    const ok = await confirmDialog(t('super.refund.confirmReject', { order_no: r.order_no }), t('super.refund.rejectBtn'))
    if (!ok) return
    setBusyId(r.order_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/billing/orders/${r.order_id}/refund/reject`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.refund.rejected')); load() }
  }

  const cols = [
    { colKey: 'tenant_id', title: t('super.refund.colTenantId'), width: 90, cell: (p: CellProps) => p.row.tenant_id == null ? '-' : '#' + p.row.tenant_id },
    { colKey: 'order_no', title: t('super.refund.colOrderNo'), width: 190 },
    { colKey: 'amount_cents', title: t('super.refund.colAmount'), width: 110, cell: (p: CellProps) => '¥' + fenToYuan(Number(p.row.amount_cents || 0)) },
    { colKey: 'channel', title: t('super.refund.colChannel'), width: 90, cell: (p: CellProps) => { const k = CHANNEL_LABELS[p.row.channel]; return <Tag>{k ? t(k) : p.row.channel}</Tag> } },
    { colKey: 'period', title: t('super.refund.colPeriod'), width: 90 },
    { colKey: 'paid_at', title: t('super.refund.colPaidAt'), width: 170, cell: (p: CellProps) => p.row.paid_at ? String(p.row.paid_at).slice(0, 19).replace('T', ' ') : '-' },
    { colKey: 'op', title: t('common.op'), width: 180, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" theme="danger" loading={busyId === p.row.order_id} disabled={busyId !== null}
          onClick={() => doRefund(p.row as RefundRow)}>{t('super.refund.execRefund')}</Button>
        <Button size="small" variant="outline" disabled={busyId !== null}
          onClick={() => doReject(p.row as RefundRow)}>{t('super.refund.reject')}</Button>
      </div>
    ) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>{t('super.refund.title')}</h2>
        <Button theme="primary" variant="outline" onClick={load} loading={loading}>{t('super.refund.refresh')}</Button>
      </div>
      <Table rowKey="order_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty={t('super.refund.empty')} pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        {t('super.refund.footNote')}
      </p>
    </div>
  )
}
