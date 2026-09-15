// 客户线索 Tab（F1/F10）：阶段筛选、列表、详情抽屉、编辑、打标、导出。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Drawer, Input, InputNumber, MessagePlugin, Select, Tag, Textarea } from 'tdesign-react'
import { apiFetch, AUTH, getToken } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { CrudRow } from '../../hooks/useCrud'
import type { TableRowData } from '../../types'

const STAGE_LABELS: Record<string, string> = { ai_connected: 'AI建联', human_connected: '人工建联', lead_captured: '已留资', arrived: '已到店', ordered: '已下单', delivered: '已交车', lost: '已战败' }
const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }
const STATUS_OPTS = ['', 'ai_connected', 'human_connected', 'lead_captured', 'arrived', 'ordered', 'delivered', 'lost']
const STAGE_OPTS = [
  { label: 'AI建联', value: 'ai_connected' }, { label: '人工建联', value: 'human_connected' }, { label: '已留资', value: 'lead_captured' },
  { label: '已到店', value: 'arrived' }, { label: '已下单', value: 'ordered' }, { label: '已交车', value: 'delivered' }, { label: '已战败', value: 'lost' },
]
const USER_OPTS = [{ label: '未分配', value: 0 }]
const STATUS_EDIT_OPTS = [{ label: '有效', value: 1 }, { label: '停用', value: 0 }]
const SOURCE_OPTS = [{ label: '手工录入', value: 'manual' }, { label: '广告投放', value: 'ad' }, { label: '老客推荐', value: 'referral' }, { label: '到店自然', value: 'walkin' }, { label: '线上咨询', value: 'online' }]

/** 清洗客户展示名，访客占位名统一显示为客户。 */
function nameOf(v?: string) { return !v || v.startsWith('访客_') ? '客户' : v }
/** 取客户名首字作为头像占位符。 */
function initialOf(v?: string) { return !v || v.startsWith('访客_') ? '客' : (v?.[0] || '?') }

/** 触发浏览器下载后台导出的 CSV 文件。 */
async function downloadCsv(url: string, filename: string) {
  const res = await apiFetch(url)
  if (!res.ok) { MessagePlugin.error('导出失败'); return }
  const blob = await res.blob()
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(a.href)
  MessagePlugin.success('已开始下载')
}

