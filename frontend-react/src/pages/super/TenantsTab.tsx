// 超管·租户管理视图：搜索 + 租户表 + 换套餐弹窗
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分），列定义与弹窗随视图一起下沉，写操作由父组件回调下发
import { Button, Dialog, Input, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { type PlanOpt, type Tenant } from './shared'

export default function TenantsTab({ tenants, kw, onKw, onGrant, onSetStatus, onOpenPlan,
  planDlg, planOpts, onClosePlan, onConfirmPlan }: {
  tenants: Tenant[]
  kw: string
  onKw: (v: string) => void
  onGrant: (id: number) => void
  onSetStatus: (id: number, st: string) => void
  onOpenPlan: (t: Tenant) => void
  planDlg: Tenant | null
  planOpts: PlanOpt[]
  onClosePlan: () => void
  onConfirmPlan: () => void
}) {
  // 按关键字过滤租户（名称或标识模糊匹配）
  const filtered = tenants.filter((t) => !kw || t.name.includes(kw) || t.code.includes(kw))

  // 租户列表列定义（含状态标签、用量、停用/恢复与发试用操作）
  const tenantCols = [
    { colKey: 'id', title: 'ID', width: 60 },
    { colKey: 'name', title: '企业名称', width: 160 },
    { colKey: 'code', title: '标识', width: 120 },
    { colKey: 'plan_name', title: '套餐', width: 100, cell: (p: CellProps) => p.row.plan_name || '-' },
    { colKey: 'used', title: '客户用量', width: 100, cell: (p: CellProps) => `${p.row.used_customers}/${p.row.max_customers || '∞'}` },
    { colKey: 'seats', title: '席位', width: 80, cell: (p: CellProps) => `${p.row.used_users ?? 0}/${p.row.max_users || '∞'}` },
    { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme={p.row.status === 'suspended' ? 'danger' : 'success'}>{p.row.status}</Tag> },
    { colKey: 'created_at', title: '开通日期', width: 120 },
    { colKey: 'op', title: '操作', width: 260, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        {p.row.status === 'suspended'
          ? <Button size="small" theme="success" onClick={() => onSetStatus(p.row.id, 'active')}>恢复</Button>
          : <Button size="small" theme="danger" variant="outline" onClick={() => onSetStatus(p.row.id, 'suspended')}>停用</Button>}
        <Button size="small" variant="outline" onClick={() => onGrant(p.row.id)}>发试用</Button>
        <Button size="small" variant="outline" onClick={() => onOpenPlan(p.row as Tenant)}>换套餐</Button>
      </div>
    ) },
  ]

  return (
    <>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0, fontSize: 18 }}>租户管理</h2>
        <Input value={kw} onChange={(v) => onKw(v)} placeholder="按名称/标识搜索" style={{ maxWidth: 280 }} />
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden"><Table rowKey="id" data={filtered} columns={tenantCols} size="small" /></div>
      {/* 换套餐弹窗：下拉展示席位/客户/月价，确认后配额快照同步并刷新列表 */}
      <Dialog
        visible={!!planDlg}
        header={planDlg ? `换套餐 · ${planDlg.name}` : '换套餐'}
        onClose={onClosePlan}
        onConfirm={onConfirmPlan}
        confirmBtn="确认变更"
        width={420}
      >
        <div style={{ fontSize: 13, color: '#475569', marginBottom: 8 }}>
          当前套餐：{planDlg?.plan_name || '-'}（席位 {planDlg?.used_users ?? 0}/{planDlg?.max_users || '∞'}）。降级不删除存量用户/客户，新增按新配额拦截。
        </div>
        <select id="spPlan" defaultValue={String(planDlg?.plan_id || '')} style={{ width: '100%', padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }}>
          {planOpts.map((p) => (
            <option key={p.id} value={String(p.id)}>
              {p.name} · 席位{p.max_users || '∞'} · 客户{p.max_customers || '∞'} · ¥{(p.price_monthly_cents / 100).toFixed(0)}/月
            </option>
          ))}
        </select>
      </Dialog>
    </>
  )
}
