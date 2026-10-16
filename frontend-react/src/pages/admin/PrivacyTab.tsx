// PIPL 删除请求管理 Tab（F1，2026-09-14）：C2 合规链最后一公里。
// 后端 /admin/privacy/deletion-requests 早已存在（列表 + 手动执行），但前端零消费者——
// 商家无法在页面查看/推进客户删除请求，只能等 15 天日批。此处补齐：筛选 + 列表 + 立即执行。
// P1-7 迁移(2026-09-20 审计批)：裸 fetch → lib/api AUTH；P2-14 文案迁移至 i18n
import { useEffect, useState } from 'react'
import { Button, Table, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
import type { CellProps } from '../../types'

// DelRow PIPL 删除请求列表行：删除范围、关联客户/用户、状态与截止/处理时间。
type DelRow = {
  id: number
  scope: string
  customer_id: number
  user_id: number
  status: string
  requested_at: string
  deadline: string
  processed_at?: string | null
  error?: string
}

const STATUS_META: Record<string, { label: I18nKey; theme: 'primary' | 'warning' | 'success' | 'danger' }> = {
  pending: { label: 'admin.privacy.stPending', theme: 'warning' },
  processing: { label: 'admin.privacy.stProcessing', theme: 'primary' },
  completed: { label: 'admin.privacy.stCompleted', theme: 'success' },
  anonymized: { label: 'admin.privacy.stAnonymized', theme: 'success' },
  failed: { label: 'admin.privacy.stFailed', theme: 'danger' },
}

const SCOPE_LABEL: Record<string, I18nKey> = { user: 'admin.privacy.subjectUser', customer: 'admin.privacy.subjectCustomer' }

/** PIPL 删除请求管理：查看待处理请求、到期倒计时、手动立即执行。 */
export function PrivacyTab() {
  const { t } = useI18n()
  const [rows, setRows] = useState<DelRow[]>([])
  const [status, setStatus] = useState('pending')
  const [err, setErr] = useState('')
  async function load() {
    setErr('')
    const q = new URLSearchParams({ page: '1', page_size: '100' })
    if (status) q.set('status', status)
    // AUTH 网络异常返回 {code:-1,...} 信封，不再抛错，故无需 try/catch
    const j = await AUTH('/api/v1/admin/privacy/deletion-requests?' + q.toString())
    if (j?.code === 0) setRows(j.data.list || [])
    else setErr(j?.message || t('common.loadFailed'))
  }
  useEffect(() => { load() }, [status, t])
  async function execute(id: number) {
    if (!(await confirmDialog(t('admin.privacy.confirmBody'), t('admin.privacy.execBtn')))) return
    const j = await AUTH(`/api/v1/admin/privacy/deletion-requests/${id}/execute`, { method: 'POST' })
    if (j?.code === 0) load()
    else setErr(j?.message || t('admin.privacy.execFailed'))
  }
  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'scope', title: t('admin.privacy.colSubject'), width: 100, cell: (p: CellProps) => { const k = SCOPE_LABEL[String(p.row.scope)]; return <Tag theme="default">{k ? t(k) : p.row.scope}</Tag> } },
    {
      colKey: 'subject', title: t('admin.privacy.colObject'), width: 140, cell: (p: CellProps) => {
        const r = p.row as unknown as DelRow
        if (r.scope === 'user') return `user#${r.user_id}`
        return r.customer_id ? `customer#${r.customer_id}` : t('admin.privacy.allAnon')
      },
    },
    { colKey: 'status', title: t('common.status'), width: 100, cell: (p: CellProps) => { const m = STATUS_META[String(p.row.status)] || { label: p.row.status as I18nKey, theme: 'default' as const }; return <Tag theme={m.theme}>{t(m.label)}</Tag> } },
    { colKey: 'requested_at', title: t('admin.privacy.colRequested'), width: 160 },
    { colKey: 'deadline', title: t('admin.privacy.colDeadline'), width: 160 },
    { colKey: 'error', title: t('admin.privacy.colError'), width: 200, ellipsis: true },
    {
      colKey: 'op', title: t('common.op'), width: 110, cell: (p: CellProps) => {
        const r = p.row as unknown as DelRow
        if (r.status === 'completed' || r.status === 'anonymized') return <span style={{ fontSize: 12, color: '#718096' }}>{t('admin.privacy.handled')}</span>
        return <Button size="small" theme="danger" variant="outline" onClick={() => execute(r.id)}>{t('admin.privacy.exec')}</Button>
      },
    },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
        <span style={{ fontSize: 13, color: '#4a5568' }}>{t('admin.privacy.headerDesc')}</span>
        <select value={status} onChange={(e) => setStatus((e.target as HTMLSelectElement).value)} className="px-3 py-2 border rounded-lg text-sm">
          <option value="pending">{t('admin.privacy.stPending')}</option>
          <option value="failed">{t('admin.privacy.stFailed')}</option>
          <option value="completed">{t('admin.privacy.stCompleted')}</option>
          <option value="">{t('common.all')}</option>
        </select>
        <Button theme="primary" onClick={load}>{t('admin.privacy.refresh')}</Button>
        {err && <span style={{ color: '#e53e3e', fontSize: 13 }}>{err}</span>}
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty={t('admin.privacy.empty')} />
      </div>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 8 }}>
        {t('admin.privacy.footNote')}
      </p>
    </div>
  )
}