/** 客户列表 Tab：支持筛选、标签、阶段和 CSV 导出。 */
export function CustomersTab() {
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(1)
  const [total, setTotal] = useState(0)
  const [leads, setLeads] = useState<TableRowData[]>([])
  const [loading, setLoading] = useState(false)
  const [detail, setDetail] = useState<TableRowData | null>(null)
  const [drawer, setDrawer] = useState(false)
  const [testDrives, setTestDrives] = useState<TableRowData[]>([])
  const [chat, setChat] = useState<TableRowData[]>([])
  const [showTd, setShowTd] = useState(false)
  const [showChat, setShowChat] = useState(false)
  const [editVisible, setEditVisible] = useState(false)
  const [tagVisible, setTagVisible] = useState(false)
  const [form, setForm] = useState<Record<string, any>>({})
  const [tagForm, setTagForm] = useState<string[]>([])
  const [tagOptions, setTagOptions] = useState<{ label: string; value: string }[]>([])
  const [users, setUsers] = useState<{ label: string; value: number }[]>(USER_OPTS)
  const [saving, setSaving] = useState(false)
  const [createVisible, setCreateVisible] = useState(false)
  const [create, setCreate] = useState<Record<string, any>>({ name: '', phone: '', city: '', interest_model: '', budget: 0, source: 'manual', assigned_user_id: 0, remark: '' })

  const H = nameOf
  const HI = initialOf

  // 拉取客户线索列表（阶段筛选 + 分页 + 归属=all），useCallback 随筛选/页码重建
  const load = useCallback(async () => {
    setLoading(true)
    const r = await apiFetch(`/api/v1/advisor/customers?status=${filter}&page=${page}&page_size=20&assigned=all`)
    const j = await r.json().catch(() => null)
    if (j?.code === 0) { setLeads(j.data?.list || []); setTotal(j.data?.total || (j.data?.list || []).length) }
    setLoading(false)
  }, [filter, page])

  useEffect(() => { void load() }, [load])
  useEffect(() => {
    ;(async () => {
      const [t, u] = await Promise.all([AUTH('/api/v1/admin/tags?page_size=200'), AUTH('/api/v1/org/users')])
      if (t?.code === 0) setTagOptions((t.data?.list || []).map((x: CrudRow) => ({ label: `${x.name}（${x.code || x.id}）`, value: String(x.name) })))
      if (u?.code === 0) setUsers([...USER_OPTS, ...(u.data || []).map((x: CrudRow) => ({ label: x.real_name || x.username || `用户${x.id}`, value: Number(x.id) }))])
    })()
  }, [])

  // 打开客户详情抽屉，并重置试驾/聊天面板展开态
  async function openDetail(id: number) {
    const r = await apiFetch(`/api/v1/advisor/customer/${id}`)
    const j = await r.json().catch(() => null)
    if (j?.code === 0) {
      setDetail(j.data); setDrawer(true); setShowTd(false); setShowChat(false); setTestDrives([]); setChat([])
    } else MessagePlugin.error('获取详情失败')
  }

  // 展开或收起该客户的试驾记录（再次点击收起，不重复拉取）
  async function loadTd() {
    if (showTd) { setShowTd(false); return }
    const cid = detail?.customer?.id
    const j = await AUTH(`/api/v1/advisor/test-drives?customer_id=${cid}`)
    setTestDrives(j?.code === 0 ? (j.data || []) : []); setShowTd(true)
  }
  // 展开或收起该客户的最近聊天记录
  async function loadChat() {
    if (showChat) { setShowChat(false); return }
    const cid = detail?.customer?.id
    const j = await AUTH(`/api/v1/chat/history?customer_id=${cid}&limit=30`)
    setChat(j?.code === 0 ? (j.data || []) : []); setShowChat(true)
  }

  // 用当前详情预填编辑表单并打开编辑弹窗
  function openEdit() {
    const c = detail?.customer
  // 聊天发送方中文名/颜色与编辑表单受控更新的小工具
 || {}
    setForm({
      name: c.name || '',
      phone: c.phone || '',
      age: c.age || 0,
      gender: c.gender || 0,
      city: c.city || '',
      region: c.region || '',
      career: c.career || '',
      interest_model: c.interest_model || '',
      budget: c.budget || 0,
      intent_score: c.intent_score || 0,
      journey_stage: c.journey_stage || '',
      assigned_user_id: c.assigned_user_id || 0,
      status: c.status ?? 1,
      remark: c.remark || '',
    })
    setEditVisible(true)
  }

  // 提交客户资料编辑（PUT info），成功后刷新详情与列表
  async function submitEdit() {
    if (!detail?.customer?.id) return
    setSaving(true)
    const cid = Number(detail.customer.id)
    const info = await AUTH(`/api/v1/advisor/customer/${cid}/info`, {
      method: 'PUT',
      body: {
        name: form.name || undefined,
        phone: form.phone || undefined,
        age: Number(form.age) || undefined,
        gender: Number(form.gender) || undefined,
        city: form.city || undefined,
        region: form.region || undefined,
        career: form.career || undefined,
        interest_model: form.interest_model || undefined,
        budget: Number(form.budget) || undefined,
        journey_stage: form.journey_stage || undefined,
        remark: form.remark || undefined,
      },
    })
    const cust = await AUTH(`/api/v1/customers/${cid}`, {
      method: 'PUT',
      body: {
        intent_score: Number(form.intent_score) || undefined,
        assigned_user_id: Number(form.assigned_user_id) || undefined,
        status: Number(form.status) || undefined,
      },
    })
    setSaving(false)
    if (info?.code === 0 || cust?.code === 0) {
      MessagePlugin.success('客户信息已更新')
      setEditVisible(false)
      await openDetail(cid)
      await load()
    }
  }

  // 打开标签编辑器：以详情现有标签预填
  function openTagEditor() {
    setTagForm((detail?.tags || []).map((t: CrudRow) => String(t.tag_name)).filter(Boolean))
    setTagVisible(true)
  }

  // 提交客户标签覆盖式更新，成功后重开详情并刷新列表
  async function submitTags() {
    if (!detail?.customer?.id) return
    setSaving(true)
    const j = await AUTH(`/api/v1/advisor/customer/${Number(detail.customer.id)}/tags`, {
      method: 'PUT',
      body: { tags: tagForm },
    })
    setSaving(false)
    if (j?.code === 0) {
      MessagePlugin.success('标签已更新')
      setTagVisible(false)
      await openDetail(Number(detail.customer.id))
      await load()
    }
  }

  // 新建线索（手工建客）：姓名/手机号至少一项，成功后关窗清空并刷新
  async function submitCreate() {
    if (!String(create.name || '').trim() && !String(create.phone || '').trim()) { MessagePlugin.error('姓名或手机号至少填一项'); return }
    setSaving(true)
    const j = await AUTH('/api/v1/customers', {
      method: 'POST',
      body: {
        name: create.name || undefined,
        phone: create.phone || undefined,
        city: create.city || undefined,
        interest_model: create.interest_model || undefined,
        budget: Number(create.budget) || undefined,
        source: create.source || undefined,
        assigned_user_id: Number(create.assigned_user_id) || undefined,
        remark: create.remark || undefined,
      },
    })
    setSaving(false)
    if (j?.code === 0) {
      MessagePlugin.success('线索已创建')
      setCreateVisible(false)
      setCreate({ name: '', phone: '', city: '', interest_model: '', budget: 0, source: 'manual', assigned_user_id: 0, remark: '' })
      await load()
    } else MessagePlugin.error(j?.message || '创建失败')
  }

  // 删除客户线索（二次确认；会话历史按合规保留），成功后关抽屉刷新
  async function deleteCustomer(id: number) {
    if (!(await confirmDialog('删除后该线索及其标签关系将从列表移除（会话历史按合规保留）。确认删除？', '删除客户线索'))) return
    const j = await AUTH(`/api/v1/customers/${id}`, { method: 'DELETE' })
    if (j?.code === 0) {
      MessagePlugin.success('已删除')
      setDrawer(false)
      await load()
    } else MessagePlugin.error(j?.message || '删除失败')
  }

  const c = detail?.customer
  const who = (t: string) => (t === 'customer' ? '客户' : t === 'ai' ? 'AI' : t === 'human' ? '人工' : '系统')
  const wcolor = (t: string) => (t === 'customer' ? 'text-blue-600' : t === 'ai' ? 'text-green-600' : 'text-orange-600')
  const setField = (k: string, v: any) => setForm((s) => ({ ...s, [k]: v }))

  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
        <span className="text-sm text-gray-500">筛选阶段：</span>
        <select value={filter} onChange={(e) => { setFilter((e.target as HTMLSelectElement).value); setPage(1) }} className="px-3 py-2 border rounded-lg text-sm">
          {STATUS_OPTS.map((s) => <option key={s} value={s}>{s === '' ? '全部' : (STAGE_LABELS[s] || s)}</option>)}
        </select>
        <Button theme="primary" onClick={() => setCreateVisible(true)}>+ 新建线索</Button>
        <Button variant="outline" onClick={() => { setPage(1); void load() }}>刷新</Button>
        <Button variant="outline" theme="success" onClick={() => void downloadCsv('/api/v1/admin/export/customers.csv', 'customers.csv')}>导出客户</Button>
        <span className="text-xs text-gray-400">共 {total || leads.length} 条</span>
      </div>
      <div className="space-y-2">
        {loading && <div className="text-center text-gray-400 py-6">加载中...</div>}
        {!loading && leads.length === 0 && <div className="text-center text-gray-400 py-6">暂无客户线索</div>}
        {leads.map((l) => (
          <div key={l.id} className="bg-white rounded-lg border border-gray-100 p-3 hover:bg-gray-50 flex items-center justify-between gap-3">
            <div className="flex items-center gap-3 cursor-pointer min-w-0" onClick={() => openDetail(l.id)}>
              <div className="w-9 h-9 bg-gray-100 rounded-full flex items-center justify-center text-sm font-medium text-gray-500">{HI(l.name)}</div>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-gray-800">{H(l.name)}</span>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded-full ${STAGE_COLORS[l.journey_stage] || 'bg-gray-100 text-gray-500'}`}>{STAGE_LABELS[l.journey_stage] || l.journey_stage || '-'}</span>
                  {l.assigned_user_name && <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-indigo-100 text-indigo-600">{l.assigned_user_name}</span>}
                </div>
                <p className="text-xs text-gray-400 truncate">{l.phone || ''} {l.interest_model ? '· ' + l.interest_model : ''}</p>
              </div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              <div className="text-right hidden md:block">
                <p className="text-xs text-gray-400 truncate max-w-[240px]">{l.last_message ? l.last_message.slice(0, 30) + '...' : '暂无消息'}</p>
                <p className="text-[10px] text-gray-300">{l.updated_at ? new Date(l.updated_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''}</p>
              </div>
              <Button size="small" variant="text" onClick={() => openDetail(l.id)}>详情</Button>
            </div>
          </div>
        ))}
      </div>

      <Drawer visible={drawer} onClose={() => setDrawer(false)} size="560px" header="客户线索详情">
        {c && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Button size="small" theme="primary" onClick={openEdit}>编辑资料</Button>
              <Button size="small" theme="success" variant="outline" onClick={openTagEditor}>管理标签</Button>
              <Button size="small" variant="outline" onClick={() => void downloadCsv(`/api/v1/admin/export/conversations.csv?customer_id=${c.id}`, `customer_${c.id}_conversations.csv`)}>导出会话</Button>
              <Button size="small" theme="danger" variant="outline" onClick={() => void deleteCustomer(Number(c.id))}>删除线索</Button>
            </div>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div><span className="text-xs text-gray-400">姓名</span><p className="text-sm font-medium">{H(c.name)}</p></div>
              <div><span className="text-xs text-gray-400">手机</span><p className="text-sm font-medium">{c.phone || '-'}</p></div>
              <div><span className="text-xs text-gray-400">兴趣产品</span><p className="text-sm">{c.interest_model || '-'}</p></div>
              <div><span className="text-xs text-gray-400">预算</span><p className="text-sm">{c.budget > 0 ? c.budget + '万' : '-'}</p></div>
              <div><span className="text-xs text-gray-400">意向分</span><p className="text-sm">{((c.intent_score || 0) * 100).toFixed(0)}%</p></div>
              <div><span className="text-xs text-gray-400">城市</span><p className="text-sm">{c.city || '-'}</p></div>
              <div><span className="text-xs text-gray-400">阶段</span><p className="text-sm">{STAGE_LABELS[c.journey_stage] || c.journey_stage || '-'}</p></div>
              <div><span className="text-xs text-gray-400">顾问</span><p className="text-sm">{detail.assigned_user_name || (c.assigned_user_id > 0 ? '顾问' + c.assigned_user_id : '未分配')}</p></div>
            </div>
            <div><span className="text-xs text-gray-400">标签</span><div className="mt-1 flex flex-wrap gap-1">{(detail.tags || []).map((t: TableRowData, i: number) => <Tag key={i} theme="primary" variant="light">{t.tag_name}</Tag>)} {(detail.tags || []).length === 0 && <span className="text-xs text-gray-300">暂无</span>}</div></div>
            <div><span className="text-xs text-gray-400">备注</span><p className="text-sm text-gray-600 mt-1">{c.remark || '-'}</p></div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="success" variant="outline" onClick={() => void loadTd()}>{showTd ? '🚗 隐藏试驾单' : '🚗 查看试驾单'}</Button>
              <div className="mt-2 space-y-2">
                {showTd && testDrives.length === 0 && <div className="text-gray-400 text-xs">暂无试驾记录</div>}
                {testDrives.map((td, i) => (
                  <div key={i} className="bg-gray-50 rounded-lg p-2 border border-gray-100">
                    <div className="flex items-center justify-between"><span className="text-xs font-medium text-gray-700">{td.model_name || '试驾'}</span><span className="text-[10px] text-gray-500">{td.status === 'pending' ? '待试驾' : td.status === 'completed' ? '已完成' : td.status === 'cancelled' ? '已取消' : td.status}</span></div>
                    <p className="text-xs text-gray-400 mt-0.5">{td.scheduled_at ? new Date(td.scheduled_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''} {td.location ? '· ' + td.location : ''}</p>
                  </div>
                ))}
              </div>
            </div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="primary" variant="outline" onClick={() => void loadChat()}>{showChat ? '💬 隐藏聊天记录' : '💬 查看聊天记录'}</Button>
              <div className="mt-2 max-h-48 overflow-y-auto bg-gray-50 rounded-lg p-3 text-xs space-y-1">
                {showChat && chat.length === 0 && <div className="text-gray-400">暂无聊天记录</div>}
                {chat.map((m, i) => <div key={i}><span className={`${wcolor(m.sender_type)} font-medium`}>[{who(m.sender_type)}]</span> {m.content}</div>)}
              </div>
            </div>
          </div>
        )}
      </Drawer>

      <Dialog header="编辑客户资料" visible={editVisible} onClose={() => setEditVisible(false)} onConfirm={() => void submitEdit()} confirmBtn={saving ? '保存中…' : '保存'} width="680px">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div><label className="block text-xs text-gray-500 mb-1">姓名</label><Input value={form.name} onChange={(v) => setField('name', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">手机</label><Input value={form.phone} onChange={(v) => setField('phone', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">城市</label><Input value={form.city} onChange={(v) => setField('city', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">兴趣产品</label><Input value={form.interest_model} onChange={(v) => setField('interest_model', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">预算（万元）</label><InputNumber value={Number(form.budget || 0)} onChange={(v) => setField('budget', v)} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">意向分（0-1）</label><InputNumber value={Number(form.intent_score || 0)} onChange={(v) => setField('intent_score', v)} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">阶段</label><Select value={form.journey_stage} onChange={(v) => setField('journey_stage', v)} options={STAGE_OPTS} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">归属顾问</label><Select value={Number(form.assigned_user_id || 0)} onChange={(v) => setField('assigned_user_id', v)} options={users} filterable style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">状态</label><Select value={Number(form.status)} onChange={(v) => setField('status', v)} options={STATUS_EDIT_OPTS} style={{ width: '100%' }} /></div>
          <div className="md:col-span-2"><label className="block text-xs text-gray-500 mb-1">备注</label><Textarea value={form.remark} onChange={(v) => setField('remark', v)} autosize={{ minRows: 2, maxRows: 5 }} /></div>
        </div>
      </Dialog>

      <Dialog header="管理标签" visible={tagVisible} onClose={() => setTagVisible(false)} onConfirm={() => void submitTags()} confirmBtn={saving ? '保存中…' : '保存'} width="620px">
        <Select
          value={tagForm}
          onChange={(v) => setTagForm(Array.isArray(v) ? v.map(String) : [])}
          options={tagOptions}
          multiple
          filterable
          clearable
          placeholder="选择标签"
          style={{ width: '100%' }}
        />
        <p className="text-xs text-gray-400 mt-2">保存后会覆盖当前客户标签；如需删除某个标签，取消勾选即可。</p>
      </Dialog>
      <Dialog header="新建客户线索" visible={createVisible} onClose={() => setCreateVisible(false)} onConfirm={() => void submitCreate()} confirmBtn={saving ? '提交中…' : '创建'} width="620px">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div><label className="block text-xs text-gray-500 mb-1">姓名</label><Input value={create.name} onChange={(v) => setCreate((s) => ({ ...s, name: v }))} placeholder="访客/客户姓名" /></div>
          <div><label className="block text-xs text-gray-500 mb-1">手机号</label><Input value={create.phone} onChange={(v) => setCreate((s) => ({ ...s, phone: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">城市</label><Input value={create.city} onChange={(v) => setCreate((s) => ({ ...s, city: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">兴趣产品</label><Input value={create.interest_model} onChange={(v) => setCreate((s) => ({ ...s, interest_model: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">预算（万元）</label><InputNumber value={Number(create.budget || 0)} onChange={(v) => setCreate((s) => ({ ...s, budget: v }))} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">来源</label><Select value={create.source} onChange={(v) => setCreate((s) => ({ ...s, source: v }))} options={SOURCE_OPTS} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">归属顾问</label><Select value={Number(create.assigned_user_id || 0)} onChange={(v) => setCreate((s) => ({ ...s, assigned_user_id: v }))} options={users} filterable style={{ width: '100%' }} /></div>
          <div className="md:col-span-2"><label className="block text-xs text-gray-500 mb-1">备注</label><Textarea value={create.remark} onChange={(v) => setCreate((s) => ({ ...s, remark: v }))} autosize={{ minRows: 2, maxRows: 4 }} /></div>
        </div>
        <p className="text-xs text-gray-400 mt-2">手工录入的线索默认意向分 0.2，可随后在详情中编辑；创建受租户客户数配额约束（超限将被拒绝）。</p>
      </Dialog>
    </div>
  )
}
