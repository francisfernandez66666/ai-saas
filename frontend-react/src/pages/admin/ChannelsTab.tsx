// F11 通道接入 Tab（F1 从 Admin.tsx 拆出）：通道凭据 CRUD、连通测试与出站死信重发。
import { useCallback, useEffect, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Dialog, Input, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import { apiFetch } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps } from '../../types'

// ChannelView 通道接入 Tab 的通道列表行：密钥类字段后端只回掩码，不可回读。
type ChannelView = {
  id: number; type: string; name: string; corpid: string; appid: string; status: string;
  department_id: number; secret_mask: string; token_mask: string; aeskey_mask: string; config_json: string; created_at: string
}
// OutboundView 出站死信列表行：一次出站消息的投递状态、重试次数与下次重试时间。
type OutboundView = {
  id: number; channel_id: number; customer_id: number; conversation_id: number; content: string;
  status: string; retries: number; error: string; created_at: string; next_retry_at?: string | null
}
// ChannelForm 通道新建/编辑弹窗的本地表单态：密钥字段创建时必填，编辑态留空即不改动。
type ChannelForm = {
  type: string; name: string; corpid: string; appid: string; agentid: string; secret: string;
  token: string; encoding_aes_key: string; department_id: string; config_json: string
}
// ApiResp 本文件接口调用的标准信封：code/message 为状态位，data 为业务负载。
type ApiResp<T> = { code: number; message?: string; data?: T }
// CreateChannelResp 新建通道接口的 data：通道本体加一次性明文密钥与回调地址（仅创建时返回）。
type CreateChannelResp = { channel: ChannelView; plaintext?: { secret?: string; token?: string; encoding_aes_key?: string }; callback_url?: string }

