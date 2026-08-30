/**
 * Billing.tsx：订阅收银台页面
 * 展示套餐用量、商业包列表与订单；支持模拟支付/扫码人工确认两种方式
 * 依赖接口：/api/v1/billing/my-package、/api/v1/packages、/api/v1/billing/orders、/api/v1/billing/subscribe、/api/v1/billing/*-pay、/api/v1/billing/manual-confirm
 */
import { useState, useEffect } from 'react'
import { Dialog, Button, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { getToken } from '../lib/api'
import type { TableRowData } from '../types'

// 当前租户套餐用量类型（收银台顶部展示）
type Quota = { tenant_name: string; status: string; used_ai_calls: number; max_ai_calls: number; ai_call_balance: number; expired_at?: string; pay_mode?: string }
// 商业包类型（收银台列表）
type Pkg = { id: number; p_type: string; name: string; price_cents: number; description?: string; ai_calls: number; duration_days?: number }
// 订阅订单类型（我的订单列表）
type Order = { id: number; order_no: string; amount_cents: number; package_name?: string; channel?: string; status: string; manual_confirm?: boolean; created_at: string }

// 收银台接口鉴权头
const AUTH = (): { headers: Record<string, string> } => ({ headers: { Authorization: "Bearer " + getToken() } })
// 支付渠道中文映射
const CH = { mock: '模拟', manual: '静态码人工', wechat: '微信', alipay: '支付宝' }

/**
 * 订阅收银台组件
 * 核心功能：
 * 1. 顶部展示当前套餐用量（AI 调用次数、增量余额、到期日）
 * 2. 中部展示商业包列表（试用包/包月包/增量包），支持订阅
 * 3. 底部展示订单列表，待支付订单可继续操作
 * 4. 支付弹窗：支持模拟支付（测试）和人工确认（静态码）
 */
export default function Billing() {
  const brand = useBrand()
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

  /** 加载当前套餐用量 */
  async function loadQuota() {
    const r = await fetch('/api/v1/billing/my-package', AUTH())
    const j = await r.json()
    if (j.code === 0) { setQuota(j.data); setPayMode(j.data.pay_mode) }
  }
  /** 加载商业包列表（过滤免费包） */
  async function loadPkgs() {
    const r = await fetch('/api/v1/packages')
    const j = await r.json()
    setPkgs((j.data || []).filter((p: Pkg) => p.p_type !== 'free'))
  }
  /** 加载订单列表（最近 50 条） */
  async function loadOrders() {
    try {
      const r = await fetch('/api/v1/billing/orders?limit=50', AUTH())
      const j = await r.json()
      if (j.code === 0) setOrders(j.data || [])
    } catch {}
  }
  /** 并行加载所有数据 */
  function loadAll() { loadQuota(); loadPkgs(); loadOrders() }

  useEffect(() => {
    // 未登录时跳转登录页
    if (!getToken()) { location.href = '/login'; return }
    loadAll()
    // 每 15s 轮询订单与套餐，自动刷新待支付/已到账状态
    const t = setInterval(() => { loadOrders(); loadQuota() }, 15000)
    return () => clearInterval(t)
  }, [])

  /**
   * 订阅商业包：调用 /api/v1/billing/subscribe 接口
   * 试用包直接发放，付费包创建订单后打开支付弹窗
   */
  async function subscribe(id: number) {
    const r = await fetch('/api/v1/billing/subscribe', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ package_id: id }) })
    const j = await r.json()
    if (j.code !== 0) { MessagePlugin.error(j.message || '订阅失败'); return }
    if (j.data.granted) { MessagePlugin.success('试用包已发放'); loadAll(); return }
    openPay(j.data.order, j.data.pay_mode)
  }
  /** 打开支付弹窗 */
  function openPay(order: Order, mode?: string) {
    setCur(order); setPayMode(mode || payMode); setMsg(''); setModal(true)
  }
  /** 模拟支付（测试环境）：调用 /api/v1/billing/orders/mock-pay 接口 */
  async function mockPay() {
    if (!cur) return
    const r = await fetch('/api/v1/billing/orders/mock-pay', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ order_id: cur.id }) })
    const j = await r.json()
    setMsg(j.message || '')
    if (j.code === 0) setTimeout(() => { setModal(false); loadOrders(); loadQuota() }, 900)
  }
  /** 人工确认已付款：调用 /api/v1/billing/manual-confirm 接口 */
  async function manualConfirm() {
    if (!cur) return
    const r = await fetch('/api/v1/billing/manual-confirm', { method: 'POST', headers: { ...AUTH().headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ order_id: cur.id }) })
    const j = await r.json()
    setMsg(j.message || '')
  }

  // 未登录时不渲染
  if (!getToken()) return null

  return (
    <div style={{ background: '#f5f7fa', minHeight: '100vh', padding: 24, color: '#2d3748' }}>
      {/* 顶栏：标题 + 支付方式说明 */}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 18 }}>
        <div><h2 style={{ marginBottom: 4 }}>订阅与收银台</h2><div style={{ color: '#718096', fontSize: 13 }}>AI 调用按"次"计费 · 增量包买断不过期</div></div>
        <div><span style={{ fontSize: 12, color: '#718096' }}>{payMode === 'static_qr' ? '收款方式：扫码转账+平台人工确认' : payMode === 'sdk' ? '收款方式：在线支付' : '测试环境：支持模拟支付'}</span>　<a href="/" style={{ color: 'var(--pri)' }}>首页</a></div>
      </div>

      {/* 当前套餐用量展示区 */}
      <div style={{ background: '#fff', borderRadius: 12, padding: '16px 22px', boxShadow: '0 3px 14px rgba(0,0,0,.06)', display: 'flex', gap: 34, flexWrap: 'wrap', marginBottom: 26 }}>
        {!quota && <span style={{ color: '#718096' }}>加载中...</span>}
        {quota && (<>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.tenant_name}</b><span style={{ fontSize: 12, color: '#718096' }}>{quota.status === 'trial' ? '试用中' : quota.status}</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.used_ai_calls} / {quota.max_ai_calls || '∞'}</b><span style={{ fontSize: 12, color: '#718096' }}>本月AI调用(次)</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.ai_call_balance}</b><span style={{ fontSize: 12, color: '#718096' }}>增量余额(买断)</span></div>
          <div style={{ textAlign: 'center' }}><b style={{ fontSize: 22, display: 'block', color: '#4c51bf' }}>{quota.expired_at ? new Date(quota.expired_at).toLocaleDateString() : '-'}</b><span style={{ fontSize: 12, color: '#718096' }}>套餐到期日</span></div>
        </>)}
      </div>

      {/* 商业包列表 */}
      <h2 style={{ fontSize: 17 }}>商业包</h2>
      <p style={{ color: '#718096', fontSize: 13, marginBottom: 20 }}>试用包注册自动发放；包月包到期顺延；加油包入余额池</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(220px,1fr))', gap: 16, marginBottom: 28 }}>
        {pkgs.map((p) => (
          <div key={p.id} style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 3px 14px rgba(0,0,0,.06)', position: 'relative', display: 'flex', flexDirection: 'column' }}>
            {p.p_type === 'increment' && <span style={{ position: 'absolute', top: -8, right: 10, background: 'var(--pri)', color: '#fff', fontSize: 11, padding: '2px 9px', borderRadius: 10 }}>热卖</span>}
            <b>{p.name}</b>
            <div style={{ fontSize: 24, fontWeight: 800, margin: '8px 0' }}>{p.price_cents > 0 ? '¥' + (p.price_cents / 100).toFixed(0) : '免费'}<small style={{ fontSize: 12, color: '#718096', fontWeight: 400 }}>{p.p_type === 'paid' ? '/月' : ''}</small></div>
            <p style={{ fontSize: 13, color: '#718096', margin: '8px 0 14px', flex: 1 }}>{p.description || ''}<br />含 {p.ai_calls} 次AI调用{p.duration_days ? ` · ${p.duration_days}天有效期` : ' · 永不过期'}</p>
            <button onClick={() => subscribe(p.id)} style={{ background: 'linear-gradient(135deg,var(--pri),#764ba2)', color: '#fff', width: '100%', padding: '9px 14px', border: 'none', borderRadius: 8, cursor: 'pointer', fontWeight: 600 }}>立即订阅 ({p.p_type === 'free' ? '注册赠送' : p.p_type === 'paid' ? '包月' : '买断'})</button>
          </div>
        ))}
      </div>

      {/* 订单列表 */}
      <h2 style={{ fontSize: 17 }}>我的订单</h2>
      <p style={{ color: '#718096', fontSize: 13, marginBottom: 20 }}>待支付订单可继续操作；已到账订单权益即时发放</p>
      <table style={{ width: '100%', background: '#fff', borderRadius: 10, borderCollapse: 'collapse', overflow: 'hidden', boxShadow: '0 3px 14px rgba(0,0,0,.06)' }}>
        <thead><tr style={{ background: '#fafafa', color: '#4a5568' }}><th style={th}>订单号</th><th style={th}>金额</th><th style={th}>渠道</th><th style={th}>状态</th><th style={th}>创建时间</th><th style={th}>操作</th></tr></thead>
        <tbody>
          {orders.length === 0 && <tr><td colSpan={6} style={{ ...td, textAlign: 'center', color: '#718096' }}>暂无订单，订阅商业包后生成</td></tr>}
          {orders.map((o) => (
            <tr key={o.id}>
              <td style={td}>{o.order_no}</td>
              <td style={td}>¥{(o.amount_cents / 100).toFixed(2)}<br /><span style={{ fontSize: 11, color: '#718096' }}>{o.package_name || ''}</span></td>
              <td style={td}>{CH[o.channel as keyof typeof CH] || o.channel || '-'}</td>
              <td style={td}><span style={{ ...st, background: o.status === 'pending' ? '#feebc8' : '#c6f6d5', color: o.status === 'pending' ? '#975a16' : '#276749' }}>{o.status === 'pending' ? (o.manual_confirm ? '待平台确认' : '待支付') : o.status}</span></td>
              <td style={td}>{new Date(o.created_at).toLocaleString()}</td>
              <td style={td}>{o.status === 'pending' ? <button style={{ background: 'var(--pri)', color: '#fff', border: 'none', padding: '4px 10px', borderRadius: 6, cursor: 'pointer' }} onClick={() => openPay(o, payMode)}>继续支付</button> : '已完成'}</td>
            </tr>
          ))}
        </tbody>
      </table>

      {/* 支付弹窗：展示订单信息与支付操作 */}
      <Dialog header="订单支付" visible={modal} onClose={() => { setModal(false); loadOrders(); loadQuota() }} footer={false}>
        {cur && <>
          <p style={{ fontSize: 13, color: '#718096' }}>订单 {cur.order_no} · 应付 ¥{(cur.amount_cents / 100).toFixed(2)}</p>
          <div style={{ background: '#f6f8ff', border: '1px dashed #b794f4', borderRadius: 10, padding: 18, textAlign: 'center', margin: '14px 0', fontSize: 13, wordBreak: 'break-all' }}>{/* qr_content rendered from order if available */}请于平台收款码完成支付后点击「我已付费」</div>
          <p style={{ fontSize: 13, minHeight: 16 }}>{msg}</p>
          <div style={{ display: 'flex', gap: 10 }}>
            <Button theme="default" variant="outline" style={{ flex: 1 }} onClick={() => { setModal(false); loadOrders(); loadQuota() }}>取消</Button>
            <Button theme="warning" variant="outline" style={{ flex: 1 }} onClick={manualConfirm}>我已付费</Button>
            {payMode === 'mock' && <Button theme="success" style={{ flex: 1 }} onClick={mockPay}>模拟支付(测试)</Button>}
          </div>
        </>}
      </Dialog>

      {/* 页脚：法律链接与品牌名 */}
      <footer style={{ textAlign: 'center', padding: 16, color: '#94a3b8', fontSize: 12, borderTop: '1px solid #e5e7eb', marginTop: 28, lineHeight: 2 }}>
        <a href="/user-agreement" style={{ color: 'var(--pri)' }}>用户协议</a> · <a href="/privacy-policy" style={{ color: 'var(--pri)' }}>隐私政策</a> · {brand.brandName} AI-SCRM 平台
      </footer>
    </div>
  )
}

// 表头单元格样式
const th: React.CSSProperties = { padding: '10px 12px', textAlign: 'left', fontSize: 13 }
// 表格数据单元格样式
const td: React.CSSProperties = { padding: '10px 12px', textAlign: 'left', fontSize: 13, borderBottom: '1px solid #edf2f7' }
// 订单状态标签样式
const st: React.CSSProperties = { padding: '2px 9px', borderRadius: 10, fontSize: 12 }
