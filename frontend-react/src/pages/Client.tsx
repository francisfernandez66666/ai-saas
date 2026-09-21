/**
 * Client.tsx：C 端客户对话页
 * 访客身份以 visitor_key 标识（避免凭客户 ID 越权读取对话）
 * 首次进入若无 visitor_key 则向后端申请，后续会话持久化复用，保证同一访客身份连续
 * 支持人机验证（Cloudflare Turnstile）、会话合并（OneID）、AI 跟进追问
 */
import { useState, useEffect, useRef } from 'react'
import { useBrand } from '../lib/branding'
import { useClientWS } from '../lib/realtime'
import { apiFetch, getToken } from '../lib/api'
import { ConfirmDialog } from '../lib/ui'
import { confirmDialog, uiAlert } from '../lib/confirm'
import { Msg } from '../types'
import { collectFreshMessages, filterReplyMessages, promoteTempAndRegister, dropSystemNotice } from '../lib/chat'

// API 基础路径
const API = '/api/v1'
// 本地持久化客户身份 ID，避免刷新后会话丢失（匿名访客态）
const LS_ID = 'scrm_customer_id'
// C3：访客密钥，匿名访问 /chat/history、/chat/welcome 必须携带，防横向越权
const LS_KEY = 'scrm_visitor_key'
// 模块级访客创建去重：StrictMode 双挂载/连续进入页面时，防止并发重复建客
// Q1 修复(2026-09-12)：G-13 信封统一后 /chat/guest 响应为 {code,data} 形态，
// 旧扁平类型标注导致 j.data 访问 TS2339（CI tsc --noEmit 必红），类型对齐实际契约
let guestPromise: Promise<{ code: number; data?: { customer_id?: number; name?: string; visitor_key?: string } }> | null = null

/**
 * C 端客户聊天页组件
 * 匿名访客与 AI 对话，支持以下核心功能：
 * 1. 访客身份管理（visitor_key + customer_id）
 * 2. 人机验证（Cloudflare Turnstile）
 * 3. 实时消息推送（WebSocket + 5s 轮询兜底）
 * 4. 会话合并（OneID：服务端可能合并访客到已有客户）
 * 5. AI 跟进追问（按延迟秒数展示）
 */
