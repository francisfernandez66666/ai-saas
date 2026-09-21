// 顾问工作台页（移动风格）：首页客户列表/跟进提醒/我的套餐，客户详情含聊天、试驾、标签、AI 接管开关
// C2 拆分(2026-09-21)：原单文件 51,979 字节（统计+列表+详情+6 弹窗全在一个组件），
// 现按「视图 / 详情卡片 / 弹窗」拆到 ./advisor/* 子组件，本文件只保留**状态与行为编排**（数据加载与写操作），
// 渲染全部下沉，行为与交互逐字保持不变。
// 依赖 /api/v1/advisor/*、/api/v1/chat/history、/api/v1/feedback、/api/v1/billing/my-package、/api/v1/advisor/tags
import { useState, useEffect, useRef } from 'react'
import { MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { AUTH, getToken, logoutAndRedirect } from '../lib/api'
import { useAdvisorWS } from '../lib/realtime'
import { collectFreshMessages } from '../lib/chat'
// §八-6 D 块：企微侧边栏 JS-SDK 按需装配（失败只告警，不阻断渲染）
import { setupWecomJsSdk } from '../lib/wecomJsSdk'
import { Cust, Detail, Msg } from '../types'
import { API, NAV_TABS, type ChannelContext, type Followup, type Quota, type Recommend, type Stat, type TestDrive } from './advisor/shared'
import HomeView from './advisor/HomeView'
import FollowupView from './advisor/FollowupView'
import MeView from './advisor/MeView'
import DetailView from './advisor/DetailView'
import { EditDialog, FeedbackDialog, FollowupDialog, StageDialog, TagDialog, TestDriveDialog, type FuForm, type StageForm, type TdForm } from './advisor/dialogs'

// 客户详情（资料+标签+会话）由 ../types 的 Detail 承载，统一领域口径

// Advisor 销售顾问工作台入口：承载首页、客户详情、会话、跟进等视图的路由容器。
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
  // §八-6 C 块：当前会话模式（ai/human）——驱动「转回 AI」按钮可见性；
  // 进详情随会话同步，接管/转回操作后就地翻转（避免整页重拉）
  const [convMode, setConvMode] = useState('')
  const [testDrives, setTestDrives] = useState<TestDrive[]>([])
  const [followups, setFollowups] = useState<Followup[]>([])
  const [quota, setQuota] = useState<Quota | null>(null)
  const [input, setInput] = useState('')
  const [editOpen, setEditOpen] = useState(false)
  const [tagOpen, setTagOpen] = useState(false)
  const [allTags, setAllTags] = useState<string[]>([])
  const [checkedTags, setCheckedTags] = useState<string[]>([])
  const [fbOpen, setFbOpen] = useState(false)
  const [fbText, setFbText] = useState('')
  // E2 修复(2026-09-14)：后端就绪但工作台零入口的 5 个能力补齐——
  // 策略推荐卡片 / 新建跟进 / 阶段调整 / 建·改试驾 / 接管对话
  const [rec, setRec] = useState<Recommend | null>(null)
  const [fuOpen, setFuOpen] = useState(false)
  const [fuForm, setFuForm] = useState<FuForm>({ method: 'phone', content: '', next_follow_at: '' })
  const [stageOpen, setStageOpen] = useState(false)
  const [stageForm, setStageForm] = useState<StageForm>({ journey_stage: '', journey_sub_stage: '' })
  const [tdOpen, setTdOpen] = useState(false)
  const [tdEdit, setTdEdit] = useState<TestDrive | null>(null) // 非空=编辑已有单（PUT），空=新建（POST）
  const [tdForm, setTdForm] = useState<TdForm>({ scheduled_at: '', model_name: '', contact_name: '', contact_phone: '', location: '', note: '', status: 'pending' })
  const [chanCtx, setChanCtx] = useState<ChannelContext | null>(null)
  const [chanKey, setChanKey] = useState<{ corpid: string; external_userid: string } | null>(null)
  // P1-9 零UI补齐(2026-09-20)：跨会话历史时间线——GET /conversations/:id/messages 此前前端零消费者，
  // 详情底部聊天记录只看当前会话，历史会话内容无处可查。点行展开该会话全量消息（≤200 条，后端 ASC）
  const [tlConv, setTlConv] = useState<number | null>(null)
  const [tlMsgs, setTlMsgs] = useState<Msg[]>([])
  // P1-9：客户满意度评分入口（POST /feedback/rating，登录态按 customer_id 记录 1-5 分+评语，后端限 5 次/天/人+客户）
  const [rate, setRate] = useState<{ score: number; comment: string }>({ score: 0, comment: '' })
  // §八-6 D 块：侧边栏 JS-SDK 装配结果（null=未启动/进行中，仅侧边栏卡片展示一行小字）
  const [jsSdkOk, setJsSdkOk] = useState<boolean | null>(null)
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
    // §八-6 D 块：侧边栏上下文带 corpid，并列装配企微 JS-SDK（内部全兜底，失败只 warn）
    setupWecomJsSdk(corpid).then(setJsSdkOk)
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
      // §八-6 C 块：同步会话接管态（mode=human 或被人工锁都按"人工"呈现，驱动转回按钮）
      setConvMode(conv ? (conv.mode === 'human' || conv.is_human_locked ? 'human' : 'ai') : '')
    }
    // P1-9：切客户重置时间线展开态与评分草稿（防上一客户的消息/分数串显）
    setTlConv(null); setTlMsgs([]); setRate({ score: 0, comment: '' })
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
  // P1-9 零UI补齐(2026-09-20)：展开/收起某历史会话的消息时间线（同会话再点收起；换会话清空重拉）
  async function toggleTimeline(cid: number) {
    if (tlConv === cid) { setTlConv(null); setTlMsgs([]); return }
    setTlConv(cid); setTlMsgs([])
    const j = await AUTH('/api/v1/conversations/' + cid + '/messages')
    if (j?.code === 0) setTlMsgs(j.data || [])
  }
  // P1-9：提交客户满意度评分（1-5 必选；后端限同人同客户 5 次/天，超限 429 由 AUTH toastError 提示）
  async function submitRating() {
    if (!detailId) return
    if (rate.score < 1) { MessagePlugin.warning('先选 1-5 分'); return }
    const j = await AUTH('/api/v1/feedback/rating', { method: 'POST', body: { customer_id: detailId, rating: rate.score, comment: rate.comment } })
    if (j?.code === 0) { MessagePlugin.success('评分已提交'); setRate({ score: 0, comment: '' }) }
  }
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
  // E2：打开新建试驾单弹窗（用当前客户资料预填车型/联系人/电话）
  const newTestDriveForm = (): TdForm => ({
    scheduled_at: '', model_name: detail?.customer?.interest_model || '',
    contact_name: detail?.customer?.name || '', contact_phone: detail?.customer?.phone || '',
    location: '', note: '', status: 'pending',
  })
  function openNewTestDrive() { setTdEdit(null); setTdForm(newTestDriveForm()); setTdOpen(true) }
  // E2：打开编辑试驾单弹窗（datetime-local 只认本地时间前 16 位，截掉时区尾）
  function openEditTestDrive(td: TestDrive) {
    setTdEdit(td)
    setTdForm({
      scheduled_at: td.scheduled_at ? td.scheduled_at.slice(0, 16) : '',
      model_name: td.model_name || '', contact_name: td.contact_name || '', contact_phone: td.contact_phone || '',
      location: td.location || '', note: td.note || '', status: td.status || 'pending',
    })
    setTdOpen(true)
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
  async function setTDStatus(td: TestDrive, status: string) {
    const j = await AUTH(API + '/test-drive/' + td.id, { method: 'PUT', body: { status } })
    if (j?.code === 0) { MessagePlugin.success('已更新'); loadTestDrives(detailId!) }
    else MessagePlugin.error(j?.message || '更新失败')
  }
  // E2：一键接管对话（mode=human，AI 暂停自动回复）
  async function takeover() {
    if (!convId) { MessagePlugin.info('暂无活跃会话'); return }
    const j = await AUTH(API + '/chat/takeover', { method: 'POST', body: { conversation_id: convId } })
    if (j.code === 0) { MessagePlugin.success('已接管，AI 暂停自动回复'); setAiOn(false); setConvMode('human') }
    else MessagePlugin.error(j?.message || '接管失败')
  }
  // §八-6 C 块：转回 AI（仅人工态显示）——成功后恢复自动回复并刷新聊天；
  // 无会话/失败的提示约定参照 takeover
  async function transferBackAI() {
    if (!convId) { MessagePlugin.info('暂无活跃会话'); return }
    const j = await AUTH('/api/v1/chat/transfer/ai', { method: 'POST', body: { conversation_id: convId } })
    if (j.code === 0) {
      MessagePlugin.success('已转回 AI，自动回复恢复')
      setAiOn(true); setConvMode('ai')
      if (detailId) loadChat(detailId)
    } else MessagePlugin.error(j?.message || '转回失败')
  }
  // §八-6 C 块：立即回复（解除合并窗口延迟）——后端入参是 customer_id（非 conversation_id）
  async function clearDelay() {
    if (!detailId) return
    const j = await AUTH('/api/v1/chat/clear-delay', { method: 'POST', body: { customer_id: detailId } })
    if (j.code === 0) MessagePlugin.success('已解除延迟，消息将立即发出')
    else MessagePlugin.error(j?.message || '操作失败')
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

  // 返回列表：清 detailId 与错位保护 ref（不动 view，见 DetailView 的 P0-3 说明）
  const backToList = () => { detailIdRef.current = null; setDetailId(null) }
  // 打开标签弹窗：以当前客户已有标签为初始勾选（保存语义=提交列表即最终态）
  const openTagDialog = () => { setCheckedTags((detail?.tags || []).map((t) => t.tag_name)); setTagOpen(true) }
  // 打开阶段弹窗：以当前客户阶段预填
  const openStageDialog = () => { setStageForm({ journey_stage: detail?.customer?.journey_stage || '', journey_sub_stage: detail?.customer?.journey_sub_stage || '' }); setStageOpen(true) }

  return (
    <div style={{ maxWidth: 480, margin: '0 auto', minHeight: '100vh', background: '#f5f7fa', color: '#2d3748' }}>
      <header style={{ background: 'var(--pri)', color: '#fff', padding: '14px 16px', fontSize: 16, fontWeight: 600, display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span>{brand.brandName} · 顾问工作台</span>
        {/* G-20：aria-label 标注退出按钮，辅助技术可识别操作意图 */}
        <button onClick={logoutAndRedirect} aria-label="退出登录" style={{ background: 'rgba(255,255,255,.2)', border: 'none', color: '#fff', borderRadius: 6, padding: '4px 10px', fontSize: 12 }}>退出</button>
      </header>

      {view === 'home' && <HomeView stats={stats} list={list} status={status} onStatus={setStatus} onOpen={openDetail} />}
      {view === 'followup' && <FollowupView followups={followups} onOpen={openDetail} />}
      {view === 'me' && <MeView quota={quota} onFeedback={() => setFbOpen(true)} />}

      {/* 客户详情 */}
      {/* P0-3 修复(2026-09-20)：原 `view === 'home'` 闸导致从「我的/统计」等 Tab 点开
          客户时 detailId 已设但详情不渲染（点了没反应）。详情本身是 fixed 全屏覆盖层，
          任意 Tab 下都应显示，返回按钮只清 detailId 不动 view */}
      {detailId != null && (
        <DetailView
          detail={detail} chanCtx={chanCtx} chanKey={chanKey} jsSdkOk={jsSdkOk}
          onBack={backToList} onEdit={() => setEditOpen(true)} onEditTags={openTagDialog}
          onNewFollowup={() => setFuOpen(true)} onStage={openStageDialog} onNewTestDrive={openNewTestDrive}
          rec={rec} onFillInput={setInput}
          testDrives={testDrives} onEditTestDrive={openEditTestDrive} onSetTDStatus={setTDStatus}
          msgs={msgs} chatRef={chatRef} convId={convId} convMode={convMode} aiOn={aiOn}
          onClearDelay={clearDelay} onTakeover={takeover} onTransferBackAI={transferBackAI} onToggleAI={toggleAI}
          tlConv={tlConv} tlMsgs={tlMsgs} onToggleTimeline={toggleTimeline}
          rate={rate} onRate={setRate} onSubmitRating={submitRating}
          input={input} onInput={setInput} onSend={send}
        />
      )}

      {/* G-20：底部导航栏 aria-label 标注导航用途，aria-current 标记当前激活页签 */}
      <nav aria-label="顾问工作台导航" style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', display: 'flex' }}>
        {NAV_TABS.map((t) => <button key={t.k} onClick={() => { setView(t.k); detailIdRef.current = null; setDetailId(null) }} aria-current={view === t.k ? 'page' : undefined} style={{ flex: 1, padding: '10px 0', border: 'none', background: 'none', color: view === t.k ? 'var(--pri)' : '#a0aec0', fontWeight: view === t.k ? 600 : 400 }}>{t.t}</button>)}
      </nav>

      <EditDialog visible={editOpen} customer={detail?.customer} onClose={() => setEditOpen(false)} onConfirm={() => saveEdit()} />
      <TagDialog visible={tagOpen} allTags={allTags} checkedTags={checkedTags}
        onToggle={(t, on) => setCheckedTags(on ? [...checkedTags, t] : checkedTags.filter((x) => x !== t))}
        onClose={() => setTagOpen(false)} onConfirm={saveTags} />
      <FeedbackDialog visible={fbOpen} value={fbText} onChange={setFbText} onClose={() => setFbOpen(false)} onConfirm={submitFeedback} />
      <FollowupDialog visible={fuOpen} form={fuForm} onForm={setFuForm} onClose={() => setFuOpen(false)} onConfirm={saveFollowup} />
      <StageDialog visible={stageOpen} form={stageForm} onForm={setStageForm} onClose={() => setStageOpen(false)} onConfirm={saveStage} />
      <TestDriveDialog visible={tdOpen} editing={tdEdit} form={tdForm} onForm={setTdForm} onClose={() => setTdOpen(false)} onConfirm={saveTestDrive} />
    </div>
  )

  // 保存客户资料编辑：从表单DOM读取姓名/手机/车型/预算/备注后 PUT /customer/:id/info
  async function saveEdit() {
    if (!detailId) return
    const body: Record<string, unknown> = {}
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
