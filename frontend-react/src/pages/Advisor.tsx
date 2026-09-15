// 顾问工作台页（移动风格）：首页客户列表/跟进提醒/我的套餐，客户详情含聊天、试驾、标签、AI 接管开关
// 顶部文件级说明；子组件 FU（跟进条目）、EditForm（资料编辑表单）见下方
// 依赖 /api/v1/advisor/*、/api/v1/chat/history、/api/v1/feedback、/api/v1/billing/my-package、/api/v1/admin/tags
import { useState, useEffect, useRef } from 'react'
import { Button, Dialog, Input, Textarea, Tag, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { AUTH, getToken, logoutAndRedirect } from '../lib/api'
import { useAdvisorWS } from '../lib/realtime'
import { collectFreshMessages } from '../lib/chat'
import { Msg, Cust, Detail } from '../types'

// 顾问工作台接口前缀
const API = '/api/v1/advisor'
// STAGE_LABELS 客户旅程阶段码 → 中文名（列表/详情展示用）
const STAGE_LABELS: Record<string, string> = { ai_connected: 'AI建联', human_connected: '人工建联', lead_captured: '已留资', arrived: '已到店', ordered: '已下单', delivered: '已交车', lost: '已战败' }
// STAGE_COLORS 阶段码 → Tailwind 徽标样式（状态标签配色）
const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }
// TABS 客户列表顶部筛选页签（值 + 中文文案）
const TABS = [{ k: 'all', t: '全部' }, { k: 'pending', t: '待跟进' }, { k: 'following', t: '跟进中' }, { k: 'arrived', t: '已到店' }, { k: 'test_drive', t: '已试驾' }]
// H 客户姓名的展示处理：匿名访客统一显示为"客户"（避免泄露原始标识）
const H = (n?: string) => (!n || n.startsWith('访客_')) ? '客户' : n
// HI 客户头像占位字符：访客取"客"，普通客户取姓名首字符
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
  // E2 修复(2026-09-14)：后端就绪但工作台零入口的 5 个能力补齐——
  // 策略推荐卡片 / 新建跟进 / 阶段调整 / 建·改试驾 / 接管对话
  const [rec, setRec] = useState<{ intent_score?: number; urgency_level?: string; recommends?: { anchor_name?: string; template_name?: string; prompt_template?: string }[] } | null>(null)
  const [fuOpen, setFuOpen] = useState(false)
  const [fuForm, setFuForm] = useState<{ method: string; content: string; next_follow_at: string }>({ method: 'phone', content: '', next_follow_at: '' })
  const [stageOpen, setStageOpen] = useState(false)
  const [stageForm, setStageForm] = useState<{ journey_stage: string; journey_sub_stage: string }>({ journey_stage: '', journey_sub_stage: '' })
  const [tdOpen, setTdOpen] = useState(false)
  const [tdEdit, setTdEdit] = useState<any>(null) // 非空=编辑已有单（PUT），空=新建（POST）
  const [tdForm, setTdForm] = useState<Record<string, string>>({ scheduled_at: '', model_name: '', contact_name: '', contact_phone: '', location: '', note: '', status: 'pending' })
  const [chanCtx, setChanCtx] = useState<any>(null)
  const [chanKey, setChanKey] = useState<{ corpid: string; external_userid: string } | null>(null)
  const chatRef = useRef<HTMLDivElement>(null)
  // P2-84 修复：已展示消息 ID 集合——轮询从"全量替换"改"只追加新消息"（对齐 Client 已修语义）
  const localIds = useRef<Set<string>>(new Set())
  // P1-13 修复(2026-09-15)：openDetail/loadChat/loadRecommend 均为"发请求→等响应→落状态"，
  // 快速切换客户时旧请求后到会覆盖新详情（detail/msgs/rec 与 detailId 错位）。
  // 用 ref 记录"当前应展示的客户"，落状态前核对，错位的响应直接丢弃。
  const detailIdRef = useRef<number | null>(null)

  // 加载工作台统计：今日线索/意向客户等汇总数字
  const loadStats = async () => { const j = await AUTH(API + '/stats'); if (j.code === 0) setStats(j.data || []) }
  // 加载客户列表：按当前状态筛选标签拉取，最多50条
  const loadCustomers = async () => { const j = await AUTH(API + '/customers?status=' + status + '&page_size=50'); if (j.code === 0) setList((j.data?.list) || []) }
  // 加载跟进提醒列表（今日待跟进/逾期）
  const loadFollowups = async () => { const j = await AUTH(API + '/followups'); if (j.code === 0) setFollowups(j.data || []) }
  // 加载当前租户套餐与三桶余额，用于顶栏额度展示
  const loadQuota = async () => { const j = await AUTH('/api/v1/billing/my-package'); if (j.code === 0) setQuota(j.data) }
  // 通道侧边栏：URL 带 corpid/external_userid 时拉取微信客户上下文，并直接落到对应客户详情
  const loadChannelContext = async () => {
    const q = new URLSearchParams(location.search)
    const corpid = q.get('corpid') || ''
    const external_userid = q.get('external_userid') || ''
    if (!corpid || !external_userid) return
    setChanKey({ corpid, external_userid })
    const j = await AUTH(`/api/v1/channel/wecom/context?corpid=${encodeURIComponent(corpid)}&external_userid=${encodeURIComponent(external_userid)}`)
    if (j?.code === 0 && j.data?.customer_id) {
      setChanCtx(j.data)
      setView('home')
      openDetail(Number(j.data.customer_id))
    } else if (j?.code !== 0) {
      MessagePlugin.warning(j?.message || '未找到该渠道客户')
    }
  }

  // 路由守卫：无 token 直接跳登录；否则加载统计、客户列表、全部标签
  useEffect(() => { if (!getToken()) { location.href = '/login'; return } loadStats(); loadCustomers(); loadAllTags(); loadChannelContext() }, [])
  useEffect(() => { loadCustomers() }, [status])
  useEffect(() => { if (view === 'followup') loadFollowups(); if (view === 'me') loadQuota() }, [view])
  useEffect(() => { if (chatRef.current) chatRef.current.scrollTop = chatRef.current.scrollHeight }, [msgs])

  // 打开客户详情：加载客户信息、会话与试驾记录，定位当前会话并同步AI回复开关
  async function openDetail(id: number) {
    detailIdRef.current = id
    setDetailId(id)
    const j = await AUTH(API + '/customer/' + id)
    // P1-13 修复(2026-09-15)：切客户后的迟到响应不得回写详情
    if (detailIdRef.current !== id) return
    let convIdNow: number | null = null
    if (j.code === 0 && j.data) {
      setDetail(j.data)
      const conv = (j.data.conversations && j.data.conversations[0])
      convIdNow = conv ? conv.id : null
      setConvId(convIdNow)
      setAiOn(conv ? conv.is_ai_reply_enabled !== false : true)
    }
    loadChat(id); loadTestDrives(id); loadRecommend(id, convIdNow)
  }
  // 拉取客户聊天记录（最多50条，供右侧会话窗口展示）
  // P2-84 修复：打开详情时重置 ID 集合并全量替换；WS/轮询增量时只追加新消息
  async function loadChat(id: number) {
    const j = await AUTH('/api/v1/chat/history?customer_id=' + id + '&limit=50')
    if (detailIdRef.current !== id) return // P1-13 修复(2026-09-15)：丢弃错位响应
    if (j.code === 0) {
      const arr = j.data || []
      localIds.current = new Set(arr.map((m: Msg) => String(m.id)))
      setMsgs(arr)
    }
  }
  // 拉取客户试驾单列表
  async function loadTestDrives(id: number) { const j = await AUTH(API + '/test-drives?customer_id=' + id); setTestDrives(j.data || []) }
  // 人工发送消息：调用顾问端 chat/send 接口，成功后追加到本地消息列表
  async function send() {
    if (!input.trim() || !detailId) return
    const content = input; setInput('')
    const j = await AUTH(API + '/chat/send', { method: 'POST', body: { conversation_id: convId, customer_id: detailId, content } })
    if (j.code === 0) { if (j.data?.conversation_id) setConvId(j.data.conversation_id); setMsgs((m) => [...m, { sender_type: 'human', content, created_at: new Date().toISOString() }]) }
    else MessagePlugin.error('发送失败')
  }
  // 切换AI自动回复开关（需要存在活跃会话）
  async function toggleAI() {
    if (!convId) { MessagePlugin.info('暂无活跃会话'); return }
    const j = await AUTH(API + '/chat/toggle-ai-reply', { method: 'POST', body: { conversation_id: convId } })
    if (j.code === 0) setAiOn(j.data.is_ai_reply_enabled)
  }
  // 加载全部标签（用于客户标签编辑弹窗的选项）
  // 2026-09-08 修复：原调 /api/v1/admin/tags 被 AdminRequired 拦（sales/user 403）→ 弹窗选项恒空。
  // 后端已开放 advisor 组只读路由 /api/v1/advisor/tags（无 AdminRequired，PQ 租户+预置可见）。
  // P0-10 修复(2026-09-15 复核批)：该路由复用 GetTagList，data.list 是 **Tag 对象数组**
  // （json: name/code/status…），旧实现按 string[] 直渲染对象 → React error #31 整页崩溃；
  // 勾选保存也会把对象塞进 tags:[]string 请求体 400。统一归一为标签名字符串。
  async function loadAllTags() {
    const j = await AUTH('/api/v1/advisor/tags')
    if (j.code !== 0) return
    const raw = (j.data?.list) || j.data || []
    setAllTags(Array.isArray(raw)
      ? raw.map((t: unknown) => (typeof t === 'string' ? t : (t as { name?: string })?.name || '')).filter(Boolean)
      : [])
  }
  // 保存客户标签：提交勾选标签到 /customer/:id/tags 后刷新详情
  async function saveTags() {
    if (!detailId) return
    const j = await AUTH(API + '/customer/' + detailId + '/tags', { method: 'PUT', body: { tags: checkedTags } })
    if (j.code === 0) { MessagePlugin.success('标签已更新'); setTagOpen(false); openDetail(detailId) }
  }
  // E2：拉取策略推荐（进详情即拉，失败静默不打扰顾问）
  // P1-13 修复(2026-09-15)：原实现读闭包里的 convId——openDetail 刚 setConvId 尚未生效，
  // 拿到的是**上一个客户**的会话 ID（推荐串台）。改由调用方显式传入本次会话 ID。
  const loadRecommend = async (id: number, cid: number | null) => {
    setRec(null)
    const j = await AUTH(`${API}/strategy/recommend?customer_id=${id}&conversation_id=${cid || ''}`)
    if (detailIdRef.current !== id) return // 迟到的错位响应丢弃
    if (j?.code === 0) setRec(j.data)
  }
  // E2：新建跟进提醒（method=phone/wechat/store/email，next_follow_at 留空=仅记录不提醒）
  async function saveFollowup() {
    if (!detailId) return
    const body: Record<string, unknown> = { customer_id: detailId, type: 'manual', method: fuForm.method, content: fuForm.content }
    if (fuForm.next_follow_at) body.next_follow_at = new Date(fuForm.next_follow_at).toISOString()
    const j = await AUTH(API + '/customer/' + detailId + '/followup', { method: 'POST', body })
    if (j.code === 0) { MessagePlugin.success('跟进已记录'); setFuOpen(false); setFuForm({ method: 'phone', content: '', next_follow_at: '' }) }
    else MessagePlugin.error(j?.message || '保存失败')
  }
  // E2：调整旅程阶段（journey_stage 必填；仅 arrived 时子状态有效）
  async function saveStage() {
    if (!detailId || !stageForm.journey_stage) { MessagePlugin.warning('请选择目标阶段'); return }
    const body: Record<string, string> = { journey_stage: stageForm.journey_stage }
    if (stageForm.journey_stage === 'arrived' && stageForm.journey_sub_stage) body.journey_sub_stage = stageForm.journey_sub_stage
    const j = await AUTH(API + '/customer/' + detailId + '/stage', { method: 'PUT', body })
    if (j.code === 0) { MessagePlugin.success('阶段已更新'); setStageOpen(false); openDetail(detailId) }
    else MessagePlugin.error(j?.message || '更新失败')
  }
  // E2：创建/更新试驾单（tdEdit 非空走 PUT，附状态变更）
  async function saveTestDrive() {
    if (!detailId) return
    if (!tdForm.scheduled_at) { MessagePlugin.warning('请选择预约时间'); return }
    let j
    if (tdEdit) {
      j = await AUTH(API + '/test-drive/' + tdEdit.id, { method: 'PUT', body: { ...tdForm, scheduled_at: new Date(tdForm.scheduled_at).toISOString() } })
    } else {
      j = await AUTH(API + '/test-drive', { method: 'POST', body: { customer_id: detailId, ...tdForm, scheduled_at: new Date(tdForm.scheduled_at).toISOString() } })
    }
    if (j?.code === 0) { MessagePlugin.success(tdEdit ? '试驾单已更新' : '试驾单已创建'); setTdOpen(false); loadTestDrives(detailId) }
    else MessagePlugin.error(j?.message || '保存失败')
  }
  // E2：快捷改试驾单状态（已完成/已取消）
  async function setTDStatus(td: { id: number }, status: string) {
    const j = await AUTH(API + '/test-drive/' + td.id, { method: 'PUT', body: { status } })
    if (j?.code === 0) { MessagePlugin.success('已更新'); loadTestDrives(detailId!) }
    else MessagePlugin.error(j?.message || '更新失败')
  }
  // E2：一键接管对话（mode=human，AI 暂停自动回复）
  async function takeover() {
    if (!convId) { MessagePlugin.info('暂无活跃会话'); return }
    const j = await AUTH(API + '/chat/takeover', { method: 'POST', body: { conversation_id: convId } })
    if (j.code === 0) { MessagePlugin.success('已接管，AI 暂停自动回复'); setAiOn(false) }
    else MessagePlugin.error(j?.message || '接管失败')
  }
  // 提交用户反馈（产品建议/吐槽），走 /api/v1/feedback
  async function submitFeedback() {
    if (!fbText.trim()) return
    const j = await AUTH('/api/v1/feedback', { method: 'POST', body: { content: fbText, target_type: 'feature' } })
    if (j.code === 0) { MessagePlugin.success('已提交'); setFbOpen(false); setFbText('') } else MessagePlugin.error(j.message || '提交失败')
  }
  // 详情轮询新消息：打开某客户后每 5s 拉一次聊天记录，保持与 AI/客户消息同步
  // P2-84 修复：只追加新消息（collectFreshMessages 按 ID 去重），不再 setMsgs 全量替换
  useEffect(() => {
    if (detailId == null) return
    const t = setInterval(async () => {
      const j = await AUTH('/api/v1/chat/history?customer_id=' + detailId + '&limit=50')
      if (j.code === 0) {
        const fresh = collectFreshMessages((j.data || []) as Msg[], localIds.current)
        if (fresh.length > 0) setMsgs((m) => [...m, ...fresh])
      }
    }, 5000)
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
        {/* G-20：aria-label 标注退出按钮，辅助技术可识别操作意图 */}
        <button onClick={logoutAndRedirect} aria-label="退出登录" style={{ background: 'rgba(255,255,255,.2)', border: 'none', color: '#fff', borderRadius: 6, padding: '4px 10px', fontSize: 12 }}>退出</button>
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
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.used_ai_calls}/{quota.max_ai_calls || '∞'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>本月AI调用</span></div>
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.ai_call_balance}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>增量余额</span></div>
            <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.expired_at ? new Date(quota.expired_at).toLocaleDateString() : '-'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>到期日</span></div>
          </div>}
          <Button theme="primary" block onClick={() => setFbOpen(true)}>提交产品反馈</Button>
        </div>
      )}

      {/* 客户详情 */}
      {detailId != null && view === 'home' && (
        <div style={{ position: 'fixed', inset: 0, background: '#f5f7fa', zIndex: 20, maxWidth: 480, margin: '0 auto' }}>
          <header style={{ background: 'var(--pri)', color: '#fff', padding: '14px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
            {/* G-20：aria-label 标注返回按钮，辅助技术可识别导航操作 */}
            <button onClick={() => (detailIdRef.current = null, setDetailId(null))} aria-label="返回客户列表" style={{ background: 'none', border: 'none', color: '#fff', fontSize: 16 }}>←</button>
            <span style={{ fontWeight: 600 }}>{H(c?.name)}</span>
            {/* G-20：aria-label 标注编辑按钮，辅助技术可识别操作意图 */}
            <button onClick={() => setEditOpen(true)} aria-label="编辑客户资料" style={{ background: 'none', border: 'none', color: '#fff', fontSize: 13 }}>编辑</button>
          </header>
          <div style={{ padding: 12, overflowY: 'auto', height: 'calc(100vh - 110px)' }}>
            {chanCtx && Number(chanCtx.customer_id) === Number(c?.id) && (
              <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13, border: '1px solid #eef2ff' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                  <b>企业微信客户</b>
                  <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#eef2ff', color: '#4338ca' }}>侧边栏</span>
                </div>
                <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2,1fr)', gap: '6px 12px', marginTop: 8 }}>
                  <div><span style={{ color: '#a0aec0' }}>阶段</span><div>{STAGE_LABELS[chanCtx.journey_stage] || chanCtx.journey_stage || '-'}</div></div>
                  <div><span style={{ color: '#a0aec0' }}>关注产品</span><div>{chanCtx.interest_model || '-'}</div></div>
                  <div><span style={{ color: '#a0aec0' }}>企业微信ID</span><div style={{ wordBreak: 'break-all' }}>{chanCtx.external_userid || chanKey?.external_userid || '-'}</div></div>
                  <div><span style={{ color: '#a0aec0' }}>接待成员</span><div>{chanCtx.staff_id || '-'}</div></div>
                </div>
                {String(chanCtx.tags || '').split(/[,，]/).map((x: string) => x.trim()).filter(Boolean).length > 0 && (
                  <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginTop: 8 }}>
                    {String(chanCtx.tags || '').split(/[,，]/).map((x: string) => x.trim()).filter(Boolean).map((x: string) => <span key={x} style={{ fontSize: 11, padding: '1px 7px', borderRadius: 10, background: '#f1f5f9', color: '#475569' }}>{x}</span>)}
                  </div>
                )}
              </div>
            )}
            <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 10 }}>
              {(detail?.tags || []).map((t, i) => <Tag key={i} theme="primary" variant="light">{t.tag_name}</Tag>)}
              <Tag theme="default" style={{ cursor: 'pointer' }} onClick={() => { setCheckedTags((detail?.tags || []).map((t) => t.tag_name)); setTagOpen(true) }}>+ 标签</Tag>
            </div>
            {/* E2 修复(2026-09-14)：动作条——后端早就绪但旧工作台只有聊天+标签，跟进/阶段/试驾/接管全靠口头或后台 */}
            <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
              <Button size="small" variant="outline" onClick={() => setFuOpen(true)}>新建跟进</Button>
              <Button size="small" variant="outline" onClick={() => { setStageForm({ journey_stage: c?.journey_stage || '', journey_sub_stage: c?.journey_sub_stage || '' }); setStageOpen(true) }}>调整阶段</Button>
              <Button size="small" variant="outline" onClick={() => { setTdEdit(null); setTdForm({ scheduled_at: '', model_name: c?.interest_model || '', contact_name: c?.name || '', contact_phone: c?.phone || '', location: '', note: '', status: 'pending' }); setTdOpen(true) }}>建试驾单</Button>
              {c?.journey_stage && <span style={{ alignSelf: 'center', fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#eef2ff', color: '#4338ca' }}>{STAGE_LABELS[c.journey_stage] || c.journey_stage}</span>}
            </div>
            {/* E2：AI 策略推荐卡片（intent/紧迫度 + 推荐话术一键填入输入框） */}
            {rec && (rec.recommends || []).length > 0 && (
              <div style={{ background: 'linear-gradient(135deg,#eef2ff,#faf5ff)', border: '1px solid #e0e7ff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
                  <b style={{ fontSize: 13 }}>策略推荐</b>
                  <span style={{ fontSize: 11, color: '#4338ca' }}>意向 {(Number(rec.intent_score) * 100).toFixed(0)}%{rec.urgency_level ? ` · ${rec.urgency_level === 'high' ? '紧迫' : rec.urgency_level === 'medium' ? '中等' : '平稳'}` : ''}</span>
                </div>
                {(rec.recommends || []).slice(0, 2).map((r, i) => (
                  <div key={i} style={{ background: '#fff', borderRadius: 8, padding: '8px 10px', marginTop: 6, display: 'flex', gap: 8, alignItems: 'flex-start' }}>
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{ fontSize: 11, color: '#7c3aed' }}>{r.anchor_name || '话术'} · {r.template_name}</div>
                      <div style={{ marginTop: 2, color: '#374151' }}>{(r.prompt_template || '').slice(0, 80)}</div>
                    </div>
                    <button aria-label="填入输入框" onClick={() => setInput(r.prompt_template || '')} style={{ flexShrink: 0, background: 'var(--pri)', color: '#fff', border: 'none', borderRadius: 6, padding: '4px 8px', fontSize: 11, cursor: 'pointer' }}>填入</button>
                  </div>
                ))}
              </div>
            )}
            <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between' }}><span style={{ color: '#a0aec0' }}>手机</span><span>{c?.phone || '-'}{c?.phone && <a href={'tel:' + c.phone} style={{ marginLeft: 8, color: 'var(--pri)' }}>📞</a>}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>兴趣车型</span><span>{c?.interest_model || '-'}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>预算</span><span>{c?.budget > 0 ? c.budget + '万' : '-'}</span></div>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>备注</span><span style={{ maxWidth: 200, textAlign: 'right' }}>{c?.remark || '-'}</span></div>
            </div>
            {/* E2：试驾单可操作——新建/编辑/完成/取消（旧版只读展示，状态全靠后台改） */}
            <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
                <b style={{ fontSize: 13 }}>试驾单</b>
                <button onClick={() => { setTdEdit(null); setTdForm({ scheduled_at: '', model_name: c?.interest_model || '', contact_name: c?.name || '', contact_phone: c?.phone || '', location: '', note: '', status: 'pending' }); setTdOpen(true) }} style={{ background: 'none', border: 'none', color: 'var(--pri)', fontSize: 12, cursor: 'pointer' }}>+ 新建</button>
              </div>
              {testDrives.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>暂无试驾单</div>}
              {testDrives.map((td, i) => <div key={i} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0', display: 'flex', gap: 8, alignItems: 'center' }}>
                <span style={{ flex: 1 }}>{td.model_name || '试驾'} <span style={{ color: '#a0aec0' }}>· {td.status === 'pending' ? '待试驾' : td.status === 'completed' ? '已完成' : '已取消'}</span>{td.scheduled_at ? <span style={{ color: '#a0aec0' }}> · {new Date(td.scheduled_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })}</span> : null}</span>
                {td.status === 'pending' && <><button onClick={() => setTDStatus(td, 'completed')} style={{ background: 'none', border: '1px solid #c6f6d5', color: '#276749', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>完成</button>
                  <button onClick={() => setTDStatus(td, 'cancelled')} style={{ background: 'none', border: '1px solid #fed7d7', color: '#9b2c2c', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>取消</button></>}
                <button onClick={() => { setTdEdit(td); setTdForm({ scheduled_at: td.scheduled_at ? td.scheduled_at.slice(0, 16) : '', model_name: td.model_name || '', contact_name: td.contact_name || '', contact_phone: td.contact_phone || '', location: td.location || '', note: td.note || '', status: td.status || 'pending' }); setTdOpen(true) }} style={{ background: 'none', border: '1px solid #e2e8f0', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>编辑</button>
              </div>)}
            </div>
            <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
                <b style={{ fontSize: 13 }}>聊天记录</b>
                <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
                  {/* E2：一键接管对话（AI 暂停），与自动回复开关联动 */}
                  <button onClick={takeover} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', border: '1px solid var(--pri)', background: '#fff', color: 'var(--pri)' }}>接管</button>
                  {/* G-20：AI回复开关 aria-label 动态切换文案，role="button" + tabIndex + onKeyDown 支持键盘操作 */}
                  <span onClick={toggleAI} role="button" tabIndex={0} aria-label={aiOn ? '关闭自动回复' : '开启自动回复'} onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') toggleAI() }} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', background: aiOn ? '#d1fae5' : '#f3f4f6', color: aiOn ? '#047857' : '#6b7280' }}>自动回复{aiOn ? '开' : '关'}</span>
                </div>
              </div>
              <div ref={chatRef} style={{ maxHeight: 320, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
                {msgs.map((m, i) => <div key={i} style={{ alignSelf: m.sender_type === 'human' ? 'flex-end' : 'flex-start', background: m.sender_type === 'human' ? 'var(--pri)' : m.sender_type === 'ai' ? '#ecfdf5' : '#f1f5f9', color: m.sender_type === 'human' ? '#fff' : '#1f2937', padding: '8px 12px', borderRadius: 10, maxWidth: '80%', fontSize: 13 }}>{m.content}</div>)}
              </div>
            </div>
          </div>
          <div style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', padding: 10, display: 'flex', gap: 8 }}>
            {/* G-20：顾问消息输入框 aria-label 供屏幕阅读器识别 */}
            <Input value={input} onChange={(v) => setInput(v)} placeholder="输入消息…" aria-label="顾问消息输入框" onEnter={send} style={{ flex: 1 }} />
            {/* G-20：发送按钮 aria-label 标注操作意图 */}
            <Button theme="primary" onClick={send} aria-label="发送消息">发送</Button>
          </div>
        </div>
      )}

      {/* G-20：底部导航栏 aria-label 标注导航用途，aria-current 标记当前激活页签 */}
      <nav aria-label="顾问工作台导航" style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', display: 'flex' }}>
        {[{ k: 'home', t: '首页' }, { k: 'followup', t: '跟进' }, { k: 'me', t: '我的' }].map((t) => <button key={t.k} onClick={() => { setView(t.k); (detailIdRef.current = null, setDetailId(null)) }} aria-current={view === t.k ? 'page' : undefined} style={{ flex: 1, padding: '10px 0', border: 'none', background: 'none', color: view === t.k ? 'var(--pri)' : '#a0aec0', fontWeight: view === t.k ? 600 : 400 }}>{t.t}</button>)}
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

      {/* E2：新建跟进弹窗 */}
      <Dialog header="新建跟进" visible={fuOpen} onClose={() => setFuOpen(false)} onConfirm={saveFollowup} confirmBtn="保存">
        <div style={{ display: 'grid', gap: 10 }}>
          <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
            {[{ k: 'phone', t: '电话' }, { k: 'wechat', t: '微信' }, { k: 'store', t: '到店' }, { k: 'email', t: '邮件' }].map((m) => (
              <button key={m.k} onClick={() => setFuForm({ ...fuForm, method: m.k })} style={{ padding: '4px 12px', borderRadius: 16, fontSize: 13, border: 'none', background: fuForm.method === m.k ? 'var(--pri)' : '#f1f5f9', color: fuForm.method === m.k ? '#fff' : '#475569', cursor: 'pointer' }}>{m.t}</button>
            ))}
          </div>
          <Textarea value={fuForm.content} onChange={(v) => setFuForm({ ...fuForm, content: v })} placeholder="跟进内容…" autosize={{ minRows: 2 }} />
          <label style={{ fontSize: 13, color: '#475569' }}>下次跟进时间（可选）<input type="datetime-local" value={fuForm.next_follow_at} onChange={(e) => setFuForm({ ...fuForm, next_follow_at: e.target.value })} style={{ display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }} /></label>
        </div>
      </Dialog>

      {/* E2：调整旅程阶段弹窗 */}
      <Dialog header="调整客户阶段" visible={stageOpen} onClose={() => setStageOpen(false)} onConfirm={saveStage} confirmBtn="保存">
        <div style={{ display: 'grid', gap: 10 }}>
          <label style={{ fontSize: 13, color: '#475569' }}>目标阶段
            <select value={stageForm.journey_stage} onChange={(e) => setStageForm({ ...stageForm, journey_stage: e.target.value })} style={{ display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }}>
              <option value="">请选择…</option>
              {Object.entries(STAGE_LABELS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
            </select>
          </label>
          {stageForm.journey_stage === 'arrived' && <label style={{ fontSize: 13, color: '#475569' }}>到店子状态
            <select value={stageForm.journey_sub_stage} onChange={(e) => setStageForm({ ...stageForm, journey_sub_stage: e.target.value })} style={{ display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }}>
              <option value="">无</option><option value="test_driven">已试驾</option><option value="quoted">已报价</option>
            </select>
          </label>}
        </div>
      </Dialog>

      {/* E2：建/改试驾单弹窗 */}
      <Dialog header={tdEdit ? '编辑试驾单' : '新建试驾单'} visible={tdOpen} onClose={() => setTdOpen(false)} onConfirm={saveTestDrive} confirmBtn="保存">
        <div style={{ display: 'grid', gap: 10 }}>
          <label style={{ fontSize: 13, color: '#475569' }}>预约时间 *<input type="datetime-local" value={tdForm.scheduled_at} onChange={(e) => setTdForm({ ...tdForm, scheduled_at: e.target.value })} style={{ display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }} /></label>
          <Input value={tdForm.model_name} onChange={(v) => setTdForm({ ...tdForm, model_name: v })} placeholder="试驾车型" />
          <div style={{ display: 'flex', gap: 8 }}>
            <Input value={tdForm.contact_name} onChange={(v) => setTdForm({ ...tdForm, contact_name: v })} placeholder="联系人" style={{ flex: 1 }} />
            <Input value={tdForm.contact_phone} onChange={(v) => setTdForm({ ...tdForm, contact_phone: v })} placeholder="联系电话" style={{ flex: 1 }} />
          </div>
          <Input value={tdForm.location} onChange={(v) => setTdForm({ ...tdForm, location: v })} placeholder="试驾地点" />
          <Textarea value={tdForm.note} onChange={(v) => setTdForm({ ...tdForm, note: v })} placeholder="备注" autosize={{ minRows: 2 }} />
        </div>
      </Dialog>
    </div>
  )

  // 保存客户资料编辑：从表单DOM读取姓名/手机/车型/预算/备注后 PUT /customer/:id/info
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
    const j = await AUTH(API + '/customer/' + detailId + '/info', { method: 'PUT', body })
    if (j.code === 0) { MessagePlugin.success('保存成功'); setEditOpen(false); openDetail(detailId) } else MessagePlugin.error('保存失败')
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