export default function Client() {
  const brand = useBrand()
  // 消息列表
  const [msgs, setMsgs] = useState<Msg[]>([])
  // WebSocket 连接用的客户 ID 和访客密钥
  const [wsCid, setWsCid] = useState<number | null>(null)
  const [wsVk, setWsVk] = useState<string | null>(null)
  // 输入框内容
  const [input, setInput] = useState('')
  const [privacyBusy, setPrivacyBusy] = useState(false)
  // 当前会话 ID（用于历史记录查询）
  const [convId, setConvId] = useState<number>(0)
  // 会话 ID 引用：轮询/欢迎判断走 ref，避免闭包捕获首帧旧值导致逻辑错乱
  const convIdRef = useRef<number>(0)
  // 是否显示"正在输入"状态
  const [typing, setTyping] = useState(false)
  // 在线状态（受工作时段影响）
  const [online, setOnline] = useState(true)
  // 人机验证（Turnstile）相关状态
  const [tsEnabled, setTsEnabled] = useState(false)  // 是否启用人机验证
  const [tsOk, setTsOk] = useState(false)             // 人机验证是否通过
  const [tsToken, setTsToken] = useState('')          // 人机验证 token
  // 客户 ID 引用（避免频繁 setState）
  const custId = useRef<number>(1)
  // 已展示消息 ID 集合（防重复加载）
  const localIds = useRef<Set<string>>(new Set())
  // 消息列表 DOM 引用（用于自动滚动到底部）
  const listRef = useRef<HTMLDivElement>(null)
  // 输入框 DOM 引用
  const inputRef = useRef<HTMLInputElement>(null)

  // 构建人机验证请求头
  const tsHeaders = () => (tsToken ? { 'X-Turnstile-Token': tsToken } : {})
  /**
   * 工作时段判定：9:00~18:00 视为在线
   * 影响"正在输入"等待时长与在线状态展示
   */
  const isWork = () => { const h = new Date().getHours(); return h >= 9 && h < 18 }
  /**
   * 模拟人工回复延迟：非工作时段 5~10s，工作时段 3~5s
   * 营造真实人工节奏，避免用户感知到纯 AI 回复
   */
  const typingDelay = () => isWork() ? 3000 + Math.random() * 2000 : 5000 + Math.random() * 5000

  /** 自动滚动消息列表到底部 */
  function scrollBottom() { setTimeout(() => { if (listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight }, 50) }

  /**
   * 加载历史消息：从后端获取最近 50 条消息
   * 同时更新 localIds（防重复）和 convId（会话 ID）
   */
  async function loadHistory() {
    const r = await apiFetch(`${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`)
    const j = await r.json()
    if (j.code === 0 && j.data) {
      setMsgs(j.data)
      localIds.current = new Set(j.data.filter((m: Msg) => m.id != null).map((m: Msg) => String(m.id)))
      if (j.data.length > 0) {
        const cid = j.data[j.data.length - 1].conversation_id || 0
        setConvId(cid)
        convIdRef.current = cid
      }
    }
  }
  /**
   * 获取欢迎消息：首次进入时调用，获取 AI 预设的欢迎语
   * 失败时降级为默认欢迎文案
   */
  async function callWelcome() {
    // 修复(2026-09-16C)：后端 CheckVisitorKey 只读 query 参数，旧版把 visitor_key 放在
    // body 里——匿名访客的 welcome 一直 403，页面显示的其实是本文件兜底欢迎语，
    // 真实欢迎消息/会话复用从未生效。改走 query，与 history/chat.test 口径一致。
    const vkQ = encodeURIComponent(localStorage.getItem(LS_KEY) || '')
    const r = await apiFetch(`${API}/chat/welcome?visitor_key=${vkQ}`, { method: 'POST', body: JSON.stringify({ customer_id: custId.current }) })
    const j = await r.json()
    if (j.code === 0 && j.data) {
      const cid = j.data.conversation_id || 0
      setConvId(cid)
      convIdRef.current = cid
      const w = j.data.welcome_message
      if (w) { setMsgs((m) => [...m, w]); localIds.current.add(String(w.id)) }
    } else {
      setMsgs((m) => [...m, { sender_type: 'system', content: '你好，欢迎咨询！我正在为你匹配专属顾问，请稍候~' }])
    }
    scrollBottom()
  }
  /**
   * 轮询新消息：每 5s 调用一次
   * 按 convId 查询当前会话，无新消息时保持静默
   * 修复：用函数式 setState 追加新消息，避免闭包捕获旧 msgs 把整条列表替换掉
   */
  async function poll() {
    const cid = convIdRef.current
    const url = cid ? `${API}/chat/history?conversation_id=${cid}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50` : `${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`
    try {
      const r = await apiFetch(url); const j = await r.json()
      if (j.code === 0 && j.data && j.data.length) {
        // 从历史全量里挑出"未见过"的新消息（按 ID 去重，只追加不替换）
        const fresh = collectFreshMessages(j.data as Msg[], localIds.current)
        if (fresh.length) {
          j.data.forEach((m: Msg) => {
            if (m.conversation_id && !convIdRef.current) convIdRef.current = m.conversation_id
          })
          setConvId(convIdRef.current)
          setMsgs((m) => [...m, ...fresh])
          setTyping(false)
          scrollBottom()
        }
      }
    } catch { /* 取历史失败静默 */ }
  }

  /**
   * 发送消息：核心业务流程
   * 1. 校验人机验证（如已启用）
   * 2. 创建临时消息（乐观更新）
   * 3. 设置"正在输入"延迟与超时提示
   * 4. 调用 /api/v1/chat/test 发送消息
   * 5. 处理响应：会话合并、AI 回复、跟进追问
   */
  async function send() {
    const content = input.trim(); if (!content) return
    // 安全：开启人机验证（Turnstile）且未通过时禁止发送，防止脚本刷对话
    if (tsEnabled && !tsOk) { setMsgs((m) => [...m, { sender_type: 'system', content: '请先完成下方人机验证再发送' }]); scrollBottom(); return }
    setInput('')
    // 创建临时消息（乐观更新），使用时间戳作为临时 ID
    const temp: Msg = { id: 'temp_' + Date.now(), sender_type: 'customer', content, created_at: new Date().toISOString() }
    setMsgs((m) => [...m, temp]); localIds.current.add(String(temp.id)); scrollBottom()
    // 先按节奏展示"正在输入"，超时 60s 仍未回复则提示顾问忙碌（避免用户无限等待）
    const t1 = setTimeout(() => setTyping(true), typingDelay())
    const t2 = setTimeout(() => { setTyping(false); setMsgs((m) => [...m, { sender_type: 'system', content: '顾问可能正在忙碌中，请稍候' }]) }, 60000)
    try {
      const vk = localStorage.getItem(LS_KEY) || ''
      // visitor_key 走 query（CheckVisitorKey 仅读 query），与 history/welcome 一致
      // C3：改走统一请求层；但 AI 生成链路耗时可远超默认 30s（smoke 侧 --max-time 60），
      // 故对本条显式 timeoutMs:0 禁用超时，避免"收口即截断"的功能回归。
      const r = await apiFetch(`${API}/chat/test?visitor_key=${encodeURIComponent(vk)}`, { method: 'POST', headers: tsHeaders(), timeoutMs: 0, body: JSON.stringify({ customer_id: custId.current, content }) })
      const j = await r.json()
      if (j.code === 0 && j.data) {
        if (j.data.conversation_id) { setConvId(j.data.conversation_id); convIdRef.current = j.data.conversation_id }
        // 身份合并（OneID）：服务端可能将本次访客合并到已有客户，需同步更新本地 ID 与持久化
        const merged = j.data.merged_customer_id || j.data.mergedCustomerId
        if (merged && merged > 0 && merged !== custId.current) { custId.current = merged; localStorage.setItem(LS_ID, String(merged)) }
        // C3：访客密钥已在 CreateGuest 时持久化（chat/test 不返回，此处无需重复处理）
        // 替换临时消息 ID 为真实数据库 ID；同时把 dbId 登记进 localIds（promoteTempAndRegister），
        // 避免 HTTP 响应先到时 poll/WS 再拉一次历史，同一条客户消息被二次渲染成双气泡。
        const dbId = j.data.customer_msg_id
        if (dbId) {
          setMsgs((m) => promoteTempAndRegister(m, String(temp.id), dbId, localIds.current))
          // 收到有效响应即清理长延迟插入的"顾问可能正在忙碌中"占位，避免占位+真实回复并存
          setMsgs((m) => dropSystemNotice(m, '顾问可能正在忙碌中，请稍候'))
        }
        // 解析 AI 回复（支持多种响应格式）
        // 修复：过滤 system 型瞬时确认语 + 已由轮询先到达的同 ID 消息，避免重复气泡/引导词循环
        let replies: Msg[] = []
        if (j.data.assistant_messages?.length) replies = j.data.assistant_messages
        else if (j.data.message?.content) replies = [j.data.message]
        else if (j.data.ai_reply) replies = [{ id: Date.now(), sender_type: 'ai', content: j.data.ai_reply, created_at: new Date().toISOString() }]
        const freshReplies = filterReplyMessages(replies, localIds.current)
        if (freshReplies.length) {
          setMsgs((m) => [...m, ...freshReplies])
          freshReplies.forEach((rp) => rp.id != null && localIds.current.add(String(rp.id)))
          setTyping(false); scrollBottom()
        }
        // 服务端下发的 AI 跟进追问：按延迟秒数提前 2s 显示"正在输入"，展示 15s 后收起
        if (j.data.follow_up) {
          const d = (j.data.follow_up.delay_seconds || 0) * 1000 - 2000
          setTimeout(() => { setTyping(true); setTimeout(() => setTyping(false), 15000) }, d > 0 ? d : 0)
        }
      }
    } catch { setMsgs((m) => [...m, { sender_type: 'system', content: '消息发送失败，请重试~' }]) }
    finally { clearTimeout(t1); clearTimeout(t2); setTyping(false) }
  }

  // E3 修复(2026-09-14)：C 端"找人工"——旧版转人工只能等 AI 触发，客户主动求助无入口。
  // 二次确认用项目 ConfirmDialog（非原生 confirm，遵循 G-20/E8 统一收口）；
  // 后端 visitor_key 自证后置会话 mode=human，前端本地追加系统提示，AI 停止接管。
  const [humanBusy, setHumanBusy] = useState(false)
  const [humanConfirm, setHumanConfirm] = useState(false)
  async function doRequestHuman() {
    setHumanConfirm(false)
    setHumanBusy(true)
    try {
      const vk = localStorage.getItem(LS_KEY) || ''
      const r = await apiFetch(`${API}/chat/request-human?visitor_key=${encodeURIComponent(vk)}`, {
        method: 'POST', body: JSON.stringify({ customer_id: custId.current }),
      })
      const j = await r.json()
      if (j?.code === 0) {
        setTyping(false)
        setMsgs((m) => [...m, { sender_type: 'system', content: j.message || '已为你转接人工顾问，请稍候~' }])
        scrollBottom()
      } else {
        setMsgs((m) => [...m, { sender_type: 'system', content: j?.message || '转接失败，请稍后再试' }])
      }
    } catch {
      setMsgs((m) => [...m, { sender_type: 'system', content: '网络异常，请稍后再试' }])
    } finally { setHumanBusy(false) }
  }

  /**
   * 初始化人机验证（Cloudflare Turnstile）
   * 站点开启时动态加载 Turnstile 脚本并渲染验证框
   * 通过回调拿到 token，随 chat 请求头 X-Turnstile-Token 上报
   */
  async function initTurnstile() {
    try {
      const r = await apiFetch(`${API}/turnstile/sitekey`); const j = await r.json()
      if (!j.data?.enabled || !j.data?.site_key) return
      setTsEnabled(true)
      const s = document.createElement('script')
      s.src = 'https://challenges.cloudflare.com/turnstile/v0/api.js?onload=_tsOnload'
      s.async = true
      ;(window as any)._tsOnload = () => {
        const el = document.getElementById('ts-box')
        if (el && (window as any).turnstile) (window as any).turnstile.render(el, { sitekey: j.data.site_key, callback: (t: string) => { setTsToken(t); setTsOk(true) } })
      }
      document.head.appendChild(s)
    } catch { /* Turnstile 加载失败降级无验证码 */ }
  }

  useEffect(() => {
    // 初始化客户身份：优先从 URL 参数读取，其次从 localStorage 读取，最后申请新访客身份
    // 修复：串行 await 访客创建 → 历史加载 → 欢迎判断，消除身份竞态与重复欢迎
    initTurnstile()
    setOnline(isWork())
    ;(async () => {
      const params = new URLSearchParams(window.location.search)
      // P0-7 修复(2026-09-09)：customer_id URL 覆盖是匿名冒充任意客户的第一跳板，仅 dev 模式生效，
      // 且此时服务端对携带 visitor_key 的客户强制校验，生产环境（非 dev）一律忽略覆盖走访客身份。
      const override = import.meta.env.DEV ? params.get('customer_id') : null
      const stored = localStorage.getItem(LS_ID)
      let cid = override ? parseInt(override) : (stored ? parseInt(stored) : 0)
      if (cid > 0) {
        custId.current = cid
        setWsCid(cid); setWsVk(localStorage.getItem(LS_KEY) || null)
      } else {
        try {
          // 访客创建去重：复用模块级进行中的请求，StrictMode 双挂载不重复建客
          if (!guestPromise) {
            guestPromise = apiFetch(`${API}/chat/guest`, { method: 'POST', headers: tsHeaders() }).then((r) => r.json()).finally(() => { guestPromise = null })
          }
          const j = await guestPromise
          // G-13 信封统一(2026-09-11)：/chat/guest 响应已收口到 RespOK 的 {code,data} 形态，
          // customer_id/visitor_key 移入 data 下（此前读扁平 j.customer_id 恒 undefined，C端建客死锁）
          const gd = j.data || {}
          if (j.code === 0 && gd.customer_id) {
            cid = gd.customer_id
            custId.current = cid
            localStorage.setItem(LS_ID, String(cid))
            if (gd.visitor_key) localStorage.setItem(LS_KEY, gd.visitor_key)
            setWsCid(cid); setWsVk(gd.visitor_key || localStorage.getItem(LS_KEY) || null)
          }
        } catch { /* 欢迎语失败静默 */ }
      }
      await loadHistory()
      // 仅当该客户还没有任何会话/历史时才发欢迎语，避免刷新或切页重复出现"顾问正在接通中"
      if (convIdRef.current === 0 && custId.current) await callWelcome()
    })()
    // 每 5s 轮询新消息与在线状态
    const t = setInterval(() => { setOnline(isWork()); poll() }, 5000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // P1-2 实时推送：客户身份就绪后连 WS，收到新消息信号即触发即时轮询（5s 轮询保留兜底）
  useClientWS(wsCid, wsVk, () => poll())

  /**
   * F13/C2：C 端 PIPL 删除权入口。
   * 匿名访客用 visitor_key 自证；登录态可额外带 Authorization，后端按 user_id 放行本人关联客户。
   */
  async function requestCustomerDeletion() {
    if (!custId.current) { uiAlert('请先开始对话'); return }
    if (privacyBusy) return
    if (!(await confirmDialog('申请删除你的咨询资料？提交后将在到期前匿名化，历史内容不再用于后续服务。'))) return
    setPrivacyBusy(true)
    try {
      const token = getToken()
      const headers: Record<string, string> = { 'Content-Type': 'application/json' }
      if (token) headers.Authorization = 'Bearer ' + token
      const r = await apiFetch(`${API}/privacy/deletion-request`, {
        method: 'POST',
        headers,
        body: JSON.stringify({
          scope: 'customer',
          customer_id: custId.current,
          visitor_key: localStorage.getItem(LS_KEY) || '',
        }),
      })
      const j = await r.json().catch(() => null)
      if (j?.code === 0) {
        const deadline = j.data?.deadline ? new Date(j.data.deadline).toLocaleString('zh-CN', { hour12: false }) : ''
        setMsgs((m) => [...m, { sender_type: 'system', content: `已受理删除申请${deadline ? '，预计 ' + deadline : ''}` }])
        scrollBottom()
      } else {
        uiAlert(j?.message || '删除申请提交失败')
      }
    } catch {
      uiAlert('删除申请提交失败')
    } finally {
      setPrivacyBusy(false)
    }
  }

  return (
    <div style={{ maxWidth: 480, margin: '0 auto', height: '100vh', display: 'flex', flexDirection: 'column', background: 'var(--bg, #f5f7fa)' }}>
      {/* 顶栏：品牌 Logo/名称 + 在线状态（主色统一走品牌/--pri） */}
      <header style={{ background: brand.primaryColor || 'var(--pri, #4f46e5)', color: '#fff', padding: '12px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {brand.logoUrl && <img src={brand.logoUrl} alt="" style={{ height: 24, borderRadius: 4 }} />}
          <span style={{ fontWeight: 600 }}>{brand.brandName}</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <button onClick={requestCustomerDeletion} disabled={privacyBusy} aria-label="申请删除我的资料" style={{ background: 'rgba(255,255,255,.16)', border: 'none', color: '#fff', borderRadius: 6, padding: '3px 8px', fontSize: 11 }}>{privacyBusy ? '提交中' : '删除资料'}</button>
          {/* G-20：aria-label 无障碍标注——屏幕阅读器可识别在线/离线状态 */}
          <span style={{ fontSize: 12 }} aria-label={online ? '当前在线' : '当前离线'}>{online ? '🟢 在线' : '🌙 离线'}</span>
        </div>
      </header>

      {/* 消息列表区域：根据 sender_type 区分客户消息（右侧主色）与 AI/系统消息（左侧白色） */}
      {/* G-20：role="log" + aria-label 标注消息列表区域，辅助技术可感知消息流变化 */}
      <div ref={listRef} role="log" aria-label="对话消息列表" style={{ flex: 1, overflowY: 'auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
        {msgs.map((m, i) => {
          if (m.sender_type === 'system') return <div key={i} style={{ textAlign: 'center', fontSize: 12, color: 'var(--text-muted, #9ca3af)' }}>{m.content}</div>
          const mine = m.sender_type === 'customer'
          return (
            <div key={i} style={{ display: 'flex', justifyContent: mine ? 'flex-end' : 'flex-start' }}>
              <div style={{ maxWidth: '75%', padding: '8px 12px', borderRadius: 12, fontSize: 14, lineHeight: 1.5, color: mine ? '#fff' : 'var(--text, #1f2937)', background: mine ? (brand.primaryColor || 'var(--pri, #4f46e5)') : '#fff', border: mine ? 'none' : '1px solid var(--border, #e5e7eb)' }}>
                {m.content}
                {m.created_at && <div style={{ fontSize: 10, opacity: 0.7, textAlign: 'right', marginTop: 2 }}>{new Date(m.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</div>}
              </div>
            </div>
          )
        })}
        {/* "正在输入"状态提示 */}
        {typing && <div style={{ display: 'flex', justifyContent: 'flex-start' }}><div style={{ background: '#fff', border: '1px solid var(--border, #e5e7eb)', borderRadius: 12, padding: '10px 14px', fontSize: 14, color: 'var(--text-muted, #9ca3af)' }}>正在输入…</div></div>}
      </div>

      {/* 人机验证区域（Turnstile）：站点开启时展示 */}
      {tsEnabled && !tsOk && <div id="ts-box" style={{ display: 'flex', justifyContent: 'center', padding: '8px 0' }} />}

      {/* 输入区域：文本输入框 + 发送按钮 */}
      {/* G-20：输入区域无障碍标注——role="form" 标识表单区域，aria-label 说明用途 */}
      <div style={{ background: '#fff', borderTop: '1px solid var(--border, #e5e7eb)', padding: 10, display: 'flex', gap: 8, alignItems: 'center' }} role="form" aria-label="消息输入区域">
        {/* G-20：输入框 aria-label 供屏幕阅读器识别此为消息输入用途 */}
        <input ref={inputRef} value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') send() }} placeholder="输入你的问题…" aria-label="消息输入框" style={{ flex: 1, padding: '10px 12px', border: '1px solid var(--border, #e5e7eb)', borderRadius: 20, outline: 'none', fontSize: 14 }} />
        {/* G-20：发送按钮 aria-label 标注操作意图；人机验证未通过时 disabled 并降低透明度 */}
        <button onClick={send} disabled={(tsEnabled && !tsOk)} aria-label="发送消息" style={{ background: brand.primaryColor || 'var(--pri, #4f46e5)', color: '#fff', border: 'none', borderRadius: 20, padding: '10px 18px', fontSize: 14, fontWeight: 600, opacity: (tsEnabled && !tsOk) ? 0.5 : 1 }}>发送</button>
        {/* E3：找人工入口——转接后真人接管，AI 停回自动回复 */}
        <button onClick={() => setHumanConfirm(true)} disabled={humanBusy} aria-label="转接人工顾问" style={{ background: '#fff', color: 'var(--pri, #4f46e5)', border: '1px solid var(--pri, #4f46e5)', borderRadius: 20, padding: '10px 14px', fontSize: 14, fontWeight: 600, opacity: humanBusy ? 0.5 : 1 }}>{humanBusy ? '转接中…' : '找人工'}</button>
      </div>
      <ConfirmDialog open={humanConfirm} title="转接人工顾问" message="转接后由真人顾问回复你，AI 助手将暂停自动回复。确认转接？" onConfirm={doRequestHuman} onCancel={() => setHumanConfirm(false)} />
    </div>
  )
}
