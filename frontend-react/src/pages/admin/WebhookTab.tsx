// D6/F Admin Webhook 管理 UI：订阅 CRUD、测试 ping、投递记录。
import { useCallback, useEffect, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Dialog, Input, MessagePlugin, Select, Switch, Table, Tag, Textarea } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps } from '../../types'

// WebhookRow Webhook 订阅列表行：回调地址、订阅事件串、失败计数与密钥掩码（连续失败会自动禁用）。
type WebhookRow = {
  id: number; name: string; url: string; events: string; active: boolean;
  fail_count: number; disabled_at?: string | null; secret_mask: string; created_at: string
}
// DeliveryRow 投递记录行：某订阅某事件一次投递的状态、尝试次数与下次重试时间。
type DeliveryRow = {
  id: number; event: string; status: string; attempts: number; payload: string;
  last_error: string; next_retry_at?: string | null; delivered_at?: string | null; created_at: string
}
// WebhookForm 订阅新建/编辑弹窗的本地表单态：events 以数组存放多选事件码，secret 留空即不改动。
type WebhookForm = { name: string; url: string; events: string[]; active: boolean; secret: string }
// ApiResp 本文件接口调用的标准信封：code/message 为状态位，data 为业务负载。
type ApiResp<T> = { code: number; message?: string; data?: T }

