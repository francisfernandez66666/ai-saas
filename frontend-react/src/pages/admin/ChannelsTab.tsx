// F11 通道接入 Tab（F1 从 Admin.tsx 拆出）：通道凭据 CRUD、连通测试与出站死信重发。
import { useEffect, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Dialog, Input, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import { authHeaders } from '../../lib/api'
import type { CellProps } from '../../types'

type ChannelView = {
  id: number; type: string; name: string; corpid: string; appid: string; status: string;
  department_id: number; secret_mask: string; token_mask: string; aeskey_mask: string; config_json: string; created_at: string
}
type OutboundView = {
  id: number; channel_id: number; customer_id: number; conversation_id: number; content: string;
  status: string; retries: number; error: string; created_at: string; next_retry_at?: string | null
}
type ChannelForm = {
  type: string; name: string; corpid: string; appid: string; agentid: string; secret: string;
  token: string; encoding_aes_key: string; department_id: string; config_json: string
}
type ApiResp<T> = { code: number; message?: string; data?: T }
type CreateChannelResp = { channel: ChannelView; plaintext?: { secret?: string; token?: string; encoding_aes_key?: string }; callback_url?: string }

const CHANNEL_TYPE_LABELS: Record<string, string> = { wecom_app: '企业微信自建应用', wecom_kf: '微信客服', wechat_mp: '微信公众号' }
const CHANNEL_STATUS_LABELS: Record<string, string> = { active: '已启用', disabled: '已停用', unverified: '待验证' }
const CHANNEL_STATUS_THEMES: Record<string, 'success' | 'danger' | 'warning'> = { active: 'success', disabled: 'danger', unverified: 'warning' }

/** 安全解析通道 JSON 配置，异常时回退空对象。 */
function safeParseConfig(s: string): Record<string, unknown> {
  try {
    const o = JSON.parse(s || '{}')
    return o && typeof o === 'object' && !Array.isArray(o) ? o : {}
  } catch {
    return {}
  }
}

/** 通道接入 Tab：维护企微/微信客服/公众号配置并查看死信。 */
export function ChannelsTab() {
  const [channels, setChannels] = useState<ChannelView[]>([])
  const [dlq, setDlq] = useState<OutboundView[]>([])
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<ChannelForm | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [created, setCreated] = useState<CreateChannelResp | null>(null)

  // 统一鉴权头（Authorization + 超管代管时 X-Tenant-ID）
  const auth = (): Record<string, string> => authHeaders()

  // 并行拉取通道列表与出站死信列表，回填两个表格
  async function load() {
    setLoading(true)
    try {
      const [cr, dr] = await Promise.all([
        fetch('/api/v1/admin/channels', { headers: auth() }),
        fetch('/api/v1/admin/channel-dlq', { headers: auth() }),
      ])
      const cj = (await cr.json().catch(() => null)) as ApiResp<{ list: ChannelView[] }> | null
      const dj = (await dr.json().catch(() => null)) as ApiResp<{ list: OutboundView[] }> | null
      if (cj?.code === 0) setChannels(cj.data?.list || [])
      else MessagePlugin.warning(cj?.message || '通道列表加载失败')
      if (dj?.code === 0) setDlq(dj.data?.list || [])
    } finally {
      setLoading(false)
    }
  }
  useEffect(() => { load() }, [])

  // 打开"新增通道"表单：预置默认类型 wecom_app，清空全部字段
  function openCreate() {
    setEditingId(null)
    setForm({ type: 'wecom_app', name: '', corpid: '', appid: '', agentid: '', secret: '', token: '', encoding_aes_key: '', department_id: '', config_json: '{}' })
  }
  // 打开"编辑通道"表单：回填非敏感字段；secret/token/aeskey 后端只存掩码，故留空表示"不修改"
  function openEdit(ch: ChannelView) {
    const cfg = safeParseConfig(ch.config_json)
    setEditingId(ch.id)
    setForm({
      type: ch.type, name: ch.name, corpid: ch.corpid, appid: ch.appid, agentid: String(cfg.agentid || ''),
      secret: '', token: '', encoding_aes_key: '', department_id: String(ch.department_id || ''), config_json: ch.config_json || '{}',
    })
  }
  // 关闭表单弹窗并退出编辑态
  function closeForm() { setForm(null); setEditingId(null) }
  // 受控更新表单某个字段（form 为 null 时忽略，防弹窗外误写）
  function setF<K extends keyof ChannelForm>(k: K, v: ChannelForm[K]) {
    setForm((s) => s ? { ...s, [k]: v } : s)
  }
  // 是否要求填 CorpID：企微自建应用/微信客服需要，公众号只需 AppID
  function requireWecomCorpid(type: string) { return type !== 'wechat_mp' }

  // 提交新建/编辑：先做前端必填与扩展配置 JSON 形态校验，再 PUT/POST；新建成功弹一次性明文
  async function submit() {
    if (!form) return
    if (!form.name.trim()) { MessagePlugin.warning('请输入通道名称'); return }
    if (requireWecomCorpid(form.type) && !form.corpid.trim()) { MessagePlugin.warning('企微通道需要填 CorpID'); return }
    if (form.type === 'wechat_mp' && !form.appid.trim()) { MessagePlugin.warning('公众号通道需要填 AppID'); return }
    if (!editingId && !form.secret.trim()) { MessagePlugin.warning('新建通道需要 Secret；如要稍后补录，可取消本次创建'); return }
    let cfg = form.config_json.trim() || '{}'
    try {
      const parsed = JSON.parse(cfg)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('bad')
      cfg = JSON.stringify(parsed)
    } catch {
      MessagePlugin.warning('扩展配置 JSON 必须是对象'); return
    }
    const body = {
      type: form.type, name: form.name.trim(), corpid: form.corpid.trim(), appid: form.appid.trim(), agentid: form.agentid.trim(),
      secret: form.secret.trim(), token: form.token.trim(), encoding_aes_key: form.encoding_aes_key.trim(),
      department_id: Number(form.department_id || 0) || undefined, config_json: cfg,
    }
    setSaving(true)
    try {
      const url = editingId ? `/api/v1/admin/channels/${editingId}` : '/api/v1/admin/channels'
      const r = await fetch(url, { method: editingId ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json', ...auth() }, body: JSON.stringify(body) })
      const j = (await r.json().catch(() => null)) as ApiResp<CreateChannelResp | null> | null
      if (j?.code !== 0) { MessagePlugin.error(j?.message || '保存失败'); return }
      if (!editingId) {
        setCreated(j.data || null)
        MessagePlugin.success('通道已创建，请保存一次性明文凭据')
      } else {
        MessagePlugin.success('通道已更新')
      }
      closeForm()
      load()
    } finally {
      setSaving(false)
    }
  }

  // 连通测试：调后端 verify 端点试拿 access_token，成功/失败提示并刷新列表状态
  async function verify(ch: ChannelView) {
    const r = await fetch(`/api/v1/admin/channels/${ch.id}/verify`, { method: 'POST', headers: auth() })
    const j = (await r.json().catch(() => null)) as ApiResp<{ ok?: boolean; detail?: string }> | null
    if (j?.code === 0 && j.data?.ok) MessagePlugin.success('连通成功')
    else MessagePlugin.warning(j?.data?.detail || j?.message || '连通失败')
    load()
  }
  // 启用/停用通道（active↔disabled），停用后入站回调与出站投递不再处理
  async function setStatus(ch: ChannelView, status: 'active' | 'disabled') {
    const r = await fetch(`/api/v1/admin/channels/${ch.id}/status?status=${status}`, { method: 'PUT', headers: auth() })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '状态更新失败')
    load()
  }
  // 删除通道：二次确认后调 DELETE，回调地址随即失效（提示已写明）
  async function del(ch: ChannelView) {
    if (!(await confirmDialog(`确认删除通道「${ch.name}」？删除后回调地址会失效。`))) return
    const r = await fetch(`/api/v1/admin/channels/${ch.id}`, { method: 'DELETE', headers: auth() })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '删除失败')
    else MessagePlugin.success('已删除')
    load()
  }
  // 死信重发：把出站队列里超上限失败的投递重新入队投递一次
  async function retryDeadLetter(row: OutboundView) {
    const r = await fetch(`/api/v1/admin/channel-dlq/${row.id}/retry`, { method: 'POST', headers: auth() })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '重发失败')
    else MessagePlugin.success('已重发')
    load()
  }

  // 通道表格列定义（类型/名称/标识/状态/凭据掩码/操作）
  const cols = [
    { colKey: 'type', title: '类型', width: 130, cell: (p: CellProps) => <Tag theme="primary">{CHANNEL_TYPE_LABELS[p.row.type] || p.row.type}</Tag> },
    { colKey: 'name', title: '名称', width: 160 },
    { colKey: 'identity', title: '标识', width: 220, cell: (p: CellProps) => <span className="text-xs text-gray-500">{p.row.corpid || p.row.appid || '-'}</span> },
    { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme={CHANNEL_STATUS_THEMES[p.row.status] || 'warning'}>{CHANNEL_STATUS_LABELS[p.row.status] || p.row.status}</Tag> },
    { colKey: 'secrets', title: '凭据掩码', width: 260, cell: (p: CellProps) => <span className="text-xs text-gray-400">S:{p.row.secret_mask || '-'} · T:{p.row.token_mask || '-'} · A:{p.row.aeskey_mask || '-'}</span> },
    { colKey: 'op', title: '操作', width: 260, cell: (p: CellProps) => (
      <div className="flex gap-2">
        <Button size="small" variant="outline" onClick={() => verify(p.row as ChannelView)}>测试</Button>
        {p.row.status === 'active'
          ? <Button size="small" variant="outline" theme="warning" onClick={() => setStatus(p.row as ChannelView, 'disabled')}>停用</Button>
          : <Button size="small" variant="outline" theme="success" onClick={() => setStatus(p.row as ChannelView, 'active')}>启用</Button>}
        <Button size="small" variant="text" onClick={() => openEdit(p.row as ChannelView)}>编辑</Button>
        <Button size="small" variant="text" theme="danger" onClick={() => del(p.row as ChannelView)}>删除</Button>
      </div>
    ) },
  ]
  // 出站死信表格列定义（含重发操作按钮）
  const dlqCols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'channel_id', title: '通道', width: 80 },
    { colKey: 'content', title: '内容', width: 280, ellipsis: true },
    { colKey: 'retries', title: '重试', width: 70 },
    { colKey: 'error', title: '原因', width: 220, ellipsis: true },
    { colKey: 'op', title: '操作', width: 90, cell: (p: CellProps) => <Button size="small" theme="primary" onClick={() => retryDeadLetter(p.row as OutboundView)}>重发</Button> },
  ]

  return (
    <div className="space-y-6">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div style={{ fontSize: 13, color: '#6b7280' }}>
          通道凭据只保存密文，列表仅展示掩码；创建成功后明文只显示一次。入站回调地址请填到企微/公众号后台，签名验证由后端处理。
        </div>
        <div className="flex gap-2">
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? '刷新中…' : '刷新'}</Button>
          <Button theme="primary" onClick={openCreate}>+ 新增通道</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={channels} columns={cols} size="small" empty="暂无通道" loading={loading} />
      </div>

      <div className="bg-white rounded-lg shadow-sm p-4">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <h3 className="text-base font-bold text-gray-800">出站死信</h3>
            <p className="text-xs text-gray-400 mt-0.5">通道投递失败超过重试上限后进入这里，修复通道后可人工重发。</p>
          </div>
          <Button size="small" variant="outline" onClick={load}>刷新</Button>
        </div>
        <Table rowKey="id" data={dlq} columns={dlqCols} size="small" empty="暂无死信" loading={loading} />
      </div>

      <Dialog
        header={editingId ? '编辑通道' : '新增通道'}
        visible={!!form}
        onClose={closeForm}
        onConfirm={submit}
        confirmBtn={saving ? '保存中…' : '保存'}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">通道类型</label>
              <Select
                value={form.type}
                onChange={(v) => setF('type', String(v))}
                options={[
                  { label: '企业微信自建应用', value: 'wecom_app' },
                  { label: '微信客服', value: 'wecom_kf' },
                  { label: '微信公众号', value: 'wechat_mp' },
                ]}
                style={{ width: '100%' }}
              />
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div><label className="block text-xs text-gray-500 mb-1">名称</label><Input value={form.name} onChange={(v) => setF('name', v)} placeholder="顾问可见的通道名称" /></div>
              <div><label className="block text-xs text-gray-500 mb-1">部门 ID（可选）</label><Input value={form.department_id} onChange={(v) => setF('department_id', v)} placeholder="用于数据范围，默认 0" /></div>
              {requireWecomCorpid(form.type) && <div><label className="block text-xs text-gray-500 mb-1">CorpID</label><Input value={form.corpid} onChange={(v) => setF('corpid', v)} /></div>}
              {form.type === 'wecom_app' && <div><label className="block text-xs text-gray-500 mb-1">AgentID</label><Input value={form.agentid} onChange={(v) => setF('agentid', v)} /></div>}
              {form.type === 'wechat_mp' && <div><label className="block text-xs text-gray-500 mb-1">AppID</label><Input value={form.appid} onChange={(v) => setF('appid', v)} /></div>}
            </div>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <div><label className="block text-xs text-gray-500 mb-1">Secret {editingId ? '（留空不改）' : ''}</label><Input value={form.secret} onChange={(v) => setF('secret', v)} type="password" /></div>
              <div><label className="block text-xs text-gray-500 mb-1">Token {editingId ? '（留空不改）' : ''}</label><Input value={form.token} onChange={(v) => setF('token', v)} type="password" /></div>
              <div><label className="block text-xs text-gray-500 mb-1">EncodingAESKey {editingId ? '（留空不改）' : ''}</label><Input value={form.encoding_aes_key} onChange={(v) => setF('encoding_aes_key', v)} type="password" /></div>
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">扩展配置 JSON</label>
              <Textarea value={form.config_json} onChange={(v) => setF('config_json', v)} autosize={{ minRows: 3, maxRows: 8 }} placeholder='{"base_url":"https://qyapi.weixin.qq.com"}' />
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        header="创建成功：请保存一次性凭据"
        visible={!!created}
        onClose={() => setCreated(null)}
        footer={<Button theme="primary" onClick={() => setCreated(null)}>我已保存</Button>}
        width="680px"
      >
        {created && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">回调地址</label>
              <Input readOnly value={location.origin + (created.callback_url || '')} />
            </div>
            <div className="grid grid-cols-1 gap-2">
              <div><label className="block text-xs text-gray-500 mb-1">Secret</label><Input readOnly value={created.plaintext?.secret || ''} /></div>
              <div><label className="block text-xs text-gray-500 mb-1">Token</label><Input readOnly value={created.plaintext?.token || ''} /></div>
              <div><label className="block text-xs text-gray-500 mb-1">EncodingAESKey</label><Input readOnly value={created.plaintext?.encoding_aes_key || ''} /></div>
            </div>
            <p className="text-xs text-orange-600">这些明文之后不再可从列表读取；关闭后只能重新编辑覆盖凭据。</p>
          </div>
        )}
      </Dialog>
    </div>
  )
}
