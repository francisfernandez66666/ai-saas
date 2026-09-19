// 开放平台 Tab（F1 从 Admin.tsx 拆出）：API Key 签发、启停与删除。
import { useEffect, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Input, MessagePlugin, Table, Tag } from 'tdesign-react'
import { authHeaders, getToken } from '../../lib/api'
import type { CellProps, TableRowData } from '../../types'

const PERM_LABELS: Record<string, string> = { 'customer.read': '客户读取', 'cdp.read': '画像读取', 'chat.read': '会话读取', 'all': '全部权限' }

/** OpenAPI Key Tab：管理租户 API Key 的创建、禁用和删除。 */
export function OpenApiTab() {
  const [keys, setKeys] = useState<TableRowData[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [perms, setPerms] = useState<string[]>(['customer.read'])
  async function load() {
    const r = await fetch('/api/v1/admin/apikeys', { headers: authHeaders() })
    const j = await r.json()
    if (j.code === 0) setKeys((j.data && j.data.list) || [])
  }
  useEffect(() => { load() }, [])
  async function create() {
    if (!name || perms.length === 0) { MessagePlugin.warning('请填写名称并至少选择一项权限'); return }
    const r = await fetch('/api/v1/admin/apikeys', { method: 'POST', headers: authHeaders({ 'Content-Type': 'application/json' }), body: JSON.stringify({ name, perms }) })
    const j = await r.json()
    if (j.code !== 0) { MessagePlugin.error(j.message || '签发失败'); return }
    const key = j.data.key
    if (await confirmDialog('【请立即保存】明文 Key 仅显示这一次：\n\n' + key + '\n\n点击确定尝试复制到剪贴板', '保存 API Key')) {
      try { await navigator.clipboard.writeText(key) } catch { /* 取数失败静默 */ }
    }
    setName(''); setShowCreate(false); load()
  }
  const toggle = async (id: number, active: boolean) => { await fetch(`/api/v1/admin/apikeys/${id}/${active ? 'enable' : 'disable'}`, { method: 'POST', headers: authHeaders() }); load() }
  const del = async (id: number) => { if (!(await confirmDialog('确认删除该 Key？'))) return; await fetch(`/api/v1/admin/apikeys/${id}`, { method: 'DELETE', headers: authHeaders() }); load() }
  const cols = [
    { colKey: 'name', title: '名称', width: 160 },
    { colKey: 'key_prefix', title: 'Key前缀', width: 160, cell: (p: CellProps) => <code className="bg-gray-100 px-2 py-0.5 rounded text-xs">{p.row.key_prefix}...</code> },
    { colKey: 'permissions', title: '权限', width: 200, cell: (p: CellProps) => { try { return JSON.parse(p.row.permissions).map((x: string) => PERM_LABELS[x] || x).join('、') } catch { return p.row.permissions } } },
    { colKey: 'call_count', title: '调用次数', width: 100 },
    { colKey: 'last_used_at', title: '最近使用', width: 160, cell: (p: CellProps) => p.row.last_used_at ? new Date(p.row.last_used_at).toLocaleString() : '从未使用' },
    { colKey: 'status', title: '状态', width: 100, cell: (p: CellProps) => <Tag theme={p.row.is_active ? 'success' : 'danger'}>{p.row.is_active ? '启用' : '停用'}</Tag> },
    { colKey: 'op', title: '操作', width: 160, cell: (p: CellProps) => (<><Button size="small" variant="outline" theme="warning" onClick={() => toggle(p.row.id, !p.row.is_active)}>{p.row.is_active ? '停用' : '启用'}</Button> <Button size="small" variant="outline" theme="danger" onClick={() => del(p.row.id)}>删除</Button></>) },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex items-center justify-between">
        <div style={{ fontSize: 13, color: '#6b7280' }}>开放 API 基地址：<code className="bg-gray-100 px-2 py-0.5 rounded">/openapi/v1</code>，鉴权：<code className="bg-gray-100 px-2 py-0.5 rounded">Bearer sk_xxx</code>。停用即时生效。<a href="/docs/api" target="_blank" rel="noreferrer" style={{ color: 'var(--pri)', marginLeft: 8 }}>查看接口文档 →</a></div>
        <Button theme="primary" onClick={() => setShowCreate(true)}>+ 签发新 Key</Button>
      </div>
      {showCreate && (
        <div className="bg-white rounded-lg shadow-sm p-4 mb-4">
          <Input value={name} onChange={(v) => setName(v)} placeholder="Key 名称（如：BI系统对接）" style={{ width: 'min(280px,100%)', marginRight: 12, marginBottom: 8 }} />
          {['customer.read', 'cdp.read', 'chat.read'].map((p) => (
            <label key={p} style={{ marginRight: 12, fontSize: 13 }}>
              <input type="checkbox" checked={perms.includes(p)} onChange={(e) => { const ck = (e.target as HTMLInputElement).checked; setPerms(ck ? [...perms, p] : perms.filter((x) => x !== p)) }} /> {PERM_LABELS[p]}
            </label>
          ))}
          <Button theme="success" onClick={create}>确认签发</Button>
          <Button theme="default" variant="outline" onClick={() => setShowCreate(false)}>取消</Button>
        </div>
      )}
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={keys} columns={cols} size="small" />
      </div>
    </div>
  )
}