// P2-14：通道类型/状态的 key 全是后端契约值（不翻译），只把展示名换成 I18nKey，
// 真正翻译发生在组件渲染时——模块级常量拿不到 t()。
const CHANNEL_TYPE_KEYS: Record<string, I18nKey> = {
  wecom_app: 'channel.typeWecomApp', wecom_kf: 'channel.typeWecomKf', wechat_mp: 'channel.typeWechatMp',
}
const CHANNEL_STATUS_KEYS: Record<string, I18nKey> = {
  active: 'channel.statusActive', disabled: 'channel.statusDisabled', unverified: 'channel.statusUnverified',
}
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
  const { t } = useI18n()
  const [channels, setChannels] = useState<ChannelView[]>([])
  const [dlq, setDlq] = useState<OutboundView[]>([])
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<ChannelForm | null>(null)
  const [editingId, setEditingId] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [created, setCreated] = useState<CreateChannelResp | null>(null)

  // 统一鉴权头（Authorization + 超管代管时 X-Tenant-ID）
  // C3(PLAN_FIX_2026-09-21)：统一请求层收口——auth() 手工拼 Authorization 改为 apiFetch，
  // 后者自带 token / 超管代管 X-Tenant-ID / 30s 超时 / 401 登出 / 403 分流。

  // 并行拉取通道列表与出站死信列表，回填两个表格。
  // 必须 useCallback：效果体依赖 [load] 才会稳定——load 若是每次渲染新建的闭包，
  // 依赖数组每轮都变，effect 每轮重跑，setLoading 又触发下一轮渲染，就是无限重渲染
  // （React "Maximum update depth exceeded"，首跑即被 ChannelsTab 单测的告警刷屏抓住）。
  const load = useCallback(async () => {
    setLoading(true)
    try {
      const [cr, dr] = await Promise.all([
        apiFetch('/api/v1/admin/channels'),
        apiFetch('/api/v1/admin/channel-dlq'),
      ])
      const cj = (await cr.json().catch(() => null)) as ApiResp<{ list: ChannelView[] }> | null
      const dj = (await dr.json().catch(() => null)) as ApiResp<{ list: OutboundView[] }> | null
      if (cj?.code === 0) setChannels(cj.data?.list || [])
      else MessagePlugin.warning(cj?.message || t('channel.listLoadFailed'))
      if (dj?.code === 0) setDlq(dj.data?.list || [])
    } finally {
      setLoading(false)
    }
  }, [t])
  useEffect(() => { load() }, [load])

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
    if (!form.name.trim()) { MessagePlugin.warning(t('channel.nameRequired')); return }
    if (requireWecomCorpid(form.type) && !form.corpid.trim()) { MessagePlugin.warning(t('channel.corpidRequired')); return }
    if (form.type === 'wechat_mp' && !form.appid.trim()) { MessagePlugin.warning(t('channel.appidRequired')); return }
    if (!editingId && !form.secret.trim()) { MessagePlugin.warning(t('channel.secretRequired')); return }
    let cfg = form.config_json.trim() || '{}'
    try {
      const parsed = JSON.parse(cfg)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('bad')
      cfg = JSON.stringify(parsed)
    } catch {
      MessagePlugin.warning(t('channel.cfgJsonInvalid')); return
    }
    const body = {
      type: form.type, name: form.name.trim(), corpid: form.corpid.trim(), appid: form.appid.trim(), agentid: form.agentid.trim(),
      secret: form.secret.trim(), token: form.token.trim(), encoding_aes_key: form.encoding_aes_key.trim(),
      department_id: Number(form.department_id || 0) || undefined, config_json: cfg,
    }
    setSaving(true)
    try {
      const url = editingId ? `/api/v1/admin/channels/${editingId}` : '/api/v1/admin/channels'
      const r = await apiFetch(url, { method: editingId ? 'PUT' : 'POST', body: JSON.stringify(body) })
      const j = (await r.json().catch(() => null)) as ApiResp<CreateChannelResp | null> | null
      if (j?.code !== 0) { MessagePlugin.error(j?.message || t('channel.saveFailed')); return }
      if (!editingId) {
        setCreated(j.data || null)
        MessagePlugin.success(t('channel.createdOk'))
      } else {
        MessagePlugin.success(t('channel.updatedOk'))
      }
      closeForm()
      load()
    } finally {
      setSaving(false)
    }
  }

  // 连通测试：调后端 verify 端点试拿 access_token，成功/失败提示并刷新列表状态
  async function verify(ch: ChannelView) {
    const r = await apiFetch(`/api/v1/admin/channels/${ch.id}/verify`, { method: 'POST' })
    const j = (await r.json().catch(() => null)) as ApiResp<{ ok?: boolean; detail?: string }> | null
    if (j?.code === 0 && j.data?.ok) MessagePlugin.success(t('channel.verifyOk'))
    else MessagePlugin.warning(j?.data?.detail || j?.message || t('channel.verifyFailed'))
    load()
  }
  // 启用/停用通道（active↔disabled），停用后入站回调与出站投递不再处理
  async function setStatus(ch: ChannelView, status: 'active' | 'disabled') {
    const r = await apiFetch(`/api/v1/admin/channels/${ch.id}/status?status=${status}`, { method: 'PUT' })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('channel.statusUpdateFailed'))
    load()
  }
  // 删除通道：二次确认后调 DELETE，回调地址随即失效（提示已写明）
  async function del(ch: ChannelView) {
    if (!(await confirmDialog(t('channel.deleteConfirm', { name: ch.name })))) return
    const r = await apiFetch(`/api/v1/admin/channels/${ch.id}`, { method: 'DELETE' })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('channel.deleteFailed'))
    else MessagePlugin.success(t('channel.deletedOk'))
    load()
  }
  // 死信重发：把出站队列里超上限失败的投递重新入队投递一次
  async function retryDeadLetter(row: OutboundView) {
    const r = await apiFetch(`/api/v1/admin/channel-dlq/${row.id}/retry`, { method: 'POST' })
    const j = (await r.json().catch(() => null)) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('channel.retryFailed'))
    else MessagePlugin.success(t('channel.retriedOk'))
    load()
  }

  // 通道表格列定义（类型/名称/标识/状态/凭据掩码/操作）
  const cols = [
    { colKey: 'type', title: t('common.type'), width: 130, cell: (p: CellProps) => { const tk = CHANNEL_TYPE_KEYS[String(p.row.type)]; return <Tag theme="primary">{tk ? t(tk) : p.row.type}</Tag> } },
    { colKey: 'name', title: t('common.name'), width: 160 },
    { colKey: 'identity', title: t('channel.colIdentity'), width: 220, cell: (p: CellProps) => <span className="text-xs text-gray-500">{p.row.corpid || p.row.appid || '-'}</span> },
    { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => { const sk = CHANNEL_STATUS_KEYS[String(p.row.status)]; return <Tag theme={CHANNEL_STATUS_THEMES[p.row.status] || 'warning'}>{sk ? t(sk) : p.row.status}</Tag> } },
    { colKey: 'secrets', title: t('channel.colSecretMask'), width: 260, cell: (p: CellProps) => <span className="text-xs text-gray-400">S:{p.row.secret_mask || '-'} · T:{p.row.token_mask || '-'} · A:{p.row.aeskey_mask || '-'}</span> },
    { colKey: 'op', title: t('crud.actions'), width: 260, cell: (p: CellProps) => (
      <div className="flex gap-2">
        <Button size="small" variant="outline" onClick={() => verify(p.row as ChannelView)}>{t('channel.test')}</Button>
        {p.row.status === 'active'
          ? <Button size="small" variant="outline" theme="warning" onClick={() => setStatus(p.row as ChannelView, 'disabled')}>{t('common.disable')}</Button>
          : <Button size="small" variant="outline" theme="success" onClick={() => setStatus(p.row as ChannelView, 'active')}>{t('common.enable')}</Button>}
        <Button size="small" variant="text" onClick={() => openEdit(p.row as ChannelView)}>{t('crud.edit')}</Button>
        <Button size="small" variant="text" theme="danger" onClick={() => del(p.row as ChannelView)}>{t('crud.delete')}</Button>
      </div>
    ) },
  ]
  // 出站死信表格列定义（含重发操作按钮）
  const dlqCols = [
    { colKey: 'id', title: t('common.id'), width: 70 },
    { colKey: 'channel_id', title: t('channel.colChannel'), width: 80 },
    { colKey: 'content', title: t('channel.colContent'), width: 280, ellipsis: true },
    { colKey: 'retries', title: t('channel.colRetries'), width: 70 },
    { colKey: 'error', title: t('channel.colReason'), width: 220, ellipsis: true },
    { colKey: 'op', title: t('crud.actions'), width: 90, cell: (p: CellProps) => <Button size="small" theme="primary" onClick={() => retryDeadLetter(p.row as OutboundView)}>{t('channel.retry')}</Button> },
  ]

  return (
    <div className="space-y-6">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div style={{ fontSize: 13, color: '#6b7280' }}>
          {t('channel.intro')}
        </div>
        <div className="flex gap-2">
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? t('channel.refreshing') : t('crud.refresh')}</Button>
          <Button theme="primary" onClick={openCreate}>{t('channel.createBtn')}</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={channels} columns={cols} size="small" empty={t('channel.empty')} loading={loading} />
      </div>

      <div className="bg-white rounded-lg shadow-sm p-4">
        <div className="mb-3 flex items-center justify-between">
          <div>
            <h3 className="text-base font-bold text-gray-800">{t('channel.dlqTitle')}</h3>
            <p className="text-xs text-gray-400 mt-0.5">{t('channel.dlqDesc')}</p>
          </div>
          <Button size="small" variant="outline" onClick={load}>{t('crud.refresh')}</Button>
        </div>
        <Table rowKey="id" data={dlq} columns={dlqCols} size="small" empty={t('channel.dlqEmpty')} loading={loading} />
      </div>

      <Dialog
        header={editingId ? t('channel.editTitle') : t('channel.createTitle')}
        visible={!!form}
        onClose={closeForm}
        onConfirm={submit}
        confirmBtn={saving ? t('crud.saving') : t('crud.save')}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('channel.typeLabel')}</label>
              <Select
                value={form.type}
                onChange={(v) => setF('type', String(v))}
                options={[
                  { label: t('channel.typeWecomApp'), value: 'wecom_app' },
                  { label: t('channel.typeWecomKf'), value: 'wecom_kf' },
                  { label: t('channel.typeWechatMp'), value: 'wechat_mp' },
                ]}
                style={{ width: '100%' }}
              />
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
              <div><label className="block text-xs text-gray-500 mb-1">{t('common.name')}</label><Input value={form.name} onChange={(v) => setF('name', v)} placeholder={t('channel.namePh')} /></div>
              <div><label className="block text-xs text-gray-500 mb-1">{t('channel.deptIdLabel')}</label><Input value={form.department_id} onChange={(v) => setF('department_id', v)} placeholder={t('channel.deptIdPh')} /></div>
              {requireWecomCorpid(form.type) && <div><label className="block text-xs text-gray-500 mb-1">CorpID</label><Input value={form.corpid} onChange={(v) => setF('corpid', v)} /></div>}
              {form.type === 'wecom_app' && <div><label className="block text-xs text-gray-500 mb-1">AgentID</label><Input value={form.agentid} onChange={(v) => setF('agentid', v)} /></div>}
              {form.type === 'wechat_mp' && <div><label className="block text-xs text-gray-500 mb-1">AppID</label><Input value={form.appid} onChange={(v) => setF('appid', v)} /></div>}
            </div>
            <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
              <div><label className="block text-xs text-gray-500 mb-1">Secret{editingId ? t('channel.keepBlank') : ''}</label><Input value={form.secret} onChange={(v) => setF('secret', v)} type="password" /></div>
              <div><label className="block text-xs text-gray-500 mb-1">Token{editingId ? t('channel.keepBlank') : ''}</label><Input value={form.token} onChange={(v) => setF('token', v)} type="password" /></div>
              <div><label className="block text-xs text-gray-500 mb-1">EncodingAESKey{editingId ? t('channel.keepBlank') : ''}</label><Input value={form.encoding_aes_key} onChange={(v) => setF('encoding_aes_key', v)} type="password" /></div>
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('channel.cfgJsonLabel')}</label>
              <Textarea value={form.config_json} onChange={(v) => setF('config_json', v)} autosize={{ minRows: 3, maxRows: 8 }} placeholder='{"base_url":"https://qyapi.weixin.qq.com"}' />
            </div>
          </div>
        )}
      </Dialog>

      <Dialog
        header={t('channel.createdTitle')}
        visible={!!created}
        onClose={() => setCreated(null)}
        footer={<Button theme="primary" onClick={() => setCreated(null)}>{t('channel.savedPlaintext')}</Button>}
        width="680px"
      >
        {created && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('channel.callbackUrl')}</label>
              <Input readOnly value={location.origin + (created.callback_url || '')} />
            </div>
            <div className="grid grid-cols-1 gap-2">
              <div><label className="block text-xs text-gray-500 mb-1">Secret</label><Input readOnly value={created.plaintext?.secret || ''} /></div>
              <div><label className="block text-xs text-gray-500 mb-1">Token</label><Input readOnly value={created.plaintext?.token || ''} /></div>
              <div><label className="block text-xs text-gray-500 mb-1">EncodingAESKey</label><Input readOnly value={created.plaintext?.encoding_aes_key || ''} /></div>
            </div>
            <p className="text-xs text-orange-600">{t('channel.plaintextWarn')}</p>
          </div>
        )}
      </Dialog>
    </div>
  )
}
