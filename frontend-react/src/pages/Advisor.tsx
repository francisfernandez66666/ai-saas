// 顾问工作台页（移动风格）：首页客户列表/跟进提醒/我的套餐，客户详情含聊天、试驾、标签、AI 接管开关
// 顶部文件级说明；子组件 FU（跟进条目）、EditForm（资料编辑表单）见下方
// 依赖 /api/v1/advisor/*、/api/v1/chat/history、/api/v1/feedback、/api/v1/billing/my-package、/api/v1/admin/tags
import { useState, useEffect, useRef } from 'react'
import { Button, Dialog, Input, Textarea, Tag, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { getToken } from '../lib/api'
import { useAdvisorWS } from '../lib/realtime'
import { Msg, Cust, Detail } from '../types'

// 顾问工作台接口前缀与带 token 的请求头构造器
const API = '/api/v1/advisor'
const AUTH = (): any => ({ headers: { Authorization: 'Bearer ' + getToken(), 'Content-Type': 'application/json' } })
const STAGE_LABELS: Record<string, string> = { ai_connected: 'AI建联', human_connected: '人工建联', lead_captured: '已留资', arrived: '已到店', ordered: '已下单', delivered: '已交车', lost: '已战败' }
const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }
const TABS = [{ k: 'all', t: '全部' }, { k: 'pending', t: '待跟进' }, { k: 'following', t: '跟进中' }, { k: 'arrived', t: '已到店' }, { k: 'test_drive', t: '已试驾' }]
const H = (n?: string) => (!n || n.startsWith('访客_')) ? '客户' : n
const HI = (n?: string) => (!n || n.startsWith('访客_')) ? '客' : (n?.[0] || '?')

// 首页统计卡片的数据结构（数值 + 文案 + 颜色）
type Stat = { value: number; label: string; color: string }
// Cust / Detail / Msg 已从 ../types 导入（见上方 import），统一领域口径

