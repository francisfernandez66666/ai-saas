// 退款受理 Tab（残项收口批 2026-09-19，B7 双轨退款的平台审批 UI 落点）：
// 租户侧非 mock 模式只能"申请退款"（refund_requested=true），执行/驳回必须经超管端点。
// 依赖接口（仅 super_admin，平台路径无需 X-Tenant-ID）：
//   GET  /api/v1/super/billing/refund-requests（待受理队列：refund_requested=true 且 status=paid，上限 200）
//   POST /api/v1/super/billing/orders/:id/refund（按比例退款+权益回收+PSP 出款，全消耗拒 409）
//   POST /api/v1/super/billing/orders/:id/refund/reject（驳回：仅清申请标记，留审计）
import { useCallback, useEffect, useState } from 'react'
import { Button, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, TableRowData } from '../../types'

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

const CHANNEL_LABELS: Record<string, string> = { mock: 'mock', manual: '人工', wechat: '微信', alipay: '支付宝' }

/** 退款受理：待受理队列 + 行内执行退款（二次确认）/驳回。 */
export function RefundTab() {
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
      `确认对订单 ${r.order_no}（租户 #${r.tenant_id ?? '-'}，¥${(r.amount_cents / 100).toFixed(2)}）执行退款？` +
      '将按未消耗比例退回并回收全部剩余权益，真钱渠道同步向 PSP 出款，操作不可逆。', '执行退款')
    if (!ok) return
    setBusyId(r.order_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/billing/orders/${r.order_id}/refund`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || '退款执行完成'); load() }
  }
  // 驳回：不符合退款条件（如已全消耗）时清掉申请，租户可后续重提
  async function doReject(r: RefundRow) {
    const ok = await confirmDialog(`确认驳回订单 ${r.order_no} 的退款申请？仅清除申请标记，不发生资金操作。`, '驳回申请')
    if (!ok) return
    setBusyId(r.order_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/billing/orders/${r.order_id}/refund/reject`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || '已驳回'); load() }
  }

  const cols = [
    { colKey: 'tenant_id', title: '租户ID', width: 90, cell: (p: CellProps) => p.row.tenant_id == null ? '-' : '#' + p.row.tenant_id },
    { colKey: 'order_no', title: '订单号', width: 190 },
    { colKey: 'amount_cents', title: '金额(元)', width: 110, cell: (p: CellProps) => '¥' + (Number(p.row.amount_cents || 0) / 100).toFixed(2) },
    { colKey: 'channel', title: '渠道', width: 90, cell: (p: CellProps) => <Tag>{CHANNEL_LABELS[p.row.channel] || p.row.channel}</Tag> },
    { colKey: 'period', title: '周期', width: 90 },
    { colKey: 'paid_at', title: '支付时间', width: 170, cell: (p: CellProps) => p.row.paid_at ? String(p.row.paid_at).slice(0, 19).replace('T', ' ') : '-' },
    { colKey: 'op', title: '操作', width: 180, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" theme="danger" loading={busyId === p.row.order_id} disabled={busyId !== null}
          onClick={() => doRefund(p.row as RefundRow)}>执行退款</Button>
        <Button size="small" variant="outline" disabled={busyId !== null}
          onClick={() => doReject(p.row as RefundRow)}>驳回</Button>
      </div>
    ) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>退款受理</h2>
        <Button theme="primary" variant="outline" onClick={load} loading={loading}>刷新</Button>
      </div>
      <Table rowKey="order_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty="暂无待受理的退款申请" pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        B7 双轨退款：生产模式下租户只能提交申请，此处为平台审批位。退款按订单窗口未消耗比例计算，已消费部分不退（全消耗后端拒 409）；
        真钱渠道出款失败会标记 psp_pending 并群告警，不回滚退款状态。列表上限 200 条。
      </p>
    </div>
  )
}
