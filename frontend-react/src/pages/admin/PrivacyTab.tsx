// PIPL 删除请求管理 Tab（F1，2026-09-14）：C2 合规链最后一公里。
// 后端 /admin/privacy/deletion-requests 早已存在（列表 + 手动执行），但前端零消费者——
// 商家无法在页面查看/推进客户删除请求，只能等 15 天日批。此处补齐：筛选 + 列表 + 立即执行。
import { useEffect, useState } from 'react'
import { Button, Table, Tag } from 'tdesign-react'
import { authHeaders } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { CellProps } from '../../types'

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

const STATUS_META: Record<string, { label: string; theme: 'primary' | 'warning' | 'success' | 'danger' }> = {
  pending: { label: '待处理', theme: 'warning' },
  processing: { label: '处理中', theme: 'primary' },
  completed: { label: '已完成', theme: 'success' },
  anonymized: { label: '已匿名化', theme: 'success' },
  failed: { label: '失败', theme: 'danger' },
}

/** PIPL 删除请求管理：查看待处理请求、到期倒计时、手动立即执行。 */
export function PrivacyTab() {
  const [rows, setRows] = useState<DelRow[]>([])
  const [status, setStatus] = useState('pending')
  const [err, setErr] = useState('')
  async function load() {
    setErr('')
    try {
      const q = new URLSearchParams({ page: '1', page_size: '100' })
      if (status) q.set('status', status)
      const r = await fetch('/api/v1/admin/privacy/deletion-requests?' + q.toString(), { headers: authHeaders() })
      const j = await r.json()
      if (j.code === 0) setRows(j.data.list || [])
      else setErr(j.message || '加载失败')
    } catch {
      setErr('网络异常，删除请求列表加载失败')
    }
  }
  useEffect(() => { load() }, [status])
  async function execute(id: number) {
    if (!(await confirmDialog('将对该主体的消息/客户/CDP 资料做匿名化处理（断内容列，保留行数与数值统计）。此操作不可撤销。', '立即执行删除请求'))) return
    const r = await fetch(`/api/v1/admin/privacy/deletion-requests/${id}/execute`, { method: 'POST', headers: authHeaders() })
    const j = await r.json()
    if (j.code === 0) load()
    else setErr(j.message || '执行失败')
  }
  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'scope', title: '主体', width: 100, cell: (p: CellProps) => <Tag theme="default">{p.row.scope === 'user' ? '商家用户' : '终端客户'}</Tag> },
    {
      colKey: 'subject', title: '对象', width: 140, cell: (p: CellProps) => {
        const r = p.row as unknown as DelRow
        return r.scope === 'user' ? `user#${r.user_id}` : r.customer_id ? `customer#${r.customer_id}` : '（全量/匿名）'
      },
    },
    { colKey: 'status', title: '状态', width: 100, cell: (p: CellProps) => { const m = STATUS_META[String(p.row.status)] || { label: p.row.status as string, theme: 'default' as const }; return <Tag theme={m.theme}>{m.label}</Tag> } },
    { colKey: 'requested_at', title: '申请时间', width: 160 },
    { colKey: 'deadline', title: '处理截止', width: 160 },
    { colKey: 'error', title: '失败原因', width: 200, ellipsis: true },
    {
      colKey: 'op', title: '操作', width: 110, cell: (p: CellProps) => {
        const r = p.row as unknown as DelRow
        if (r.status === 'completed' || r.status === 'anonymized') return <span style={{ fontSize: 12, color: '#718096' }}>已处理</span>
        return <Button size="small" theme="danger" variant="outline" onClick={() => execute(r.id)}>立即执行</Button>
      },
    },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
        <span style={{ fontSize: 13, color: '#4a5568' }}>PIPL 删除权请求（客户侧 C2 合规链）</span>
        <select value={status} onChange={(e) => setStatus((e.target as HTMLSelectElement).value)} className="px-3 py-2 border rounded-lg text-sm">
          <option value="pending">待处理</option>
          <option value="failed">失败</option>
          <option value="completed">已完成</option>
          <option value="">全部</option>
        </select>
        <Button theme="primary" onClick={load}>刷新</Button>
        {err && <span style={{ color: '#e53e3e', fontSize: 13 }}>{err}</span>}
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty="暂无删除请求" />
      </div>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 8 }}>
        说明：到期请求由每日批处理自动匿名化；此处可对紧急/逾期请求手动立即执行。匿名化断内容列、保留行数与数值统计列。
      </p>
    </div>
  )
}
