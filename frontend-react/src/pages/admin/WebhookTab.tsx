// D6/F Admin Webhook 管理 UI：订阅 CRUD、测试 ping、投递记录。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, MessagePlugin, Select, Switch, Table, Tag, Textarea } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CellProps } from '../../types'

type WebhookRow = {
  id: number; name: string; url: string; events: string; active: boolean;
  fail_count: number; disabled_at?: string | null; secret_mask: string; created_at: string
}
type DeliveryRow = {
  id: number; event: string; status: string; attempts: number; payload: string;
  last_error: string; next_retry_at?: string | null; delivered_at?: string | null; created_at: string
}
type WebhookForm = { name: string; url: string; events: string[]; active: boolean; secret: string }
type ApiResp<T> = { code: number; message?: string; data?: T }

const EVENT_OPTIONS = [
  { label: '支付成功 payment.paid', value: 'payment.paid' },
  { label: '订单退款 order.refunded', value: 'order.refunded' },
  { label: '线索留资 lead.captured', value: 'lead.captured' },
  { label: '人工接管 human.assigned', value: 'human.assigned' },
]
const DELIVERY_STATUS: Record<string, { label: string; theme: 'primary' | 'success' | 'warning' | 'danger' }> = {
  pending: { label: '待投递', theme: 'primary' },
  delivering: { label: '投递中', theme: 'warning' },
  delivered: { label: '已送达', theme: 'success' },
  failed: { label: '失败重试', theme: 'warning' },
  dead: { label: '死信', theme: 'danger' },
}

/** 解析 Webhook 事件字符串为数组。 */
function eventList(v?: string): string[] {
  return (v || '').split(',').map((x) => x.trim()).filter(Boolean)
}
/** 格式化 Webhook 时间字段。 */
function fmtTime(v?: string | null): string {
  return v ? new Date(v).toLocaleString('zh-CN', { hour12: false }) : '-'
}

