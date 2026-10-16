// 发票受理 Tab（§八-6 平台运营 UI 批，2026-09-18）：超管人工受理租户的发票申请。
// 依赖接口（仅 super_admin，平台路径无需 X-Tenant-ID）：
//   GET  /api/v1/super/invoices?status=requested|issued|voided（上限 200 条）
//   POST /api/v1/super/invoices/:id/issue {invoice_no}（仅 requested→issued；2026-09-18 冒烟批由 :order_id 统一为 :id）
//   POST /api/v1/super/invoices/:id/void（非空状态置 voided，租户可重提）
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, Select, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
// FIX-8(2026-09-27)：发票金额与退款金额是同一笔钱的两个视图，必须走同一个换算口径。
import { fenToYuan } from '../../lib/money'
import { confirmDialog } from '../../lib/confirm'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
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

// 状态码 → 中文/配色（requested 为待开具工作队列，默认筛选）。
// 模块级拿不到 t()，只存「后端值 → I18nKey」，渲染时再 t()。
const STATUS_KEYS: Record<string, I18nKey> = { requested: 'super.invoice.stRequested', issued: 'super.invoice.stIssued', voided: 'super.invoice.stVoided' }
const STATUS_THEME: Record<string, 'warning' | 'success' | 'default'> = { requested: 'warning', issued: 'success', voided: 'default' }

/** 发票受理：状态筛选 + 表格 + 行内开具（弹窗回录发票号）/作废。 */
export function InvoiceTab() {
  const { t } = useI18n()
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
    if (!no) { MessagePlugin.warning(t('super.invoice.noEmpty')); return }
    setSaving(true)
    // 单行模板串：method 与路径同表达式就近内联（契约 METHOD 判定 + 避免尾段字面量误配）
    const j = await AUTH(`/api/v1/super/invoices/${issueRow.order_id}/issue`, { method: 'POST', body: { invoice_no: no } })
    setSaving(false)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.invoice.issued')); setIssueRow(null); setIssueNo(''); load() }
  }
  // 作废：开错/退票场景置 voided，租户侧可重新申请（带二次确认）
  async function doVoid(r: InvoiceRow) {
    if (!(await confirmDialog(t('super.invoice.voidBody', { order: r.order_id }), t('super.invoice.voidTitle')))) return
    const j = await AUTH(`/api/v1/super/invoices/${r.order_id}/void`, { method: 'POST' })
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.invoice.voided')); load() }
  }

  const cols = [
    { colKey: 'tenant_id', title: t('super.invoice.colTenantId'), width: 90, cell: (p: CellProps) => p.row.tenant_id == null ? '-' : '#' + p.row.tenant_id },
    { colKey: 'order_id', title: t('super.invoice.colOrderId'), width: 90 },
    { colKey: 'amount_cents', title: t('super.invoice.colAmount'), width: 110, cell: (p: CellProps) => '¥' + fenToYuan(Number(p.row.amount_cents || 0)) },
    { colKey: 'invoice_title', title: t('super.invoice.colTitle'), width: 180, ellipsis: true },
    { colKey: 'invoice_tax_no', title: t('super.invoice.colTaxNo'), width: 160, cell: (p: CellProps) => p.row.invoice_tax_no || '-' },
    { colKey: 'invoice_email', title: t('super.invoice.colEmail'), width: 160, cell: (p: CellProps) => p.row.invoice_email || '-' },
    { colKey: 'invoice_status', title: t('common.status'), width: 90, cell: (p: CellProps) => <Tag theme={STATUS_THEME[p.row.invoice_status] || 'default'}>{t(STATUS_KEYS[p.row.invoice_status] || p.row.invoice_status as I18nKey)}</Tag> },
    { colKey: 'invoice_no', title: t('super.invoice.colNo'), width: 140, cell: (p: CellProps) => p.row.invoice_no || '-' },
    { colKey: 'op', title: t('super.invoice.colOp'), width: 150, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        {/* 开具仅待开具状态可点（后端仅允许 requested→issued） */}
        {p.row.invoice_status === 'requested' && (
          <Button size="small" theme="primary" onClick={() => { setIssueRow(p.row as InvoiceRow); setIssueNo('') }}>{t('super.invoice.issueBtn')}</Button>
        )}
        {/* 作废对 requested/issued 有意义；voided 终态不再展示 */}
        {p.row.invoice_status !== 'voided' && (
          <Button size="small" theme="danger" variant="outline" onClick={() => doVoid(p.row as InvoiceRow)}>{t('super.invoice.voidBtn')}</Button>
        )}
      </div>) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>{t('super.invoice.title')}</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <Select value={status} onChange={(v) => setStatus(String(v))} options={[
            { label: t('super.invoice.stRequested'), value: 'requested' }, { label: t('super.invoice.stIssued'), value: 'issued' },
            { label: t('super.invoice.stVoided'), value: 'voided' }, { label: t('common.all'), value: '' },
          ]} style={{ width: 140 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>{t('super.invoice.refresh')}</Button>
        </div>
      </div>
      <Table rowKey="order_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty={t('super.invoice.empty')} pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>{t('super.invoice.footNote')}</p>

      {/* 开具弹窗：输入发票号回录（必填，非空前端先校验） */}
      <Dialog
        visible={!!issueRow}
        header={issueRow ? t('super.invoice.issueHeader', { order: issueRow.order_id }) : t('super.invoice.issueTitle')}
        onClose={() => setIssueRow(null)}
        onConfirm={doIssue}
        confirmBtn={{ content: t('super.invoice.confirmIssue'), loading: saving }}
        width={420}
      >
        <div style={{ fontSize: 13, color: '#475569', marginBottom: 8 }}>
          {t('super.invoice.issueBody', {
            title: issueRow?.invoice_title || '-',
            tax: issueRow?.invoice_tax_no || '',
            amount: issueRow ? fenToYuan(issueRow.amount_cents) : '',
          })}
        </div>
        <Input value={issueNo} onChange={(v) => setIssueNo(String(v))} placeholder={t('super.invoice.noPlaceholder')} aria-label={t('super.invoice.noAria')} />
      </Dialog>
    </div>
  )
}
