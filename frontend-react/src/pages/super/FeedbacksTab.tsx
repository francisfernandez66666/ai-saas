// 超管·用户反馈视图：状态/类型双筛选 + 反馈表（C6 前端异常也进入这里）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Button, Select, Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { FB_TYPES, Section, esc, type Fb } from './shared'

// FeedbacksTab 平台后台「反馈处理」页：按状态与对象筛选并处理用户反馈。
export default function FeedbacksTab({ fbs, status, onStatus, target, onTarget, onResolve }: {
  fbs: Fb[]
  status: string
  onStatus: (v: string) => void
  target: string
  onTarget: (v: string) => void
  onResolve: (id: number) => void
}) {
  // 用户反馈列定义（租户/提交人/类型/意见/标记处理）
  const fbCols = [
    { colKey: 'created_at', title: '时间', width: 140, cell: (p: CellProps) => (p.row.created_at || '').replace('T', ' ').slice(0, 16) },
    { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `#${p.row.tenant_id} ${esc(p.row.tenant_name)}` },
    { colKey: 'username', title: '提交人', width: 100, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'target_type', title: '类型', width: 90, cell: (p: CellProps) => FB_TYPES[p.row.target_type] || p.row.target_type },
    { colKey: 'content', title: '意见', width: 220, ellipsis: true },
    { colKey: 'op', title: '操作', width: 100, cell: (p: CellProps) => p.row.status === 'open' ? <Button size="small" theme="success" onClick={() => onResolve(p.row.id)}>标记处理</Button> : <span style={{ fontSize: 12, color: '#718096' }}>已处理</span> },
  ]

  return (
    <Section title="用户反馈" desc="顾问端AI回复气泡「反馈」提交，C6 前端异常也会进入这里；新反馈推群机器人">
      <div style={{ marginBottom: 12, display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        <Select value={status} onChange={(v) => onStatus(v as string)} options={[{ label: '待处理', value: 'open' }, { label: '已处理', value: 'resolved' }, { label: '全部', value: '' }]} style={{ width: 160 }} />
        <Select value={target} onChange={(v) => onTarget(v as string)} options={[{ label: '全部类型', value: '' }, { label: 'AI话术', value: 'ai_reply' }, { label: '功能建议', value: 'feature' }, { label: '满意度', value: 'rating' }, { label: '前端异常', value: 'client_error' }, { label: '其他', value: 'other' }]} style={{ width: 160 }} />
      </div>
      <Table rowKey="id" data={fbs} columns={fbCols} size="small" empty="暂无反馈" />
    </Section>
  )
}
