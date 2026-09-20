// 审计日志 Tab（F1 从 Admin.tsx 拆出）：按动作与时间范围查看关键操作记录。
// P1-7 迁移(2026-09-20 审计批)：裸 fetch → lib/api AUTH（统一超时/401 登出/断网兜底）。
import { useEffect, useState } from 'react'
import { Button, Table, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CellProps, TableRowData } from '../../types'

/** 审计日志 Tab：展示租户内关键操作记录。 */
export function AuditTab() {
  const [rows, setRows] = useState<TableRowData[]>([])
  const [action, setAction] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  // P2-13(2026-09-20 批三)：后端 queryAuditLogs 默认仅回 20 条且无翻页 UI=旧版"以为全量"。
  // 接上 page/page_size 并展示总数，筛选变化回到第 1 页。
  const [page, setPage] = useState(1)
  const [total, setTotal] = useState(0)
  const PAGE_SIZE = 50
  async function load(p = page) {
    const q = new URLSearchParams({ page: String(p), page_size: String(PAGE_SIZE) })
    if (action) q.set('action', action)
    if (from) q.set('from', from)
    if (to) q.set('to', to)
    const j = await AUTH('/api/v1/admin/audit-logs?' + q.toString())
    if (j?.code === 0) { setRows(j.data.list || []); setTotal(Number(j.data.total) || 0) }
  }
  useEffect(() => { load(page) }, [page])
  /** 查询按钮：回第 1 页并强制按当前筛选重拉 */
  function search() { if (page === 1) { void load(1) } else setPage(1) }
  const cols = [
    { colKey: 'created_at', title: '时间', width: 160 },
    { colKey: 'action', title: '动作', width: 200, cell: (p: CellProps) => <Tag theme={String(p.row.action).includes('critical') ? 'danger' : 'primary'}>{p.row.action}</Tag> },
    { colKey: 'username', title: '操作人', width: 120, cell: (p: CellProps) => p.row.username || p.row.user_id },
    { colKey: 'resource', title: '资源', width: 200 },
    { colKey: 'detail', title: '详情', width: 280, ellipsis: true },
    { colKey: 'ip', title: 'IP', width: 120 },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
        <select value={action} onChange={(e) => setAction((e.target as HTMLSelectElement).value)} aria-label="审计动作筛选" className="px-3 py-2 border rounded-lg text-sm">
          <option value="">全部动作</option>
          <option value="order_manual_confirm_critical">我已付费(critical)</option>
          <option value="order_paid_confirm">订单确认发放</option>
          <option value="order_paid_mock">模拟支付</option>
          <option value="apikey_create">API Key签发</option>
          <option value="apikey_disable">API Key停用</option>
          <option value="super_admin_access">超管访问</option>
          <option value="tenant_signup">租户注册</option>
        </select>
        <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="px-3 py-2 border rounded-lg text-sm" />
        <span className="text-gray-400 text-sm">至</span>
        <input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="px-3 py-2 border rounded-lg text-sm" />
        <Button theme="primary" onClick={search}>查询</Button>
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" />
        {/* P2-13 翻页条：总数+上一页/下一页（后端 page_size 上限 100，50/页留余量） */}
        <div className="flex items-center justify-end gap-3 px-4 py-2 text-sm text-gray-500 border-t border-gray-100">
          <span>共 {total} 条 · 第 {page} 页</span>
          <Button size="small" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => Math.max(1, p - 1))}>上一页</Button>
          <Button size="small" variant="outline" disabled={page * PAGE_SIZE >= total} onClick={() => setPage((p) => p + 1)}>下一页</Button>
        </div>
      </div>
    </div>
  )
}