/** 出站 Webhook Tab：管理订阅和查看投递记录。 */
export function WebhookTab() {
  const [rows, setRows] = useState<WebhookRow[]>([])
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<WebhookForm | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [createdSecret, setCreatedSecret] = useState<{ url: string; secret: string } | null>(null)
  const [deliveryOpen, setDeliveryOpen] = useState(false)
  const [deliveryTarget, setDeliveryTarget] = useState<WebhookRow | null>(null)
  const [deliveries, setDeliveries] = useState<DeliveryRow[]>([])
  const [deliveryLoading, setDeliveryLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    const j = (await AUTH('/api/v1/admin/webhooks')) as ApiResp<{ list: WebhookRow[] }> | null
    if (j?.code === 0) setRows(j.data?.list || [])
    else MessagePlugin.error(j?.message || 'Webhook 列表加载失败')
    setLoading(false)
  }, [])

  useEffect(() => { void load() }, [load])

  function openCreate() {
    setEditingId(null)
    setForm({ name: '', url: '', events: ['payment.paid', 'lead.captured'], active: true, secret: '' })
  }
  function openEdit(row: WebhookRow) {
    setEditingId(row.id)
    setForm({ name: row.name, url: row.url, events: eventList(row.events), active: row.active, secret: '' })
  }
  function setF<K extends keyof WebhookForm>(k: K, v: WebhookForm[K]) {
    setForm((s) => s ? { ...s, [k]: v } : s)
  }

  async function submit() {
    if (!form) return
    if (!form.url.trim()) { MessagePlugin.warning('请填写回调 URL'); return }
    if (!/^https?:\/\//.test(form.url.trim())) { MessagePlugin.warning('URL 必须以 http(s):// 开头'); return }
    if (!form.events.length) { MessagePlugin.warning('请至少选择一个事件'); return }
    setSaving(true)
    const body: Record<string, unknown> = {
      name: form.name.trim(), url: form.url.trim(), events: form.events, active: form.active,
    }
    if (form.secret.trim()) body.secret = form.secret.trim()
    const url = editingId ? `/api/v1/admin/webhooks/${editingId}` : '/api/v1/admin/webhooks'
    const j = (await AUTH(url, { method: editingId ? 'PUT' : 'POST', body })) as ApiResp<{ secret?: string } | null> | null
    setSaving(false)
    if (j?.code !== 0) { MessagePlugin.error(j?.message || '保存失败'); return }
    if (!editingId && j.data?.secret) setCreatedSecret({ url: form.url.trim(), secret: j.data.secret })
    MessagePlugin.success(editingId ? '已更新' : '已创建')
    setForm(null); setEditingId(null)
    load()
  }

  async function test(row: WebhookRow) {
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}/test`, { method: 'POST' })) as ApiResp<{ ok?: boolean; status?: number; detail?: string }> | null
    if (j?.code === 0 && j.data?.ok) MessagePlugin.success(`测试成功，状态码 ${j.data.status || 200}`)
    else MessagePlugin.warning(j?.data?.detail || j?.message || `测试失败，状态码 ${j?.data?.status || '-'}`)
    load()
  }
  async function toggle(row: WebhookRow, active: boolean) {
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}`, { method: 'PUT', body: { active } })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '状态更新失败')
    load()
  }
  async function remove(row: WebhookRow) {
    if (!confirm(`确认删除订阅「${row.name || row.url}」？`)) return
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}`, { method: 'DELETE' })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '删除失败')
    else MessagePlugin.success('已删除')
    load()
  }
  async function openDeliveries(row: WebhookRow) {
    setDeliveryTarget(row); setDeliveryOpen(true); setDeliveries([]); setDeliveryLoading(true)
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}/deliveries`)) as ApiResp<{ list: DeliveryRow[] }> | null
    if (j?.code === 0) setDeliveries(j.data?.list || [])
    else MessagePlugin.error(j?.message || '投递记录加载失败')
    setDeliveryLoading(false)
  }

  const cols = [
    { colKey: 'name', title: '名称', width: 140, cell: (p: CellProps) => p.row.name || '-' },
    { colKey: 'url', title: '回调地址', width: 260, ellipsis: true },
    { colKey: 'events', title: '事件', width: 230, cell: (p: CellProps) => (
      <div className="flex flex-wrap gap-1">{eventList(p.row.events).map((e) => <Tag key={e} theme="primary" variant="light">{e}</Tag>)}</div>
    ) },
    { colKey: 'status', title: '状态', width: 120, cell: (p: CellProps) => (
      <div className="flex flex-col gap-1">
        <Switch value={p.row.active} onChange={(v: boolean) => void toggle(p.row as WebhookRow, v)} />
        {p.row.disabled_at ? <span className="text-[10px] text-orange-500">已熔断</span> : null}
        {p.row.fail_count > 0 ? <span className="text-[10px] text-red-500">失败 {p.row.fail_count}</span> : null}
      </div>
    ) },
    { colKey: 'secret_mask', title: '密钥', width: 120, cell: (p: CellProps) => <span className="text-xs text-gray-400">{p.row.secret_mask || '-'}</span> },
    { colKey: 'created_at', title: '创建', width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.created_at)}</span> },
    { colKey: 'op', title: '操作', width: 230, cell: (p: CellProps) => (
      <div className="flex gap-2">
        <Button size="small" variant="outline" onClick={() => void test(p.row as WebhookRow)}>测试</Button>
        <Button size="small" variant="text" onClick={() => openEdit(p.row as WebhookRow)}>编辑</Button>
        <Button size="small" variant="text" onClick={() => void openDeliveries(p.row as WebhookRow)}>记录</Button>
        <Button size="small" variant="text" theme="danger" onClick={() => void remove(p.row as WebhookRow)}>删除</Button>
      </div>
    ) },
  ]
  const deliveryCols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'event', title: '事件', width: 130, cell: (p: CellProps) => <Tag theme="primary" variant="light">{p.row.event}</Tag> },
    { colKey: 'status', title: '状态', width: 95, cell: (p: CellProps) => {
      const s = DELIVERY_STATUS[p.row.status] || { label: p.row.status, theme: 'primary' as const }
      return <Tag theme={s.theme}>{s.label}</Tag>
    } },
    { colKey: 'attempts', title: '次数', width: 70 },
    { colKey: 'payload', title: 'Payload', width: 230, ellipsis: true },
    { colKey: 'last_error', title: '错误', width: 180, ellipsis: true, cell: (p: CellProps) => p.row.last_error || '-' },
    { colKey: 'time', title: '时间', width: 170, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.delivered_at || p.row.updated_at || p.row.created_at)}</span> },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div className="text-[13px] text-gray-500">关键事件会带 HMAC-SHA256 签名推送；连续失败会自动熔断，编辑后重新启用可清零。</div>
        <div className="flex gap-2">
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? '刷新中…' : '刷新'}</Button>
          <Button theme="primary" onClick={openCreate}>+ 新增订阅</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty="暂无 Webhook 订阅" loading={loading} />
      </div>

      <Dialog
        header={editingId ? '编辑 Webhook 订阅' : '新增 Webhook 订阅'}
        visible={!!form}
        onClose={() => { setForm(null); setEditingId(null) }}
        onConfirm={submit}
        confirmBtn={saving ? '保存中…' : '保存'}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div><label className="block text-xs text-gray-500 mb-1">名称</label><Input value={form.name} onChange={(v) => setF('name', v)} placeholder="如：支付回调" /></div>
            <div><label className="block text-xs text-gray-500 mb-1">回调 URL</label><Input value={form.url} onChange={(v) => setF('url', v)} placeholder="https://example.com/hooks/scrm" /></div>
            <div><label className="block text-xs text-gray-500 mb-1">订阅事件</label><Select value={form.events} onChange={(v) => setF('events', (v as string[]) || [])} options={EVENT_OPTIONS} multiple filterable style={{ width: '100%' }} /></div>
            <div><label className="block text-xs text-gray-500 mb-1">Secret（留空不修改）</label><Input value={form.secret} onChange={(v) => setF('secret', v)} type="password" placeholder={editingId ? '不修改则留空' : '可自动生成，也可手填'} /></div>
            <div className="flex items-center gap-2"><Switch value={form.active} onChange={(v: boolean) => setF('active', v)} /><span className="text-sm text-gray-600">启用</span></div>
          </div>
        )}
      </Dialog>

      <Dialog
        header="保存一次性 Secret"
        visible={!!createdSecret}
        onClose={() => setCreatedSecret(null)}
        footer={<Button theme="primary" onClick={() => setCreatedSecret(null)}>我已保存</Button>}
        width="620px"
      >
        {createdSecret && (
          <div className="space-y-3">
            <p className="text-sm text-gray-600">请立即复制保存，列表之后只会显示掩码。</p>
            <Input value={createdSecret.url} readOnly />
            <Textarea value={createdSecret.secret} readOnly autosize={{ minRows: 2, maxRows: 4 }} />
          </div>
        )}
      </Dialog>

      <Dialog
        header={deliveryTarget ? `投递记录：${deliveryTarget.name || deliveryTarget.url}` : '投递记录'}
        visible={deliveryOpen}
        onClose={() => { setDeliveryOpen(false); setDeliveryTarget(null) }}
        footer={<Button variant="outline" onClick={() => { setDeliveryOpen(false); setDeliveryTarget(null) }}>关闭</Button>}
        width="1000px"
      >
        <Table rowKey="id" data={deliveries} columns={deliveryCols} size="small" empty="暂无投递记录" loading={deliveryLoading} />
      </Dialog>
    </div>
  )
}
