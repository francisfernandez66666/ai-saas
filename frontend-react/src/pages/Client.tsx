// C 端客户对话页：访客身份以 visitor_key 标识（避免凭客户 ID 越权读取对话）
// 首次进入若无 visitor_key 则向后端申请，后续会话持久化复用，保证同一访客身份连续
import { useState, useEffect, useRef } from 'react'
import { useBrand } from '../lib/branding'
import { useClientWS } from '../lib/realtime'
import { Msg } from '../types'

const API = '/api/v1'
// 本地持久化客户身份 ID，避免刷新后会话丢失（匿名访客态）
const LS_ID = 'scrm_customer_id'
// C3：访客密钥，匿名访问 /chat/history、/chat/welcome 必须携带，防横向越权
const LS_KEY = 'scrm_visitor_key'

// C 端客户聊天页：匿名访客与 AI 对话，支持人机验证（Turnstile）、会话合并、AI 跟进追问
// 注：Msg 类型已从 ../types 导入（上方 import），统一领域口径
// 依赖 /api/v1/chat/history、/api/v1/chat/welcome、/api/v1/chat/test、/api/v1/chat/guest、/api/v1/turnstile/sitekey
export default function Client() {
  const brand = useBrand()
  const [msgs, setMsgs] = useState<Msg[]>([])
  const [wsCid, setWsCid] = useState<number | null>(null)
  const [wsVk, setWsVk] = useState<string | null>(null)
  const [input, setInput] = useState('')
  const [convId, setConvId] = useState<number>(0)
  const [typing, setTyping] = useState(false)
  const [online, setOnline] = useState(true)
  const [tsEnabled, setTsEnabled] = useState(false)
  const [tsOk, setTsOk] = useState(false)
  const [tsToken, setTsToken] = useState('')
  const custId = useRef<number>(1)
  const localIds = useRef<Set<string>>(new Set())
  const listRef = useRef<HTMLDivElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  const tsHeaders = () => (tsToken ? { 'X-Turnstile-Token': tsToken } : {})
  // 工作时段判定：9:00~18:00 视为在线，影响"正在输入"等待时长与在线状态展示
  const isWork = () => { const h = new Date().getHours(); return h >= 9 && h < 18 }
  // 非工作时段模拟回复更慢（5~10s），工作时段 3~5s，营造真实人工节奏
  const typingDelay = () => isWork() ? 3000 + Math.random() * 2000 : 5000 + Math.random() * 5000

  function scrollBottom() { setTimeout(() => { if (listRef.current) listRef.current.scrollTop = listRef.current.scrollHeight }, 50) }

  async function loadHistory() {
    const r = await fetch(`${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`)
    const j = await r.json()
    if (j.code === 0 && j.data) {
      setMsgs(j.data)
      localIds.current = new Set(j.data.filter((m: Msg) => m.id != null).map((m: Msg) => String(m.id)))
      if (j.data.length > 0) setConvId(j.data[j.data.length - 1].conversation_id || 0)
    }
  }
  async function callWelcome() {
    const r = await fetch(`${API}/chat/welcome`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ customer_id: custId.current, visitor_key: localStorage.getItem(LS_KEY) || '' }) })
    const j = await r.json()
    if (j.code === 0 && j.data) {
      setConvId(j.data.conversation_id || 0)
      const w = j.data.welcome_message
      if (w) { setMsgs((m) => [...m, w]); localIds.current.add(String(w.id)) }
    } else {
      setMsgs((m) => [...m, { sender_type: 'system', content: '你好，欢迎咨询！我正在为你匹配专属顾问，请稍候~' }])
    }
    scrollBottom()
  }
  async function poll() {
    let url = convId ? `${API}/chat/history?conversation_id=${convId}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50` : `${API}/chat/history?customer_id=${custId.current}&visitor_key=${localStorage.getItem(LS_KEY) || ''}&limit=50`
    try {
      const r = await fetch(url); const j = await r.json()
      if (j.code === 0 && j.data && j.data.length) {
        let hasNew = false
        const next = [...msgs]
        j.data.forEach((m: Msg) => {
          if (m.id != null && !localIds.current.has(String(m.id))) {
            next.push(m); localIds.current.add(String(m.id)); hasNew = true
            if (m.conversation_id && !convId) setConvId(m.conversation_id)
          }
        })
        if (hasNew) { setMsgs(next); setTyping(false); scrollBottom() }
      }
    } catch {}
  }

  async function send() {
    const content = input.trim(); if (!content) return
    // 安全：开启人机验证（Turnstile）且未通过时禁止发送，防止脚本刷对话
    if (tsEnabled && !tsOk) { alert('请先完成人机验证'); return }
    setInput('')
    const temp: Msg = { id: 'temp_' + Date.now(), sender_type: 'customer', content, created_at: new Date().toISOString() }
    setMsgs((m) => [...m, temp]); localIds.current.add(String(temp.id)); scrollBottom()
    // 先按节奏展示"正在输入"，超时 60s 仍未回复则提示顾问忙碌（避免用户无限等待）
    const t1 = setTimeout(() => setTyping(true), typingDelay())
    const t2 = setTimeout(() => { setTyping(false); setMsgs((m) => [...m, { sender_type: 'system', content: '顾问可能正在忙碌中，请稍候' }]) }, 60000)
    try {
      const r = await fetch(`${API}/chat/test`, { method: 'POST', headers: Object.assign({ 'Content-Type': 'application/json' }, tsHeaders()), body: JSON.stringify({ customer_id: custId.current, content }) })
      const j = await r.json()
      if (j.code === 0 && j.data) {
        if (j.data.conversation_id) setConvId(j.data.conversation_id)
        // 身份合并（OneID）：服务端可能将本次访客合并到已有客户，需同步更新本地 ID 与持久化
        const merged = j.data.merged_customer_id || j.data.mergedCustomerId
        if (merged && merged > 0 && merged !== custId.current) { custId.current = merged; localStorage.setItem(LS_ID, String(merged)) }
        // C3：服务端下发的访客密钥持久化，供 history/welcome 携带
        if (j.data.visitor_key) localStorage.setItem(LS_KEY, j.data.visitor_key)
        const dbId = j.data.customer_msg_id
        if (dbId) setMsgs((m) => m.map((x) => x.id === temp.id ? { ...x, id: dbId } : x))
        let replies: Msg[] = []
        if (j.data.assistant_messages?.length) replies = j.data.assistant_messages
        else if (j.data.message?.content) replies = [j.data.message]
        else if (j.data.ai_reply) replies = [{ id: Date.now(), sender_type: 'ai', content: j.data.ai_reply, created_at: new Date().toISOString() }]
        if (replies.length) {
          setMsgs((m) => [...m, ...replies])
          replies.forEach((rp) => rp.id != null && localIds.current.add(String(rp.id)))
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

  // 人机验证（Cloudflare Turnstile）：站点开启时动态加载脚本并渲染验证框，通过回调拿到 token
  // 安全：token 随 chat 请求头 X-Turnstile-Token 上报，用于拦截机器人与刷量
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
    const params = new URLSearchParams(window.location.search)
    const override = params.get('customer_id')
    const stored = localStorage.getItem(LS_ID)
    if (override) { custId.current = parseInt(override); setWsCid(custId.current); setWsVk(localStorage.getItem(LS_KEY) || null) }
    else if (stored) { custId.current = parseInt(stored); setWsCid(custId.current); setWsVk(localStorage.getItem(LS_KEY) || null) }
    else { (async () => { try { const r = await fetch(`${API}/chat/guest`, { method: 'POST', headers: tsHeaders() }); const j = await r.json(); if (j.code === 0 && j.customer_id) { custId.current = j.customer_id; localStorage.setItem(LS_ID, String(custId.current)); if (j.visitor_key) localStorage.setItem(LS_KEY, j.visitor_key); setWsCid(custId.current); setWsVk(j.visitor_key || localStorage.getItem(LS_KEY) || null) } } catch {} })() }
    initTurnstile()
    loadHistory().then(() => { if (convId === 0 && custId.current) callWelcome() })
    setOnline(isWork())
    // 每 5s 轮询新消息与在线状态
    const t = setInterval(() => { setOnline(isWork()); poll() }, 5000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // P1-2 实时推送：客户身份就绪后连 WS，收到新消息信号即触发即时轮询（5s 轮询保留兜底）
  useClientWS(wsCid, wsVk, () => poll())

  return (
    <div style={{ maxWidth: 480, margin: '0 auto', height: '100vh', display: 'flex', flexDirection: 'column', background: '#f7f7f7' }}>
      <header style={{ background: brand.primaryColor || '#16a34a', color: '#fff', padding: '12px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          {brand.logoUrl && <img src={brand.logoUrl} alt="" style={{ height: 24, borderRadius: 4 }} />}
          <span style={{ fontWeight: 600 }}>{brand.brandName}</span>
        </div>
        <span style={{ fontSize: 12 }}>{online ? '🟢 在线' : '🌙 离线'}</span>
      </header>

      <div ref={listRef} style={{ flex: 1, overflowY: 'auto', padding: 12, display: 'flex', flexDirection: 'column', gap: 10 }}>
        {msgs.map((m, i) => {
          if (m.sender_type === 'system') return <div key={i} style={{ textAlign: 'center', fontSize: 12, color: '#9ca3af' }}>{m.content}</div>
          const mine = m.sender_type === 'customer'
          return (
            <div key={i} style={{ display: 'flex', justifyContent: mine ? 'flex-end' : 'flex-start' }}>
              <div style={{ maxWidth: '75%', padding: '8px 12px', borderRadius: 12, fontSize: 14, lineHeight: 1.5, color: mine ? '#fff' : '#1f2937', background: mine ? '#16a34a' : '#fff', border: mine ? 'none' : '1px solid #f0f0f0' }}>
                {m.content}
                {m.created_at && <div style={{ fontSize: 10, opacity: 0.7, textAlign: 'right', marginTop: 2 }}>{new Date(m.created_at).toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })}</div>}
              </div>
            </div>
          )
        })}
        {typing && <div style={{ display: 'flex', justifyContent: 'flex-start' }}><div style={{ background: '#fff', border: '1px solid #f0f0f0', borderRadius: 12, padding: '10px 14px', fontSize: 14, color: '#9ca3af' }}>正在输入…</div></div>}
      </div>

      {tsEnabled && !tsOk && <div id="ts-box" style={{ display: 'flex', justifyContent: 'center', padding: '8px 0' }} />}

      <div style={{ background: '#fff', borderTop: '1px solid #e5e7eb', padding: 10, display: 'flex', gap: 8, alignItems: 'center' }}>
        <input ref={inputRef} value={input} onChange={(e) => setInput(e.target.value)} onKeyDown={(e) => { if (e.key === 'Enter') send() }} placeholder="输入你的问题…" style={{ flex: 1, padding: '10px 12px', border: '1px solid #e5e7eb', borderRadius: 20, outline: 'none', fontSize: 14 }} />
        <button onClick={send} disabled={(tsEnabled && !tsOk)} style={{ background: brand.primaryColor || '#16a34a', color: '#fff', border: 'none', borderRadius: 20, padding: '10px 18px', fontSize: 14, fontWeight: 600, opacity: (tsEnabled && !tsOk) ? 0.5 : 1 }}>发送</button>
      </div>
    </div>
  )
}