// 顾问端（移动风格）：首页客户列表/跟进提醒/我的套餐，客户详情含聊天、试驾、标签、AI 接管开关
// 依赖 /api/v1/advisor/*、/api/v1/chat/history、/api/v1/feedback、/api/v1/billing/my-package
export default function Advisor() {
  const brand = useBrand()
  const [view, setView] = useState('home')
  const [status, setStatus] = useState('all')
  const [stats, setStats] = useState<Stat[]>([])
  const [list, setList] = useState<Cust[]>([])
  const [detailId, setDetailId] = useState<number | null>(null)
  const [detail, setDetail] = useState<Detail | null>(null)
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [convId, setConvId] = useState<number | null>(null)
  const [aiOn, setAiOn] = useState(true)
  const [testDrives, setTestDrives] = useState<any[]>([])
  const [followups, setFollowups] = useState<any[]>([])
  const [quota, setQuota] = useState<any>(null)
  const [input, setInput] = useState('')
  const [editOpen, setEditOpen] = useState(false)
  const [tagOpen, setTagOpen] = useState(false)
  const [allTags, setAllTags] = useState<string[]>([])
  const [checkedTags, setCheckedTags] = useState<string[]>([])
  const [fbOpen, setFbOpen] = useState(false)
  const [fbText, setFbText] = useState('')
  const chatRef = useRef<HTMLDivElement>(null)

  const loadStats = async () => { const r = await fetch(API + '/stats', AUTH()); const j = await r.json(); if (j.code === 0) setStats(j.data || []) }
  const loadCustomers = async () => { const r = await fetch(API + '/customers?status=' + status + '&page_size=50', AUTH()); const j = await r.json(); if (j.code === 0) setList((j.data?.list) || []) }
  const loadFollowups = async () => { const r = await fetch(API + '/followups', AUTH()); const j = await r.json(); if (j.code === 0) setFollowups(j.data || []) }
  const loadQuota = async () => { const r = await fetch('/api/v1/billing/my-package', AUTH()); const j = await r.json(); if (j.code === 0) setQuota(j.data) }

  // 路由守卫：无 token 直接跳登录；否则加载统计、客户列表、全部标签
  useEffect(() => { if (!getToken()) { location.href = '/login'; return } loadStats(); loadCustomers(); loadAllTags() }, [])
  useEffect(() => { loadCustomers() }, [status])
  useEffect(() => { if (view === 'followup') loadFollowups(); if (view === 'me') loadQuota() }, [view])
  useEffect(() => { if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight }, [msgs])

  async function openDetail(id: number) {
    setDetailId(id)
    const r = await fetch(API + '/customer/' + id, AUTH()); const j = await r.json()
    if (j.code === 0 && j.data) { setDetail(j.data); const conv = (j.data.conversations && j.data.conversations[0]); setConvId(conv ? conv.id : null); setAiOn(conv ? conv.is_ai_reply_enabled !== false : true) }
    loadChat(id); loadTestDrives(id)
  }
  async function loadChat(id: number) { const r = await fetch('/api/v1/chat/history?customer_id=' + id + '&limit=50', AUTH()); const j = await r.json(); if (j.code === 0) setMsgs(j.data || []) }
  async function loadTestDrives(id: number) { const r = await fetch(API + '/test-drives?customer_id=' + id, AUTH()); const j = await r.json(); setTestDrives(j.data || []) }
  async function send() {
    if (!input.trim() || !detailId) return
    const content = input; setInput('')
    const r = await fetch(API + '/chat/send', { method: 'POST', headers: AUTH(), body: JSON.stringify({ conversation_id: convId, customer_id: detailId, content }) })
    const j = await r.json()
    if (j.code === 0) { if (j.data?.conversation_id) setConvId(j.data.conversation_id); setMsgs((m) => [...m, { sender_type: 'human', content, created_at: new Date().toISOString() }]) }
    else MessagePlugin.error('发送失败')
  }
  async function toggleAI() {
    if (!convId) { MessagePlugin.info('暂无活跃会话'); return }
    const r = await fetch(API + '/chat/toggle-ai-reply', { method: 'POST', headers: AUTH(), body: JSON.stringify({ conversation_id: convId }) })
    const j = await r.json(); if (j.code === 0) setAiOn(j.data.is_ai_reply_enabled)
  }
  async function loadAllTags() { const r = await fetch('/api/v1/admin/tags', AUTH()); const j = await r.json(); if (j.code === 0) setAllTags((j.data?.list) || j.data || []) }
  async function saveTags() {
    if (!detailId) return
    const r = await fetch(API + '/customer/' + detailId + '/tags', { method: 'PUT', headers: AUTH(), body: JSON.stringify({ tags: checkedTags }) })
    const j = await r.json(); if (j.code === 0) { MessagePlugin.success('标签已更新'); setTagOpen(false); openDetail(detailId) }
  }
  async function submitFeedback() {
    if (!fbText.trim()) return
    const r = await fetch('/api/v1/feedback', { method: 'POST', headers: AUTH(), body: JSON.stringify({ content: fbText, target_type: 'feature' }) })
    const j = await r.json(); if (j.code === 0) { MessagePlugin.success('已提交'); setFbOpen(false); setFbText('') } else MessagePlugin.error(j.message || '提交失败')
  }
  // 详情轮询新消息：打开某客户后每 5s 拉一次聊天记录，保持与 AI/客户消息同步
  useEffect(() => {
    if (detailId == null) return
    const t = setInterval(async () => { const r = await fetch('/api/v1/chat/history?customer_id=' + detailId + '&limit=50', AUTH()); const j = await r.json(); if (j.code === 0) setMsgs(j.data || []) }, 5000)
    return () => clearInterval(t)
  }, [detailId])

  // P1-2 实时推送：收到新消息信号即触发即时拉取（5s 轮询保留兜底）
  useAdvisorWS((ev) => {
    loadCustomers()
    if (detailId != null && ev?.customer_id === detailId) loadChat(detailId)
  })

  if (!getToken()) return null
  const c = detail?.customer

  return (
    <div style={{ maxWidth: 480, margin: '0 auto', minHeight: '100vh', background: '#f5f7fa', color: '#2d3748' }}>
      <header style={{ background: 'var(--pri)', color: '#fff', padding: '14px 16px', fontSize: 16, fontWeight: 600, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span>{brand.brandName} · 顾问工作台</span>
        <button onClick={() => { localStorage.clear(); location.href = '/login' }} style={{ background: 'rgba(255,255,255,.2)', border: 'none', color: '#fff', borderRadius: 6, padding: '4px 10px', fontSize: 12 }}>退出</button>
      </header>

      {view === 'home' && (
        <div>
          <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 8, padding: 12 }}>
            {stats.map((s, i) => <div key={i} style={{ background: '#fff', borderRadius: 8, border: '1px solid #f0f0f0', padding: 10, textAlign: 'center' }}><p style={{ fontSize: 20, fontWeight: 700 }}>{s.value}</p><p style={{ fontSize: 11, color: '#718096', marginTop: 2 }}>{s.label}</p></div>)}
          </div>
          <div style={{ display: 'flex', gap: 6, padding: '0 12px 10px', overflowX: 'auto' }}>
            {TABS.map((t) => <button key={t.k} onClick={() => setStatus(t.k)} style={{ whiteSpace: 'nowrap', padding: '5px 12px', borderRadius: 16, fontSize: 13, border: 'none', background: status === t.k ? 'var(--pri)' : '#fff', color: status === t.k ? '#fff' : '#718096' }}>{t.t}</button>)}
          </div>
          <div style={{ background: '#fff' }}>
            {list.length === 0 && <div style={{ textAlign: 'center', color: '#a0aec0', padding: 40, fontSize: 13 }}>暂无客户</div>}
            {list.map((l) => (
              <div key={l.id} onClick={() => openDetail(l.id)} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px', cursor: 'pointer', borderBottom: '1px solid #f0f0f0' }}>
                <div style={{ width: 40, height: 40, background: '#f0f0f0', borderRadius: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, color: '#718096', fontWeight: 600 }}>{HI(l.name)}</div>
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                    <span style={{ fontSize: 14, fontWeight: 500 }}>{H(l.name)}</span>
                    <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: (STAGE_COLORS[l.journey_stage || ''] || 'bg-gray-100') + ' ' + (STAGE_COLORS[l.journey_stage || ''] ? '' : 'text-gray-500') }}>{STAGE_LABELS[l.journey_stage || ''] || l.journey_stage || '-'}</span>
                    {l.conv_mode === 'human' && <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#dbeafe', color: '#1d4ed8' }}>人工</span>}
                  </div>
                  <p style={{ fontSize: 12, color: '#a0aec0', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{l.last_message || '暂无消息'}</p>
                </div>
                <span style={{ fontSize: 11, color: '#cbd5e0' }}>{l.updated_at ? new Date(l.updated_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {view === 'followup' && (
        <div style={{ padding: 12 }}>
          <h3 style={{ fontSize: 14, margin: '8px 0' }}>今日待跟进</h3>
          {followups.filter((f) => f.next_follow_at && new Date(f.next_follow_at) >= new Date()).map((f) => <FU key={'t' + f.customer_id} f={f} onClick={() => openDetail(f.customer_id)} />)}
          <h3 style={{ fontSize: 14, margin: '16px 0 8px' }}>逾期跟进</h3>
          {followups.filter((f) => f.next_follow_at && new Date(f.next_follow_at) < new Date()).map((f) => <FU key={'o' + f.customer_id} f={f} onClick={() => openDetail(f.customer_id)} />)}
          {followups.length === 0 && <p style={{ color: '#a0aec0', fontSize: 13 }}>暂无跟进提醒</p>}
        </div>
      )}

      {view === 'me' && (
        <div style={{ padding: 16 }}>
          {quota && <div style={{ background: '#fff', borderRadius: 12, padding: 16, marginBottom: 16, display: 'flex', gap: 20 }}>
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: '#4c51bf' }}>{quota.used_ai_calls}/{quota.max_ai_calls || '∞'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>本月AI调用</span></div>
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: '#4c51bf' }}>{quota.ai_call_balance}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>增量余额</span></div>
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: '#4c51bf' }}>{quota.expired_at ? new Date(quota.expired_at).toLocaleDateString() : '-'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>到期日</span></div>
          </div>}
          <Button theme="primary" block onClick={() => setFbOpen(true)}>提交产品反馈</Button>
        </div>
      )}

      {/* 客户详情 */}
      {detailId != null && view === 'home' && (
        <div style={{ position: 'fixed', inset: 0, background: '#f5f7fa', zIndex: 20, maxWidth: 480, margin: '0 auto' }}>
          <header style={{ background: 'var(--pri)', color: '#fff', padding: '14px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            <button onClick={() => setDetailId(null)} style={{ background: 'none', border: 'none', color: '#fff', fontSize: 16 }}>←</button>
            <span style={{ fontWeight: 600 }}>{H(c?.name)}</span>
            <button onClick={() => setEditOpen(true)} style={{ background: 'none', border: 'none', color: '#fff', fontSize: 13 }}>编辑</button>
          </header>
          <div style={{ padding: 12, overflowY: 'auto', height: 'calc(100vh - 110px)' }}>
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 10 }}>
              {(detail?.tags || []).map((t, i) => <Tag key={i} theme="primary" variant="light">{t.tag_name}</Tag>)}
              <Tag theme="default" style={{ cursor: 'pointer' }} onClick={() => { setCheckedTags((detail?.tags || []).map((t) => t.tag_name)); setTagOpen(true) }}>+ 标签</Tag>
            </div>
            <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}><span style={{ color: '#a0aec0' }}>手机</span><span>{c?.phone || '-'}{c?.phone && <a href={'tel:' + c.phone} style={{ marginLeft: 8, color: 'var(--pri)' }}>📞</a>}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>兴趣车型</span><span>{c?.interest_model || '-'}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>预算</span><span>{c?.budget > 0 ? c.budget + '万' : '-'}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>备注</span><span style={{ maxWidth: 200, textAlign: 'right' }}>{c?.remark || '-'}</span></div>
            </div>
            {testDrives.length > 0 && <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
              <b style={{ fontSize: 13 }}>试驾单</b>
              {testDrives.map((td, i) => <div key={i} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0' }}><span>{td.model_name || '试驾'}</span> <span style={{ color: '#a0aec0' }}>· {td.status === 'pending' ? '待试驾' : td.status === 'completed' ? '已完成' : '已取消'}</span></div>)}
            </div>}
            <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                <b style={{ fontSize: 13 }}>聊天记录</b>
                <span onClick={toggleAI} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', background: aiOn ? '#d1fae5' : '#f3f4f6', color: aiOn ? '#047857' : '#6b7280' }}>🤖 AI{aiOn ? '开' : '关'}</span>
              </div>
              <div ref={chatRef} style={{ maxHeight: 320, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
                {msgs.map((m, i) => <div key={i} style={{ alignSelf: m.sender_type === 'human' ? 'flex-end' : 'flex-start', background: m.sender_type === 'human' ? 'var(--pri)' : m.sender_type === 'ai' ? '#ecfdf5' : '#f1f5f9', color: m.sender_type === 'human' ? '#fff' : '#1f2937', padding: '8px 12px', borderRadius: 10, maxWidth: '80%', fontSize: 13 }}>{m.content}</div>)}
              </div>
            </div>
          </div>
          <div style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', padding: 10, display: 'flex', gap: 8 }}>
            <Input value={input} onChange={(v) => setInput(v)} placeholder="输入消息…" onEnter={send} style={{ flex: 1 }} />
            <Button theme="primary" onClick={send}>发送</Button>
          </div>
        </div>
      )}

      <nav style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', display: 'flex' }}>
        {[{ k: 'home', t: '首页' }, { k: 'followup', t: '跟进' }, { k: 'me', t: '我的' }].map((t) => <button key={t.k} onClick={() => { setView(t.k); setDetailId(null) }} style={{ flex: 1, padding: '10px 0', border: 'none', background: 'none', color: view === t.k ? 'var(--pri)' : '#a0aec0', fontWeight: view === t.k ? 600 : 400 }}>{t.t}</button>)}
      </nav>

      <Dialog header="编辑客户资料" visible={editOpen} onClose={() => setEditOpen(false)} onConfirm={() => saveEdit()} confirmBtn="保存">
        {c && <EditForm id={detailId!} cur={c} />}
      </Dialog>
      <Dialog header="编辑标签" visible={tagOpen} onClose={() => setTagOpen(false)} onConfirm={saveTags} confirmBtn="保存">
        <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
          {allTags.map((t) => <label key={t} style={{ fontSize: 13, display: 'flex', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={checkedTags.includes(t)} onChange={(e) => { const el = e.target as HTMLInputElement; setCheckedTags(el.checked ? [...checkedTags, t] : checkedTags.filter((x) => x !== t)) }} />{t}</label>)}
        </div>
      </Dialog>
      <Dialog header="产品反馈" visible={fbOpen} onClose={() => setFbOpen(false)} onConfirm={submitFeedback} confirmBtn="提交">
        <Textarea value={fbText} onChange={(v) => setFbText(v)} placeholder="说说你的建议…" autosize={{ minRows: 3 }} />
      </Dialog>
    </div>
  )

  async function saveEdit() {
    if (!detailId) return
    const body: any = {}
    const name = (document.getElementById('eName') as HTMLInputElement)?.value.trim()
    const phone = (document.getElementById('ePhone') as HTMLInputElement)?.value.trim()
    const model = (document.getElementById('eModel') as HTMLInputElement)?.value.trim()
    const budget = parseFloat((document.getElementById('eBudget') as HTMLInputElement)?.value || '0')
    const remark = (document.getElementById('eRemark') as HTMLTextAreaElement)?.value.trim()
    if (name) body.name = name; if (phone) body.phone = phone; if (model) body.interest_model = model
    if (budget > 0) body.budget = budget; if (remark) body.remark = remark
    const r = await fetch(API + '/customer/' + detailId + '/info', { method: 'PUT', headers: AUTH(), body: JSON.stringify(body) })
    const j = await r.json(); if (j.code === 0) { MessagePlugin.success('保存成功'); setEditOpen(false); openDetail(detailId) } else MessagePlugin.error('保存失败')
  }
}

// 跟进提醒条目：今日待跟进与逾期跟进列表中的单条展示
function FU({ f, onClick }: { f: any; onClick: () => void }) {
  return (<div onClick={onClick} style={{ background: '#fff', borderRadius: 8, border: '1px solid #f0f0f0', padding: 12, marginBottom: 8, display: 'flex', gap: 10, cursor: 'pointer', alignItems: 'center' }}>
    <div style={{ width: 32, height: 32, background: '#f0f0f0', borderRadius: 16, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11 }}>{H(f.customer_name)[0]}</div>
    <div style={{ flex: 1, minWidth: 0 }}><p style={{ fontSize: 13, fontWeight: 500 }}>{H(f.customer_name)}</p><p style={{ fontSize: 12, color: '#a0aec0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{f.content || '跟进提醒'}</p></div>
    <span style={{ fontSize: 11, color: '#cbd5e0' }}>{f.next_follow_at ? new Date(f.next_follow_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''}</span>
  </div>)
}

// 客户资料编辑表单：通过 DOM id 直接读取输入框值（非受控），提交到 /advisor/customer/:id/info
// 疑点：类型里声明了 id 入参但函数未解构使用，实际靠闭包 detailId 提交
function EditForm({ cur }: { id: number; cur: any }) {
  const lab = { display: 'block', fontSize: 13, color: '#475569', margin: '8px 0 4px' }
  const inp = { width: '100%', padding: '8px 12px', border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14 }
  return (<div style={{ display: 'grid', gap: 0 }}>
    <label style={lab}>姓名</label><input id="eName" defaultValue={cur.name || ''} style={inp} />
    <label style={lab}>手机号</label><input id="ePhone" defaultValue={cur.phone || ''} style={inp} />
    <label style={lab}>兴趣车型</label><input id="eModel" defaultValue={cur.interest_model || ''} style={inp} />
    <label style={lab}>预算(万)</label><input id="eBudget" type="number" defaultValue={cur.budget || ''} style={inp} />
    <label style={lab}>备注</label><textarea id="eRemark" defaultValue={cur.remark || ''} style={{ ...inp, minHeight: 60 }} />
  </div>)
}
