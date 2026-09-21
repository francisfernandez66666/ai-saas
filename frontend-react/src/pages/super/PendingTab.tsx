// 超管·待确认收款视图：租户已扫码付款并点「我已付费」，核对到账后确认发放（接口幂等）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Button, Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { Section, esc, type Pending } from './shared'

export default function PendingTab({ pendings, onConfirm }: { pendings: Pending[]; onConfirm: (id: number) => void }) {
  // 待确认收款订单列定义（含确认到账发放按钮）
  const pendingCols = [
    { colKey: 'order_no', title: '订单号', width: 160 }, { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'package_name', title: '商业包', width: 140 }, { colKey: 'amount_cents', title: '金额', width: 100, cell: (p: CellProps) => '¥' + (p.row.amount_cents / 100).toFixed(2) },
    { colKey: 'created_at', title: '提交时间', width: 160 }, { colKey: 'op', title: '操作', width: 140, cell: (p: CellProps) => <Button size="small" theme="success" onClick={() => onConfirm(p.row.id)}>确认到账并发放</Button> },
  ]

  return (
    <Section title={<>待确认收款 <span style={{ fontSize: 13, color: '#975a16' }}>{pendings.length ? `(${pendings.length}笔待核实)` : ''}</span></>} desc="租户已扫码付款并点击「我已付费」，请核对账户到账后确认发放权益（重复确认自动幂等跳过）">
      <Table rowKey="id" data={pendings} columns={pendingCols} size="small" empty="暂无待确认收款" />
    </Section>
  )
}
