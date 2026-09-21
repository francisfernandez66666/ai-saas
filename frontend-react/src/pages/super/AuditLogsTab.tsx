// 超管·审计日志视图：动作/租户/时间区间筛选（非受控 DOM 读取）+ 50 条分页条（P2-13）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
// 注意：本文件与 admin/AuditTab.tsx 同名不同域——那是租户内审计，这是平台全量审计
import { Button, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { SELECT_STYLE as sel, Section, type Audit } from './shared'

// AuditLogsTab 平台后台「审计日志」页：分页查询平台侧操作审计记录。
export default function AuditLogsTab({ audits, page, total, onPage, onQuery }: {
  audits: Audit[]
  page: number
  total: number
  onPage: (p: number) => void
  onQuery: () => void
}) {
  // 平台审计日志列定义（动作/critical 高亮/操作人/IP）
  const auditCols = [
    { colKey: 'created_at', title: '时间', width: 150 }, { colKey: 'tenant_id', title: '租户', width: 70, cell: (p: CellProps) => '#' + p.row.tenant_id },
    { colKey: 'action', title: '动作', width: 200, cell: (p: CellProps) => <Tag theme={p.row.action.includes('critical') ? 'danger' : 'primary'}>{p.row.action}</Tag> },
    { colKey: 'username', title: '操作人', width: 100, cell: (p: CellProps) => p.row.username || p.row.user_id }, { colKey: 'resource', title: '资源', width: 160 },
    { colKey: 'detail', title: '详情', width: 260, ellipsis: true }, { colKey: 'ip', title: 'IP', width: 120 },
  ]

  return (
    <Section title="审计日志">
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <select id="aAction" aria-label="审计动作筛选" defaultValue="" style={sel}><option value="">全部动作</option><option value="order_manual_confirm_critical">我已付费(critical)</option><option value="order_paid_confirm">订单确认发放</option><option value="order_paid_mock">模拟支付</option><option value="super_admin_access">超管访问</option><option value="super_tenant_status">租户封禁/恢复</option><option value="tenant_signup">租户注册</option><option value="apikey_create">API Key签发</option></select>
        <input id="aTenant" aria-label="按租户ID过滤" type="number" placeholder="按租户ID过滤" style={{ ...sel, width: 130 }} />
        <input id="aFrom" aria-label="审计时间起" type="date" style={sel} /> <span style={{ color: '#718096' }}>至</span> <input id="aTo" aria-label="审计时间止" type="date" style={sel} />
        <Button theme="primary" onClick={onQuery}>查询</Button>
      </div>
      <Table rowKey="id" data={audits} columns={auditCols} size="small" empty="暂无记录" />
      {/* P2-13 翻页条（page 变化经父组件 effect 重拉） */}
      <div style={{ display: 'flex', justifyContent: 'flex-end', alignItems: 'center', gap: 10, padding: '8px 4px 0', fontSize: 13, color: '#718096' }}>
        <span>共 {total} 条 · 第 {page} 页</span>
        <Button size="small" variant="outline" disabled={page <= 1} onClick={() => onPage(Math.max(1, page - 1))}>上一页</Button>
        <Button size="small" variant="outline" disabled={page * 50 >= total} onClick={() => onPage(page + 1)}>下一页</Button>
      </div>
    </Section>
  )
}