/** 事件码 → 中文名。**事件码是后端契约值不翻译**，只把展示名换成 I18nKey，渲染期再取文案。 */
const EVENT_NAME_KEYS: Record<string, I18nKey> = {
  'payment.paid': 'webhook.eventPaymentPaid',
  'order.refunded': 'webhook.eventOrderRefunded',
  'lead.captured': 'webhook.eventLeadCaptured',
  'human.assigned': 'webhook.eventHumanAssigned',
}
/** 事件码全集（顺序即表单里的展示顺序；后端校验同一集合）。 */
const EVENT_CODES = ['payment.paid', 'order.refunded', 'lead.captured', 'human.assigned']
/** 投递状态码 → 展示 key + 标签主题。未知状态码原样透传（后端新增状态不应白屏）。 */
const DELIVERY_STATUS_KEYS: Record<string, { key: I18nKey; theme: 'primary' | 'success' | 'warning' | 'danger' }> = {
  pending: { key: 'webhook.deliveryPending', theme: 'primary' },
  delivering: { key: 'webhook.deliveryDelivering', theme: 'warning' },
  delivered: { key: 'webhook.deliveryDelivered', theme: 'success' },
  failed: { key: 'webhook.deliveryFailed', theme: 'warning' },
  dead: { key: 'webhook.deliveryDead', theme: 'danger' },
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
  const { t } = useI18n()
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

  // 订阅事件下拉选项：标签 = 中文名 + 事件码（"支付成功 payment.paid"），渲染期组装
  const eventOptions = EVENT_CODES.map((c) => ({ label: t('webhook.eventLabel', { name: t(EVENT_NAME_KEYS[c]), code: c }), value: c }))

  // 拉取 Webhook 订阅列表（useCallback 稳定引用，供 useEffect 与刷新按钮复用）
  const load = useCallback(async () => {
    setLoading(true)
    const j = (await AUTH('/api/v1/admin/webhooks')) as ApiResp<{ list: WebhookRow[] }> | null
    if (j?.code === 0) setRows(j.data?.list || [])
    else MessagePlugin.error(j?.message || t('webhook.loadFailed'))
    setLoading(false)
  }, [t])

  useEffect(() => { void load() }, [load])

  // 打开"新增订阅"表单：默认订阅支付+留资两事件、启用
  function openCreate() {
    setEditingId(null)
    setForm({ name: '', url: '', events: ['payment.paid', 'lead.captured'], active: true, secret: '' })
  }
  // 打开"编辑订阅"表单：回填名称/URL/事件/开关；secret 后端只存掩码故留空表示不改
  function openEdit(row: WebhookRow) {
    setEditingId(row.id)
    setForm({ name: row.name, url: row.url, events: eventList(row.events), active: row.active, secret: '' })
  }
  // 受控更新表单某字段（form 为 null 时忽略）
  function setF<K extends keyof WebhookForm>(k: K, v: WebhookForm[K]) {
    setForm((s) => s ? { ...s, [k]: v } : s)
  }

  // 提交新建/编辑：URL 协议与事件非空校验；新建且后端回吐 secret 时弹一次性明文
  async function submit() {
    if (!form) return
    if (!form.url.trim()) { MessagePlugin.warning(t('webhook.urlRequired')); return }
    if (!/^https?:\/\//.test(form.url.trim())) { MessagePlugin.warning(t('webhook.urlScheme')); return }
    if (!form.events.length) { MessagePlugin.warning(t('webhook.eventRequired')); return }
    setSaving(true)
    const body: Record<string, unknown> = {
      name: form.name.trim(), url: form.url.trim(), events: form.events, active: form.active,
    }
    if (form.secret.trim()) body.secret = form.secret.trim()
    const url = editingId ? `/api/v1/admin/webhooks/${editingId}` : '/api/v1/admin/webhooks'
    const j = (await AUTH(url, { method: editingId ? 'PUT' : 'POST', body })) as ApiResp<{ secret?: string } | null> | null
    setSaving(false)
    if (j?.code !== 0) { MessagePlugin.error(j?.message || t('common.saveFailed')); return }
    if (!editingId && j.data?.secret) setCreatedSecret({ url: form.url.trim(), secret: j.data.secret })
    MessagePlugin.success(editingId ? t('crud.updated') : t('crud.created'))
    setForm(null); setEditingId(null)
    load()
  }

  // 测试 ping：向订阅 URL 发一条测试事件，展示对端状态码
  async function test(row: WebhookRow) {
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}/test`, { method: 'POST' })) as ApiResp<{ ok?: boolean; status?: number; detail?: string }> | null
    if (j?.code === 0 && j.data?.ok) MessagePlugin.success(t('webhook.testOk', { status: j.data.status || 200 }))
    else MessagePlugin.warning(j?.data?.detail || j?.message || t('webhook.testFail', { status: j?.data?.status || '-' }))
    load()
  }
  // 启停订阅（active 开关）；编辑后重新启用可清零连续失败熔断计数
  async function toggle(row: WebhookRow, active: boolean) {
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}`, { method: 'PUT', body: { active } })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('webhook.statusFailed'))
    load()
  }
  // 删除订阅：二次确认后 DELETE
  async function remove(row: WebhookRow) {
    if (!(await confirmDialog(t('webhook.confirmDelete', { name: row.name || row.url })))) return
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}`, { method: 'DELETE' })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('webhook.deleteFailed'))
    else MessagePlugin.success(t('crud.deleted'))
    load()
  }
  // 打开投递记录抽屉：拉取该订阅最近的事件投递明细（状态/次数/错误/payload）
  async function openDeliveries(row: WebhookRow) {
    setDeliveryTarget(row); setDeliveryOpen(true); setDeliveries([]); setDeliveryLoading(true)
    const j = (await AUTH(`/api/v1/admin/webhooks/${row.id}/deliveries`)) as ApiResp<{ list: DeliveryRow[] }> | null
    if (j?.code === 0) setDeliveries(j.data?.list || [])
    else MessagePlugin.error(j?.message || t('webhook.deliveryLoadFailed'))
    setDeliveryLoading(false)
  }

  // 订阅表格列定义（含启停开关、熔断/失败计数、操作区）
  const cols = [
    { colKey: 'name', title: t('common.name'), width: 140, cell: (p: CellProps) => p.row.name || '-' },
    { colKey: 'url', title: t('webhook.colUrl'), width: 260, ellipsis: true },
    { colKey: 'events', title: t('webhook.colEvents'), width: 230, cell: (p: CellProps) => (
      <div className="flex flex-wrap gap-1">{eventList(p.row.events).map((e) => <Tag key={e} theme="primary" variant="light">{e}</Tag>)}</div>
    ) },
    { colKey: 'status', title: t('common.status'), width: 120, cell: (p: CellProps) => (
      <div className="flex flex-col gap-1">
        <Switch value={p.row.active} onChange={(v: boolean) => void toggle(p.row as WebhookRow, v)} />
        {p.row.disabled_at ? <span className="text-[10px] text-orange-500">{t('webhook.tripped')}</span> : null}
        {p.row.fail_count > 0 ? <span className="text-[10px] text-red-500">{t('webhook.failCount', { count: p.row.fail_count })}</span> : null}
      </div>
    ) },
    { colKey: 'secret_mask', title: t('webhook.colSecret'), width: 120, cell: (p: CellProps) => <span className="text-xs text-gray-400">{p.row.secret_mask || '-'}</span> },
    { colKey: 'created_at', title: t('webhook.colCreated'), width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.created_at)}</span> },
    { colKey: 'op', title: t('crud.actions'), width: 230, cell: (p: CellProps) => (
      <div className="flex gap-2">
        <Button size="small" variant="outline" onClick={() => void test(p.row as WebhookRow)}>{t('webhook.test')}</Button>
        <Button size="small" variant="text" onClick={() => openEdit(p.row as WebhookRow)}>{t('crud.edit')}</Button>
        <Button size="small" variant="text" onClick={() => void openDeliveries(p.row as WebhookRow)}>{t('webhook.records')}</Button>
        <Button size="small" variant="text" theme="danger" onClick={() => void remove(p.row as WebhookRow)}>{t('crud.delete')}</Button>
      </div>
    ) },
  ]
  // 投递记录表格列定义（事件/状态标签/重试次数/错误/payload/时间）
  const deliveryCols = [
    { colKey: 'id', title: t('common.id'), width: 70 },
    { colKey: 'event', title: t('webhook.colEvents'), width: 130, cell: (p: CellProps) => <Tag theme="primary" variant="light">{p.row.event}</Tag> },
    { colKey: 'status', title: t('common.status'), width: 95, cell: (p: CellProps) => {
      const s = DELIVERY_STATUS_KEYS[p.row.status]
      return <Tag theme={s ? s.theme : 'primary'}>{s ? t(s.key) : p.row.status}</Tag>
    } },
    { colKey: 'attempts', title: t('webhook.colAttempts'), width: 70 },
    { colKey: 'payload', title: 'Payload', width: 230, ellipsis: true },
    { colKey: 'last_error', title: t('webhook.colError'), width: 180, ellipsis: true, cell: (p: CellProps) => p.row.last_error || '-' },
    { colKey: 'time', title: t('common.time'), width: 170, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.delivered_at || p.row.updated_at || p.row.created_at)}</span> },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div className="text-[13px] text-gray-500">{t('webhook.intro')}</div>
        <div className="flex gap-2">
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? t('webhook.refreshing') : t('crud.refresh')}</Button>
          <Button theme="primary" onClick={openCreate}>{t('webhook.createBtn')}</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty={t('webhook.empty')} loading={loading} />
      </div>

      <Dialog
        header={editingId ? t('webhook.editTitle') : t('webhook.createTitle')}
        visible={!!form}
        onClose={() => { setForm(null); setEditingId(null) }}
        onConfirm={submit}
        confirmBtn={saving ? t('crud.saving') : t('crud.save')}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div><label className="block text-xs text-gray-500 mb-1">{t('common.name')}</label><Input value={form.name} onChange={(v) => setF('name', v)} placeholder={t('webhook.namePh')} /></div>
            <div><label className="block text-xs text-gray-500 mb-1">{t('webhook.urlLabel')}</label><Input value={form.url} onChange={(v) => setF('url', v)} placeholder="https://example.com/hooks/scrm" /></div>
            <div><label className="block text-xs text-gray-500 mb-1">{t('webhook.eventsLabel')}</label><Select value={form.events} onChange={(v) => setF('events', (v as string[]) || [])} options={eventOptions} multiple filterable style={{ width: '100%' }} /></div>
            <div><label className="block text-xs text-gray-500 mb-1">{t('webhook.secretLabel')}</label><Input value={form.secret} onChange={(v) => setF('secret', v)} type="password" placeholder={editingId ? t('webhook.secretKeepPh') : t('webhook.secretCreatePh')} /></div>
            <div className="flex items-center gap-2"><Switch value={form.active} onChange={(v: boolean) => setF('active', v)} /><span className="text-sm text-gray-600">{t('common.enable')}</span></div>
          </div>
        )}
      </Dialog>

      <Dialog
        header={t('webhook.secretDialogTitle')}
        visible={!!createdSecret}
        onClose={() => setCreatedSecret(null)}
        footer={<Button theme="primary" onClick={() => setCreatedSecret(null)}>{t('webhook.savedSecret')}</Button>}
        width="620px"
      >
        {createdSecret && (
          <div className="space-y-3">
            <p className="text-sm text-gray-600">{t('webhook.secretWarn')}</p>
            <Input value={createdSecret.url} readOnly />
            <Textarea value={createdSecret.secret} readOnly autosize={{ minRows: 2, maxRows: 4 }} />
          </div>
        )}
      </Dialog>

      <Dialog
        header={deliveryTarget ? t('webhook.deliveryTitle', { name: deliveryTarget.name || deliveryTarget.url }) : t('webhook.deliveryTitlePlain')}
        visible={deliveryOpen}
        onClose={() => { setDeliveryOpen(false); setDeliveryTarget(null) }}
        footer={<Button variant="outline" onClick={() => { setDeliveryOpen(false); setDeliveryTarget(null) }}>{t('common.close')}</Button>}
        width="1000px"
      >
        <Table rowKey="id" data={deliveries} columns={deliveryCols} size="small" empty={t('webhook.deliveryEmpty')} loading={deliveryLoading} />
      </Dialog>
    </div>
  )
}
