/**
 * Billing.tsx：订阅收银台页面
 * 展示套餐用量、商业包列表与订单；支持模拟支付/扫码人工确认两种方式
 * 依赖接口：/api/v1/billing/my-package、/api/v1/packages、/api/v1/billing/orders、/api/v1/billing/subscribe、/api/v1/billing/*-pay、/api/v1/billing/manual-confirm
 */
import { useState, useEffect } from 'react'
import { Dialog, Button, Input, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
// FIX-8(2026-09-27)：金额一律经 lib/money 换算（界面是元、提交体是分），不再各处 `x / 100`。
import { fenToYuan, fenToYuanCompact } from '../lib/money'
import { ConfirmDialog } from '../lib/ui'
import { getToken, apiFetch } from '../lib/api'
import { isStaff } from '../lib/roles'
import { useI18n } from '../i18n/useI18n'
import type { I18nKey } from '../i18n/context'


// 当前租户套餐用量类型（收银台顶部展示）
type Quota = { tenant_name: string; status: string; used_ai_calls: number; max_ai_calls: number; ai_call_balance: number; expired_at?: string; pay_mode?: string }
// 商业包类型（收银台列表）
type Pkg = { id: number; p_type: string; name: string; price_cents: number; description?: string; ai_calls: number; duration_days?: number }
// 订阅订单类型（我的订单列表）
// E1 修复(2026-09-14)：补 qr_content/refund_requested/invoice_status——后端一直下发，
// 前端旧版不渲染收款码（static_qr 模式下用户根本看不到码）
// §八-6(2026-09-18)：补 invoice_no——/billing/orders Select 白名单已含该列，issued 行展示发票号
type Order = { id: number; order_no: string; amount_cents: number; original_amount_cents?: number; package_name?: string; channel?: string; status: string; manual_confirm?: boolean; created_at: string; qr_content?: string; refund_requested?: boolean; invoice_status?: string; invoice_no?: string; refund_amount_cents?: number; refunded_at?: string }

// 收银台接口鉴权头（保留给个别需要手拼 header 的调用点）
const AUTH = (): { headers: Record<string, string> } => ({ headers: { Authorization: "Bearer " + getToken() } })
// P2 修复(2026-09-15)：收银台原全量裸 fetch——401 不跳登录/403 不触发改密拦截、
// 超管代管不带 X-Tenant-ID、无超时。统一换 apiFetch 包装：Authorization 由 apiFetch
// 注入（这里把调用点自带的 Authorization 摘掉避免重复），其余 opts 原样透传。
async function BFETCH(url: string, opts: RequestInit & { headers?: Record<string, string> } = {}): Promise<Response> {
  const h = { ...(opts.headers || {}) }
  delete h.Authorization
  return apiFetch(url, { ...opts, headers: h })
}
// P2-14：支付渠道展示名映射。key 是后端契约值（不翻译），value 是 I18nKey，
// 真正翻译发生在组件渲染时——模块级常量拿不到 t()。
const CHANNEL_KEYS: Record<string, I18nKey> = {
  mock: 'billing.chMock', manual: 'billing.chManual', wechat: 'billing.chWechat', alipay: 'billing.chAlipay',
}

/**
 * 订阅收银台组件
 * 核心功能：
 * 1. 顶部展示当前套餐用量（AI 调用次数、增量余额、到期日）
 * 2. 中部展示商业包列表（试用包/包月包/增量包），支持订阅
 * 3. 底部展示订单列表，待支付订单可继续操作
 * 4. 支付弹窗：支持模拟支付（测试）和人工确认（静态码）
 */
export default function Billing() {
  const { t } = useI18n()
  const brand = useBrand()
  /** 渠道码 → 展示名；未登记码原样带出（后端加了新渠道也要看得见）。 */
  const channelName = (c?: string) => {
    const k = c ? CHANNEL_KEYS[c] : undefined
    return k ? t(k) : (c || '-')
  }
  // P1-42(2026-09-09)：订单订阅/支付接口须管理员权限——成员角色(sales/user)看/
  // /app/billing 不应 403 白屏：只展示额度与套餐信息（订阅/支付/订单区隐藏）
  const isAdmin = isStaff(localStorage.getItem('role') || '')
  // 当前套餐用量
  const [quota, setQuota] = useState<Quota | null>(null)
  // 商业包列表
  const [pkgs, setPkgs] = useState<Pkg[]>([])
  // 订单列表
  const [orders, setOrders] = useState<Order[]>([])
  // 支付弹窗可见性
  const [modal, setModal] = useState(false)
  // 当前待支付订单
  const [cur, setCur] = useState<Order | null>(null)
  // 支付方式（mock=模拟、manual=静态码人工、sdk=在线支付）
  const [payMode, setPayMode] = useState('mock')
  // 支付弹窗提示信息
  const [msg, setMsg] = useState('')
  // 确认弹窗状态
  const [confirmOpen, setConfirmOpen] = useState(false)
  const [confirmTitle, setConfirmTitle] = useState('')
  const [confirmMsg, setConfirmMsg] = useState('')
  const [confirmFn, setConfirmFn] = useState<() => void>(() => {})
  // E9：发票申请弹窗（抬头/税号/邮箱随单提交，替代旧硬编码"AI-SCRM服务费"）
  const [invOpen, setInvOpen] = useState(false)
  const [invOrder, setInvOrder] = useState<Order | null>(null)
  const [invForm, setInvForm] = useState({ title: '', tax_no: '', email: '' })
  // P2-11 修复(2026-09-20 批三)：发票邮箱预填原读 localStorage['email']——全仓无任何写入点，恒为空串假预填。
  // 改从 /auth/me 拉绑定邮箱（后端 auth.go:29 一直下发 email 字段）
  const [meEmail, setMeEmail] = useState('')

  /** 加载当前套餐用量 */
  async function loadQuota() {
    const r = await BFETCH('/api/v1/billing/my-package', AUTH())
    const j = await r.json()
    if (j.code === 0) { setQuota(j.data); setPayMode(j.data.pay_mode) }
  }
  /** 加载商业包列表（过滤免费包） */
  async function loadPkgs() {
    const r = await BFETCH('/api/v1/packages')
    const j = await r.json()
    setPkgs((j.data || []).filter((p: Pkg) => p.p_type !== 'free'))
  }
  /** 加载订单列表（最近 50 条）；仅管理员可查（P1-42：成员角色不触发 403） */
  async function loadOrders() {
    if (!isAdmin) return
    try {
      const r = await BFETCH('/api/v1/billing/orders?limit=50', AUTH())
      const j = await r.json()
      if (j.code === 0) setOrders(j.data || [])
    } catch { /* 取数失败静默 */ }
  }
  /** 并行加载所有数据 */
  function loadAll() { loadQuota(); loadPkgs(); loadOrders() }

  // P2-5 修复(2026-09-19 审计批一)：到期倒计时基准时间改为 state（原渲染期直调
  // Date.now() 违反 react-hooks/purity——渲染必须是纯函数），随 15s 轮询刷新。
  const [nowMs, setNowMs] = useState(0)

  useEffect(() => {
    // 未登录时跳转登录页
    if (!getToken()) { location.href = '/login'; return }
    loadAll()
    // P2-11(2026-09-20 批三)：拉 /auth/me 绑定邮箱作发票预填（失败静默留空可手填）
    ;(async () => {
      try {
        const r = await apiFetch('/api/v1/auth/me')
        const j = await r.json().catch(() => null)
        if (j?.code === 0 && j.data?.email) setMeEmail(String(j.data.email))
      } catch { /* 断网/未绑定邮箱：保持空串 */ }
    })()
    setNowMs(Date.now())
    // 每 15s 轮询订单与套餐，自动刷新待支付/已到账状态
    // 局部变量名避开 t：t 是 i18n 翻译函数（useI18n），被局部同名变量遮蔽后
    // 同一作用域内调用 t('...') 会拿到 interval id 而类型报错
    const timer = setInterval(() => { loadOrders(); loadQuota(); setNowMs(Date.now()) }, 15000)
    return () => clearInterval(timer)
    // 挂载一次即注册 15s 轮询是刻意设计：loadAll/loadOrders/loadQuota 均为普通函数
    // 声明（每次渲染重建、引用不稳定），补进依赖数组会令本 effect 每渲染重建，
    // 轮询被反复 clear/重设乃至死循环。函数内部只 set state，不读外部可变值。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // E1 修复：sdk 模式支付弹窗内 5s 轮询到账状态，paid 即自动关闭并刷新
  useEffect(() => {
    if (!modal || !cur || payMode !== 'sdk') return
    const timer = setInterval(async () => {
      try {
        const r = await BFETCH(`/api/v1/billing/orders/${cur.id}`, AUTH())
        const j = await r.json()
        if (j.code === 0 && j.data?.status === 'paid') {
          MessagePlugin.success(t('billing.paidArrived'))
          setModal(false); loadOrders(); loadQuota()
        }
      } catch { /* 轮询失败静默重试 */ }
    }, 5000)
    return () => clearInterval(timer)
    // 同上：loadOrders 为普通函数声明、引用不稳定，补依赖会让弹窗内 5s 到账轮询
    // 每渲染重建；该函数只触发 set state 与请求，不捕获渲染期可变值。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [modal, cur, payMode, t])

  /**
   * 订阅商业包：调用 /api/v1/billing/subscribe 接口
   * 试用包直接发放，付费包创建订单后打开支付弹窗
   */
  async function subscribe(id: number) {
    const r = await BFETCH('/api/v1/billing/subscribe', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ package_id: id }) })
    const j = await r.json()
    if (j.code !== 0) { MessagePlugin.error(j.message || t('billing.subscribeFailed')); return }
    if (j.data.granted) { MessagePlugin.success(t('billing.trialGranted')); loadAll(); return }
    openPay(j.data.order, j.data.pay_mode)
  }
  /** 打开支付弹窗 */
  function openPay(order: Order, mode?: string) {
    setCur(order); setPayMode(mode || payMode); setMsg(''); setModal(true)
  }
  /** 模拟支付（测试环境）：调用 /api/v1/billing/orders/mock-pay 接口 */
  async function mockPay() {
    if (!cur) return
    const r = await BFETCH('/api/v1/billing/orders/mock-pay', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ order_id: cur.id }) })
    const j = await r.json()
    setMsg(j.message || '')
    if (j.code === 0) setTimeout(() => { setModal(false); loadOrders(); loadQuota() }, 900)
  }
  /** 人工确认已付款：调用 /api/v1/billing/manual-confirm 接口 */
  async function manualConfirm() {
    if (!cur) return
    const r = await BFETCH('/api/v1/billing/manual-confirm', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ order_id: cur.id }) })
    const j = await r.json()
    setMsg(j.message || '')
  }
  /** E9：提交发票申请（抬头必填；专票填税号；邮箱用于接收电子发票） */
  async function submitInvoice() {
    if (!invOrder) return
    if (!invForm.title.trim()) { MessagePlugin.warning(t('billing.invoiceTitleRequired')); return }
    const r = await BFETCH(`/api/v1/billing/orders/${invOrder.id}/invoice`, { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify(invForm) })
    const j = await r.json()
    if (j.code === 0) { MessagePlugin.success(t('billing.invoiceSubmitted')); setInvOpen(false); loadOrders() }
    else MessagePlugin.error(j.message || t('billing.invoiceFailed'))
  }

  // 未登录时不渲染
  if (!getToken()) return null

  return (
    <div className="px-4 py-4 lg:px-6" style={{ background: '#f5f7fa', minHeight: '100vh', color: '#2d3748' }}>
      {/* 顶栏：标题 + 支付方式说明；移动端允许换行避免挤出视口 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 18, gap: 10, flexWrap: 'wrap' }}>
        <div><h2 style={{ marginBottom: 4 }}>{t('billing.title')}</h2><div style={{ color: '#718096', fontSize: 13 }}>{t('billing.subtitle')}</div></div>
        <div><span style={{ fontSize: 12, color: '#718096' }}>{payMode === 'static_qr' ? t('billing.payModeStaticQr') : payMode === 'sdk' ? t('billing.payModeSdk') : t('billing.payModeMock')}</span>　<a href="/" style={{ color: 'var(--pri)' }}>{t('billing.home')}</a></div>
      </div>

      {/* 续费触达 banner（商业缺口批 2026-09-16）：到期/临期/待支付订单三态，此前租户侧对到期零感知 */}
      {quota && (() => {
        const days = quota.expired_at && nowMs ? Math.ceil((new Date(quota.expired_at).getTime() - nowMs) / 86400000) : NaN
        const pending = isAdmin ? orders.filter((o) => o.status === 'pending').length : 0
        let banner: { bg: string; bd: string; tx: string } | null = null
        if (quota.status === 'expired') {
          banner = { bg: '#fff5f5', bd: '#feb2b2', tx: t('billing.bannerExpired') }
        } else if (quota.status === 'trial' && !isNaN(days) && days <= 7) {
          banner = { bg: '#fffaf0', bd: '#fbd38d', tx: t('billing.bannerTrial', { days }) }
        } else if (!isNaN(days) && days <= 7 && days >= 0) {
          banner = { bg: '#fffaf0', bd: '#fbd38d', tx: t('billing.bannerPlan', { days }) }
        }
        if (!banner && pending > 0) banner = { bg: '#ebf8ff', bd: '#90cdf4', tx: t('billing.bannerPending', { n: pending }) }
        if (!banner) return null
        return (
          <div style={{ background: banner.bg, border: `1px solid ${banner.bd}`, borderRadius: 10, padding: '10px 16px', marginBottom: 16, fontSize: 13, color: '#2d3748' }}>
            {banner.tx}
            {quota.status === 'expired' && !isAdmin && <span style={{ color: '#718096' }}>{t('billing.contactAdmin')}</span>}
          </div>
        )
      })()}

      {/* 当前套餐用量展示区 */}
      <div style={{ background: '#fff', borderRadius: 12, padding: '16px 22px', boxShadow: '0 3px 14px rgba(0,0,0,.06)', display: 'flex', gap: 34, flexWrap: 'wrap', marginBottom: 26 }}>
        {!quota && <span style={{ color: '#718096' }}>{t('billing.loading')}</span>}
        {quota && (<>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.tenant_name}</b><span style={{ fontSize: 12, color: '#718096' }}>{quota.status === 'trial' ? t('billing.trialing') : quota.status}</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.used_ai_calls} / {quota.max_ai_calls || '∞'}</b><span style={{ fontSize: 12, color: '#718096' }}>{t('billing.aiCallsMonth')}</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.ai_call_balance}</b><span style={{ fontSize: 12, color: '#718096' }}>{t('billing.balance')}</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.expired_at ? new Date(quota.expired_at).toLocaleDateString() : '-'}</b><span style={{ fontSize: 12, color: '#718096' }}>{t('billing.expireDay')}</span></div>
        </>)}
      </div>

      {/* 商业包列表 */}
      <h2 style={{ fontSize: 17 }}>{t('billing.packages')}</h2>
      <p style={{ color: '#718096', fontSize: 13, marginBottom: 20 }}>{t('billing.packagesDesc')}</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(220px,1fr))', gap: 16, marginBottom: 28 }}>
        {pkgs.map((p) => (
          <div key={p.id} style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 3px 14px rgba(0,0,0,.06)', position: 'relative', display: 'flex', flexDirection: 'column' }}>
            {p.p_type === 'increment' && <span style={{ position: 'absolute', top: -8, right: 10, background: 'var(--pri)', color: '#fff', fontSize: 11, padding: '2px 9px', borderRadius: 10 }}>{t('billing.hot')}</span>}
            <b>{p.name}</b>
            <div style={{ fontSize: 24, fontWeight: 800, margin: '8px 0' }}>{p.price_cents > 0 ? '¥' + fenToYuanCompact(p.price_cents) : t('billing.free')}<small style={{ fontSize: 12, color: '#718096', fontWeight: 400 }}>{p.p_type === 'paid' ? t('billing.perMonth') : ''}</small></div>
            <p style={{ fontSize: 13, color: '#718096', margin: '8px 0 14px', flex: 1 }}>{p.description || ''}<br />{t('billing.pkgIncludes', { n: p.ai_calls })}{p.duration_days ? t('billing.pkgDuration', { days: p.duration_days }) : t('billing.pkgForever')}</p>
            <button onClick={() => isAdmin ? subscribe(p.id) : undefined} disabled={!isAdmin} aria-label={t('billing.subscribeAria') + p.name} style={{ background: 'linear-gradient(135deg,var(--pri),#764ba2)', color: '#fff', width: '100%', padding: '9px 14px', border: 'none', borderRadius: 8, cursor: isAdmin ? 'pointer' : 'not-allowed', fontWeight: 600, opacity: isAdmin ? 1 : .6 }}>{isAdmin ? t('billing.subscribeNow') : t('billing.subscribeNeedAdmin')}</button>
          </div>
        ))}
      </div>

      {/* 订单列表（P1-42：仅管理员可见，成员角色只读额度与套餐信息） */}
      {isAdmin && <>
      <h2 style={{ fontSize: 17 }}>{t('billing.myOrders')}</h2>
      <p style={{ color: '#718096', fontSize: 13, marginBottom: 20 }}>{t('billing.ordersDesc')}</p>
      <div className="overflow-x-auto">
      <table style={{ width: '100%', background: '#fff', borderRadius: 10, borderCollapse: 'collapse', overflow: 'hidden', boxShadow: '0 3px 14px rgba(0,0,0,.06)' }} className="min-w-[620px]">
        <thead><tr style={{ background: '#fafafa', color: '#4a5568' }}><th style={th}>{t('billing.colOrderNo')}</th><th style={th}>{t('billing.colAmount')}</th><th style={th}>{t('billing.colChannel')}</th><th style={th}>{t('billing.colStatus')}</th><th style={th}>{t('billing.colCreatedAt')}</th><th style={th}>{t('crud.actions')}</th></tr></thead>
        <tbody>
          {orders.length === 0 && <tr><td colSpan={6} style={{ ...td, textAlign: 'center', color: '#718096' }}>{t('billing.ordersEmpty')}</td></tr>}
          {orders.map((o) => (
            <tr key={o.id}>
              <td style={td}>{o.order_no}</td>
              <td style={td}>¥{fenToYuan(o.amount_cents)}<br /><span style={{ fontSize: 11, color: '#718096' }}>{o.package_name || ''}</span></td>
              <td style={td}>{channelName(o.channel)}</td>
              {/* F1(2026-09-15)：后端列表接口已补退款字段——已退款单显示中文态与实际退款金额，
                  旧版直接显示英文原值"refunded"且看不到退了多少钱 */}
              <td style={td}><span style={{ ...st, background: o.status === 'pending' ? '#feebc8' : o.status === 'refunded' ? '#fed7d7' : '#c6f6d5', color: o.status === 'pending' ? '#975a16' : o.status === 'refunded' ? '#9b2c2c' : '#276749' }}>{o.status === 'pending' ? (o.manual_confirm ? t('billing.statusPendingConfirm') : t('billing.statusPending')) : o.status === 'refunded' ? t('billing.statusRefunded', { amount: o.refund_amount_cents ? ' ¥' + fenToYuan(o.refund_amount_cents) : '' }) : o.status}</span></td>
              <td style={td}>{new Date(o.created_at).toLocaleString()}</td>
              <td style={td}>{o.status === 'pending' ? <button aria-label={t('billing.payContinueAria') + o.order_no} style={{ background: 'var(--pri)', color: '#fff', border: 'none', padding: '4px 10px', borderRadius: 6, cursor: 'pointer' }} onClick={() => openPay(o, payMode)}>{t('billing.payContinue')}</button> : <span style={{ display: 'inline-flex', gap: 4 }}>
                <span style={{ color: '#718096', fontSize: 12 }}>{t('billing.completed')}</span>
                {/* G-16：退款按钮——仅已完成订单可操作，弹出 ConfirmDialog 二次确认后调用 refund 接口 */}
                {/* B7 双轨：mock 即时退；static_qr/sdk 仅受理申请，超管审批后执行 */}
                {/* P2-12 修复(2026-09-20 批三)：status=refunded 的行旧版仍渲染退款/发票按钮——
                    再点必然 409/误申请，资金动作入口对终态单直接隐藏 */}
                {o.status !== 'refunded' && o.refund_requested && <span style={{ ...st, background: '#feebc8', color: '#975a16' }}>{t('billing.refundApproving')}</span>}
                {o.status !== 'refunded' && !o.refund_requested && <button aria-label={t('billing.refundAria') + o.order_no} style={{ background: 'none', border: '1px solid #e2e8f0', padding: '2px 8px', borderRadius: 4, cursor: 'pointer', fontSize: 11 }} onClick={() => {
                  setConfirmTitle(t('billing.refundTitle'))
                  setConfirmMsg(payMode === 'mock' ? t('billing.refundConfirmMock') : t('billing.refundConfirmManual'))
                  setConfirmFn(async () => {
                    const r = await BFETCH(`/api/v1/billing/orders/${o.id}/refund`, { ...AUTH(), method: 'POST' })
                    const j = await r.json()
                    if (j.code === 0) { MessagePlugin.success(j.message || t('billing.refundSubmitted')); loadOrders() } else MessagePlugin.error(j.message || t('billing.refundFailed'))
                  })
                  setConfirmOpen(true)
                }}>{t('billing.refund')}</button>}
                {/* E9：发票按钮——弹表单收集抬头/税号/邮箱（旧版硬编码"AI-SCRM服务费"且无税号位） */}
                {o.invoice_status === 'issued'
                  ? <span style={{ color: '#276749', fontSize: 12 }}>{t('billing.invoiceIssued')}{o.invoice_no ? `（${o.invoice_no}）` : ''}</span>
                  : o.invoice_status === 'requested'
                    ? <span style={{ color: '#975a16', fontSize: 12 }}>{t('billing.invoiceRequested')}</span>
                    : o.status !== 'refunded' && <button aria-label={t('billing.invoiceAria') + o.order_no} style={{ background: 'none', border: '1px solid #e2e8f0', padding: '2px 8px', borderRadius: 4, cursor: 'pointer', fontSize: 11 }} onClick={() => {
                      setInvOrder(o); setInvForm({ title: '', tax_no: '', email: meEmail }); setInvOpen(true)
                    }}>{t('billing.invoice')}</button>}
              </span>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      </div>
      </>}

      {/* 支付弹窗：展示订单信息与支付操作（仅管理员可用） */}
      <Dialog header={t('billing.payTitle')} visible={modal} onClose={() => { setModal(false); loadOrders(); loadQuota() }} footer={false}>
        {cur && <>
          <p style={{ fontSize: 13, color: '#718096' }}>{t('billing.payOrderLine', { no: cur.order_no, amount: fenToYuan(cur.amount_cents) })}{cur.amount_cents !== cur.original_amount_cents && cur.original_amount_cents ? t('billing.payOriginal', { amount: fenToYuan(cur.original_amount_cents) }) : ''}</p>
          {/* E1 修复(2026-09-14)：渲染后端已下发的 qr_content 收款码——旧版只有一行提示文案，
              static_qr 模式下用户拿不到码只能"盲付"。图片 URL/data URI 直接 <img>，其它当链接给 */}
          <div style={{ background: '#f6f8ff', border: '1px dashed #b794f4', borderRadius: 10, padding: 18, textAlign: 'center', margin: '14px 0', fontSize: 13, wordBreak: 'break-all' }}>
            {(() => {
              const qr = (cur.qr_content || '').trim()
              const isImg = qr.startsWith('data:image') || /^https?:\/\/.+/i.test(qr) && /\.(png|jpe?g|gif|webp|svg)(\?|$)/i.test(qr)
              if (!qr) return <>{t('billing.payQrHint')}</>
              if (isImg) return <img src={qr} alt={t('billing.qrAlt')} style={{ maxWidth: 220, maxHeight: 220, margin: '0 auto', display: 'block', borderRadius: 8 }} />
              return <><a href={qr.startsWith('http') ? qr : undefined} target="_blank" rel="noreferrer" style={{ color: 'var(--pri)', fontWeight: 600 }}>{t('billing.payOpenQr')}</a>
                <div style={{ marginTop: 8, color: '#718096' }}>{t('billing.payAfterQr')}</div></>
            })()}
          </div>
          <p style={{ fontSize: 13, minHeight: 16 }}>{msg}</p>
          <div style={{ display: 'flex', gap: 10 }}>
            <Button theme="default" variant="outline" style={{ flex: 1 }} aria-label={t('billing.payCancelAria')} onClick={() => { setModal(false); loadOrders(); loadQuota() }}>{t('common.cancel')}</Button>
            {payMode === 'sdk'
              ? <span style={{ flex: 1, textAlign: 'center', alignSelf: 'center', fontSize: 12, color: '#718096' }}>{t('billing.payAutoArrive')}</span>
              : <Button theme="warning" variant="outline" style={{ flex: 1 }} onClick={manualConfirm}>{t('billing.paidBtn')}</Button>}
            {payMode === 'mock' && <Button theme="success" style={{ flex: 1 }} onClick={mockPay}>{t('billing.mockPayBtn')}</Button>}
          </div>
        </>}
      </Dialog>

      {/* 页脚：法律链接与品牌名 */}
      <footer style={{ textAlign: 'center', padding: 16, color: '#94a3b8', fontSize: 12, borderTop: '1px solid #e5e7eb', marginTop: 28, lineHeight: 2 }}>
        <a href="/user-agreement" aria-label={t('billing.viewAgreement')} style={{ color: 'var(--pri)' }}>{t('login.userAgreement')}</a> · <a href="/privacy-policy" aria-label={t('billing.viewPrivacy')} style={{ color: 'var(--pri)' }}>{t('login.privacyPolicy')}</a> · {t('billing.footer', { brand: brand.brandName })}
      </footer>
      <ConfirmDialog open={confirmOpen} title={confirmTitle} message={confirmMsg} onConfirm={() => { setConfirmOpen(false); confirmFn() }} onCancel={() => setConfirmOpen(false)} />

      {/* E9：发票申请表单弹窗（抬头/税号/邮箱 → 后端落单，超管人工开具回录发票号） */}
      <Dialog header={t('billing.invDialogTitle')} visible={invOpen} onClose={() => setInvOpen(false)} onConfirm={submitInvoice} confirmBtn={t('billing.invSubmit')} cancelBtn={t('common.cancel')}>
        <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
          <Input style={formInput} placeholder={t('billing.invTitlePh')} value={invForm.title} onChange={(v) => setInvForm({ ...invForm, title: v })} />
          <Input style={formInput} placeholder={t('billing.invTaxPh')} value={invForm.tax_no} onChange={(v) => setInvForm({ ...invForm, tax_no: v })} />
          <Input style={formInput} placeholder={t('billing.invEmailPh')} value={invForm.email} onChange={(v) => setInvForm({ ...invForm, email: v })} />
          <div style={{ fontSize: 12, color: '#718096' }}>{t('billing.invNote')}</div>
        </div>
      </Dialog>
    </div>
  )
}

// E9 表单输入框样式
const formInput: React.CSSProperties = { padding: '8px 12px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 13 }

// 表头单元格样式
const th: React.CSSProperties = { padding: '10px 12px', textAlign: 'left', fontSize: 13 }
// 表格数据单元格样式
const td: React.CSSProperties = { padding: '10px 12px', textAlign: 'left', fontSize: 13, borderBottom: '1px solid #edf2f7' }
// 订单状态标签样式
const st: React.CSSProperties = { padding: '2px 9px', borderRadius: 10, fontSize: 12 }
