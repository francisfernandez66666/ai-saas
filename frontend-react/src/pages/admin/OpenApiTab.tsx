// 开放平台 Tab（F1 从 Admin.tsx 拆出）：API Key 签发、启停与删除。
// P1-7 迁移(2026-09-20 审计批)：裸 fetch → lib/api AUTH（统一超时/401 登出/断网兜底/toastError）；P2-14 文案迁移至 i18n
import { useEffect, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Input, MessagePlugin, Table, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
import type { CellProps, TableRowData } from '../../types'

const PERM_LABELS: Record<string, I18nKey> = { 'customer.read': 'admin.openapi.permRead', 'cdp.read': 'admin.openapi.permCdp', 'chat.read': 'admin.openapi.permChat', 'all': 'admin.openapi.permAll' }

/** OpenAPI Key Tab：管理租户 API Key 的创建、禁用和删除。 */
export function OpenApiTab() {
  const { t } = useI18n()
  const [keys, setKeys] = useState<TableRowData[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [perms, setPerms] = useState<string[]>(['customer.read'])
  async function load() {
    const j = await AUTH('/api/v1/admin/apikeys')
    if (j?.code === 0) setKeys((j.data && j.data.list) || [])
  }
  useEffect(() => { load() }, [])
  async function create() {
    if (!name || perms.length === 0) { MessagePlugin.warning(t('admin.openapi.warnFill')); return }
    // 失败提示由 AUTH 的 toastError 统一处理，避免双弹
    const j = await AUTH('/api/v1/admin/apikeys', { method: 'POST', body: { name, perms } })
    if (j?.code !== 0) return
    const key = j.data.key
    if (await confirmDialog(t('admin.openapi.saveHint', { key }), t('admin.openapi.saveBtn'))) {
      try { await navigator.clipboard.writeText(key) } catch { /* 取数失败静默 */ }
    }
    setName(''); setShowCreate(false); load()
  }
  const toggle = async (id: number, active: boolean) => { await AUTH(`/api/v1/admin/apikeys/${id}/${active ? 'enable' : 'disable'}`, { method: 'POST' }); load() }
  const del = async (id: number) => { if (!(await confirmDialog(t('admin.openapi.confirmDel')))) return; await AUTH(`/api/v1/admin/apikeys/${id}`, { method: 'DELETE' }); load() }
  const cols = [
    { colKey: 'name', title: t('common.name'), width: 160 },
    { colKey: 'key_prefix', title: t('admin.openapi.colPrefix'), width: 160, cell: (p: CellProps) => <code className="bg-gray-100 px-2 py-0.5 rounded text-xs">{p.row.key_prefix}...</code> },
    { colKey: 'permissions', title: t('admin.openapi.colPerm'), width: 200, cell: (p: CellProps) => { try { return JSON.parse(p.row.permissions).map((x: string) => { const k = PERM_LABELS[x]; return k ? t(k) : x }).join('、') } catch { return p.row.permissions } } },
    { colKey: 'call_count', title: t('admin.openapi.colCalls'), width: 100 },
    { colKey: 'last_used_at', title: t('admin.openapi.colLastUsed'), width: 160, cell: (p: CellProps) => p.row.last_used_at ? new Date(p.row.last_used_at).toLocaleString() : t('admin.openapi.neverUsed') },
    { colKey: 'status', title: t('common.status'), width: 100, cell: (p: CellProps) => <Tag theme={p.row.is_active ? 'success' : 'danger'}>{p.row.is_active ? t('admin.openapi.enabled') : t('admin.openapi.disabled')}</Tag> },
    { colKey: 'op', title: t('common.op'), width: 160, cell: (p: CellProps) => (<><Button size="small" variant="outline" theme="warning" onClick={() => toggle(p.row.id, !p.row.is_active)}>{p.row.is_active ? t('admin.openapi.disable') : t('admin.openapi.enable')}</Button> <Button size="small" variant="outline" theme="danger" onClick={() => del(p.row.id)}>{t('admin.openapi.del')}</Button></>) },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex items-center justify-between">
        <div style={{ fontSize: 13, color: '#6b7280' }}>{t('admin.openapi.headerDesc', { base: '/openapi/v1', auth: 'Bearer sk_xxx' })}<a href="/docs/api" target="_blank" rel="noreferrer" style={{ color: 'var(--pri)', marginLeft: 8 }}>{t('admin.openapi.docLink')}</a></div>
        <Button theme="primary" onClick={() => setShowCreate(true)}>+ {t('admin.openapi.issueNew')}</Button>
      </div>
      {showCreate && (
        <div className="bg-white rounded-lg shadow-sm p-4 mb-4">
          <Input value={name} onChange={(v) => setName(v)} placeholder={t('admin.openapi.phName')} style={{ width: 'min(280px,100%)', marginRight: 12, marginBottom: 8 }} />
          {['customer.read', 'cdp.read', 'chat.read'].map((p) => (
            <label key={p} style={{ marginRight: 12, fontSize: 13 }}>
              <input type="checkbox" checked={perms.includes(p)} onChange={(e) => { const ck = (e.target as HTMLInputElement).checked; setPerms(ck ? [...perms, p] : perms.filter((x) => x !== p)) }} /> {PERM_LABELS[p] ? t(PERM_LABELS[p]) : p}
            </label>
          ))}
          <Button theme="success" onClick={create}>{t('admin.openapi.confirmIssue')}</Button>
          <Button theme="default" variant="outline" onClick={() => setShowCreate(false)}>{t('common.cancel')}</Button>
        </div>
      )}
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={keys} columns={cols} size="small" />
      </div>
    </div>
  )
}
