// 超管·AI 商业包管理视图：新增表单（非受控 DOM 读取）+ 包列表上下架
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Button, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { Section, TYPE_NAMES, type Pkg } from './shared'

// PackagesTab 平台后台「套餐管理」页：套餐列表、上下架与新建入口。
export default function PackagesTab({ pkgs, onToggle, onCreate }: {
  pkgs: Pkg[]
  onToggle: (id: number, enabled: boolean) => void
  onCreate: () => void
}) {
  // AI 商业包列定义（类型/售价/有效期/上下架切换）
  const pkgCols = [
    { colKey: 'id', title: 'ID', width: 50 }, { colKey: 'code', title: '标识', width: 120 }, { colKey: 'name', title: '名称', width: 120 },
    { colKey: 'p_type', title: '类型', width: 90, cell: (p: CellProps) => TYPE_NAMES[p.row.p_type] || p.row.p_type },
    { colKey: 'ai_calls', title: 'AI次数', width: 80 },
    { colKey: 'price_cents', title: '售价', width: 90, cell: (p: CellProps) => '¥' + (p.row.price_cents / 100).toFixed(p.row.price_cents % 100 ? 2 : 0) },
    { colKey: 'duration_days', title: '有效期', width: 90, cell: (p: CellProps) => p.row.duration_days ? p.row.duration_days + '天' : '—' },
    { colKey: 'enabled', title: '状态', width: 80, cell: (p: CellProps) => <Tag theme={p.row.enabled ? 'success' : 'default'}>{p.row.enabled ? '上架' : '下架'}</Tag> },
    { colKey: 'op', title: '操作', width: 90, cell: (p: CellProps) => <Button size="small" theme={p.row.enabled ? 'danger' : 'success'} variant="outline" onClick={() => onToggle(p.row.id, !p.row.enabled)}>{p.row.enabled ? '下架' : '上架'}</Button> },
  ]

  return (
    <Section title="AI 商业包管理" desc="公开定价页与租户订阅入口实时读取；已有订单引用的包删除时自动转下架">
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <input id="pCode" placeholder="标识(如 pro_8000)" style={{ width: 130, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <input id="pName" placeholder="名称" style={{ width: 110, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <select id="pType" defaultValue="paid" style={{ width: 120, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }}>
          <option value="paid">包月</option><option value="increment">增量买断</option><option value="free">试用</option>
        </select>
        <input id="pCalls" placeholder="AI次数" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <input id="pPrice" placeholder="售价分" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <input id="pDays" placeholder="有效天数" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <Button theme="primary" onClick={onCreate}>新增</Button>
      </div>
      <Table rowKey="id" data={pkgs} columns={pkgCols} size="small" />
    </Section>
  )
}
