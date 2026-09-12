/**
 * Client.tsx：C 端客户对话页
 * 访客身份以 visitor_key 标识（避免凭客户 ID 越权读取对话）
 * 首次进入若无 visitor_key 则向后端申请，后续会话持久化复用，保证同一访客身份连续
 * 支持人机验证（Cloudflare Turnstile）、会话合并（OneID）、AI 跟进追问
 */
import { useState, useEffect, useRef } from 'react'
import { useBrand } from '../lib/branding'
import { useClientWS } from '../lib/realtime'
import { Msg } from '../types'
import { collectFreshMessages, filterReplyMessages, promoteTempAndRegister, dropSystemNotice } from '../lib/chat'

// API 基础路径
const API = '/api/v1'
// 本地持久化客户身份 ID，避免刷新后会话丢失（匿名访客态）
const LS_ID = 'scrm_customer_id'
// C3：访客密钥，匿名访问 /chat/history、/chat/welcome 必须携带，防横向越权
const LS_KEY = 'scrm_visitor_key'
// 模块级访客创建去重：StrictMode 双挂载/连续进入页面时，防止并发重复建客
let guestPromise: Promise<{ code: number; customer_id: number; visitor_key?: string }> | null = null

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
    const r = await fetch(`${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`)
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
    const r = await fetch(`${API}/chat/welcome`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ customer_id: custId.current, visitor_key: localStorage.getItem(LS_KEY) || '' }) })
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
    let url = cid ? `${API}/chat/history?conversation_id=${cid}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50` : `${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`
    try {
      const r = await fetch(url); const j = await r.json()
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
    } catch {}
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
    if (tsEnabled && !tsOk) { alert('请先完成人机验证'); return }
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
      const r = await fetch(`${API}/chat/test?visitor_key=${encodeURIComponent(vk)}`, { method: 'POST', headers: Object.assign({ 'Content-Type': 'application/json' }, tsHeaders()), body: JSON.stringify({ customer_id: custId.current, content }) })
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

  /**
   * 初始化人机验证（Cloudflare Turnstile）
   * 站点开启时动态加载 Turnstile 脚本并渲染验证框
   * 通过回调拿到 token，随 chat 请求头 X-Turnstile-Token 上报
   */
  async function initTurnstile() {
    try {
      const r = await fetch(`${API}/turnstile/sitekey`); const j = await r.json()
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
    } catch {}
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
            guestPromise = fetch(`${API}/chat/guest`, { method: 'POST', headers: tsHeaders() }).then((r) => r.json()).finally(() => { guestPromise = null })
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
        } catch {}
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

  return (
    <div style={{ maxWidth: 480, margin: '0 auto', height: '100vh', display: 'flex', flexDirection: 'column', background: 'var(--bg, #f5f7fa)' }}>
      {/* 顶栏：品牌 Logo/名称 + 在线状态（主色统一走品牌/--pri） */}
      <header style={{ background: brand.primaryColor || 'var(--pri, #4f46e5)', color: '#fff', padding: '12px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {brand.logoUrl && <img src={brand.logoUrl} alt="" style={{ height: 24, borderRadius: 4 }} />}
          <span style={{ fontWeight: 600 }}>{brand.brandName}</span>
        </div>
        {/* G-20：aria-label 无障碍标注——屏幕阅读器可识别在线/离线状态 */}
        <span style={{ fontSize: 12 }} aria-label={online ? '当前在线' : '当前离线'}>{online ? '🟢 在线' : '🌙 离线'}</span>
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
      </div>
    </div>
  )
}
