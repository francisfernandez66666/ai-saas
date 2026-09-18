// 发票受理 Tab（§八-6 平台运营 UI 批，2026-09-18）：超管人工受理租户的发票申请。
// 依赖接口（仅 super_admin，平台路径无需 X-Tenant-ID）：
//   GET  /api/v1/super/invoices?status=requested|issued|voided（上限 200 条）
//   POST /api/v1/super/invoices/:id/issue {invoice_no}（仅 requested→issued；2026-09-18 冒烟批由 :order_id 统一为 :id）
//   POST /api/v1/super/invoices/:id/void（非空状态置 voided，租户可重提）
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, Select, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, TableRowData } from '../../types'

// InvoiceRow 申请行（对齐 billing.go SuperListInvoices 的 gin.H 字段；tenant_id 为指针列可为 null）
type InvoiceRow = {
  order_id: number
  tenant_id: number | null
  amount_cents: number
  invoice_status: string
  invoice_title: string
  invoice_tax_no: string
  invoice_email: string
  invoice_no: string
}
// InvoiceListData data 信封 {list,total}
type InvoiceListData = { list: InvoiceRow[]; total: number }
// Envelope AUTH 响应信封（发票接口沿用统一 code/message/data）
type InvoiceResp = { code: number; message?: string; data?: InvoiceListData }

// 状态码 → 中文/配色（requested 为待开具工作队列，默认筛选）
const STATUS_LABELS: Record<string, string> = { requested: '待开具', issued: '已开具', voided: '已作废' }
const STATUS_THEME: Record<string, 'warning' | 'success' | 'default'> = { requested: 'warning', issued: 'success', voided: 'default' }

/** 发票受理：状态筛选 + 表格 + 行内开具（弹窗回录发票号）/作废。 */
export function InvoiceTab() {
  // 状态筛选（''=全部），默认 requested 工作队列
  const [status, setStatus] = useState('requested')
  const [rows, setRows] = useState<InvoiceRow[]>([])
  const [loading, setLoading] = useState(false)
  // 开具弹窗：非空=正在处理的行；no 为发票号（必填，前端先校验非空再提交）
  const [issueRow, setIssueRow] = useState<InvoiceRow | null>(null)
  const [issueNo, setIssueNo] = useState('')
  const [saving, setSaving] = useState(false)

  // 按筛选状态拉列表（后端 4xx 的 message 由 AUTH 内 toastError 直接弹出）
  const load = useCallback(async () => {
    setLoading(true)
    const j = await AUTH<InvoiceResp>('/api/v1/super/invoices' + (status ? '?status=' + status : ''))
    if (j?.code === 0) setRows(j.data?.list || [])
    setLoading(false)
  }, [status])
  useEffect(() => { load() }, [load])

  // 开具：回录发票号 requested→issued，成功后刷新列表
  async function doIssue() {
    const no = issueNo.trim()
    if (!issueRow) return
    if (!no) { MessagePlugin.warning('发票号不能为空'); return }
    setSaving(true)
    // 单行模板串：method 与路径同表达式就近内联（契约 METHOD 判定 + 避免尾段字面量误配）
    const j = await AUTH(`/api/v1/super/invoices/${issueRow.order_id}/issue`, { method: 'POST', body: { invoice_no: no } })
    setSaving(false)
    if (j?.code === 0) { MessagePlugin.success(j.message || '发票已开具'); setIssueRow(null); setIssueNo(''); load() }
  }
  // 作废：开错/退票场景置 voided，租户侧可重新申请（带二次确认）
  async function doVoid(r: InvoiceRow) {
    if (!(await confirmDialog(`确认作废订单 #${r.order_id} 的发票申请？作废后该租户可重新提交申请。`, '作废发票'))) return
    const j = await AUTH(`/api/v1/super/invoices/${r.order_id}/void`, { method: 'POST' })
    if (j?.code === 0) { MessagePlugin.success(j.message || '发票已作废'); load() }
  }

  const cols = [
    { colKey: 'tenant_id', title: '租户ID', width: 90, cell: (p: CellProps) => p.row.tenant_id == null ? '-' : '#' + p.row.tenant_id },
    { colKey: 'order_id', title: '订单ID', width: 90 },
    { colKey: 'amount_cents', title: '金额(元)', width: 110, cell: (p: CellProps) => '¥' + (Number(p.row.amount_cents || 0) / 100).toFixed(2) },
    { colKey: 'invoice_title', title: '发票抬头', width: 180, ellipsis: true },
    { colKey: 'invoice_tax_no', title: '税号', width: 160, cell: (p: CellProps) => p.row.invoice_tax_no || '-' },
    { colKey: 'invoice_email', title: '接收邮箱', width: 160, cell: (p: CellProps) => p.row.invoice_email || '-' },
    { colKey: 'invoice_status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme={STATUS_THEME[p.row.invoice_status] || 'default'}>{STATUS_LABELS[p.row.invoice_status] || p.row.invoice_status}</Tag> },
    { colKey: 'invoice_no', title: '发票号', width: 140, cell: (p: CellProps) => p.row.invoice_no || '-' },
    { colKey: 'op', title: '操作', width: 150, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        {/* 开具仅待开具状态可点（后端仅允许 requested→issued） */}
        {p.row.invoice_status === 'requested' && (
          <Button size="small" theme="primary" onClick={() => { setIssueRow(p.row as InvoiceRow); setIssueNo('') }}>开具</Button>
        )}
        {/* 作废对 requested/issued 有意义；voided 终态不再展示 */}
        {p.row.invoice_status !== 'voided' && (
          <Button size="small" theme="danger" variant="outline" onClick={() => doVoid(p.row as InvoiceRow)}>作废</Button>
        )}
      </div>
    ) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>发票受理</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Select value={status} onChange={(v) => setStatus(String(v))} options={[
            { label: '待开具', value: 'requested' }, { label: '已开具', value: 'issued' },
            { label: '已作废', value: 'voided' }, { label: '全部', value: '' },
          ]} style={{ width: 140 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>刷新</Button>
        </div>
      </div>
      <Table rowKey="order_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty="暂无发票申请" pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>资质未到位前走人工闭环：租户申请（requested）→ 线下开票后回录发票号（issued）→ 开错可作废重提（voided）。列表上限 200 条。</p>

      {/* 开具弹窗：输入发票号回录（必填，非空前端先校验） */}
      <Dialog
        visible={!!issueRow}
        header={issueRow ? `开具发票 · 订单 #${issueRow.order_id}` : '开具发票'}
        onClose={() => setIssueRow(null)}
        onConfirm={doIssue}
        confirmBtn={{ content: '确认开具', loading: saving }}
        width={420}
      >
        <div style={{ fontSize: 13, color: '#475569', marginBottom: 8 }}>
          抬头：{issueRow?.invoice_title || '-'}{issueRow?.invoice_tax_no ? ` · 税号 ${issueRow.invoice_tax_no}` : ''}{issueRow ? ` · 金额 ¥${(issueRow.amount_cents / 100).toFixed(2)}` : ''}
        </div>
        <Input value={issueNo} onChange={(v) => setIssueNo(String(v))} placeholder="发票号（必填）" aria-label="发票号" />
      </Dialog>
    </div>
  )
}
