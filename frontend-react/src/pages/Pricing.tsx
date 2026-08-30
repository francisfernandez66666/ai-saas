import { useState, useEffect } from 'react'
import { useBrand } from '../lib/branding'

// 租户套餐（定价页展示）
type Plan = { name: string; price_monthly_cents: number; highlights?: string; max_users: number; max_customers: number; max_departments: number }
// AI 商业包（定价页展示）
type Pkg = { name: string; p_type: string; price_cents: number; ai_calls: number; duration_days?: number; description?: string }

// 定价页：展示套餐（plan）与 AI 商业包（package），数据来自 /api/v1/plans（含 packages 字段）
export default function Pricing() {
  const brand = useBrand()
  const [plans, setPlans] = useState<Plan[]>([])
  const [pkgs, setPkgs] = useState<Pkg[]>([])
  useEffect(() => {
    fetch('/api/v1/plans').then((r) => r.json()).then((j) => {
      setPlans(j.data || [])
      setPkgs(j.packages || [])
    }).catch(() => {})
  }, [])
  return (
    <div style={{ fontFamily: '-apple-system, PingFang SC, sans-serif', background: '#f5f7fa', minHeight: '100vh', padding: '40px 20px', color: '#2d3748' }}>
      <h1 style={{ textAlign: 'center', marginBottom: 8 }}>选择适合您的套餐</h1>
      <p style={{ textAlign: 'center', color: '#718096', marginBottom: 36 }}>全部套餐支持免费试用 · 随时升降级</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(240px,1fr))', gap: 20, maxWidth: 1080, margin: '0 auto' }}>
        {plans.map((p, i) => {
          let hl: string[] = []
          try { hl = JSON.parse(p.highlights || '[]') } catch {}
          const m = (p.price_monthly_cents / 100).toFixed(0)
          const hasPrice = p.price_monthly_cents > 0
          return (
            <div key={i} style={{ background: '#fff', borderRadius: 12, padding: 26, boxShadow: '0 4px 18px rgba(0,0,0,.07)', position: 'relative' }} className={i === 1 ? 'ring-2 ring-indigo-500' : ''}>
              {i === 1 && <span style={{ position: 'absolute', top: -10, right: 12, background: 'var(--pri)', color: '#fff', fontSize: 11, padding: '3px 8px', borderRadius: 10 }}>推荐</span>}
              <b>{p.name}</b>
              <div style={{ fontSize: 30, fontWeight: 800, margin: '10px 0' }}>{hasPrice ? '¥' + m : <small style={{ fontSize: 13, color: '#718096', fontWeight: 400 }}>联系商务</small>}{hasPrice && <small style={{ fontSize: 13, color: '#718096' }}>/月</small>}</div>
              <ul style={{ listStyle: 'none', margin: '14px 0' }}>
                {hl.map((h, k) => <li key={k} style={{ padding: '5px 0', fontSize: 14, color: '#4a5568' }}>✓ {h}</li>)}
              </ul>
              <div style={{ fontSize: 12, color: '#718096', borderTop: '1px dashed #e2e8f0', marginTop: 12, paddingTop: 10 }}>席位 {p.max_users} · 客户 {p.max_customers} · 部门 {p.max_departments}</div>
              <a href="/register" style={{ display: 'block', textAlign: 'center', marginTop: 16, padding: 11, borderRadius: 8, background: 'var(--pri)', color: '#fff', textDecoration: 'none' }}>{hasPrice ? '免费试用' : '联系我们'}</a>
            </div>
          )
        })}
      </div>
      <h1 style={{ fontSize: 26, textAlign: 'center', marginTop: 44 }}>AI 商业包</h1>
      <p style={{ textAlign: 'center', color: '#718096', marginBottom: 24 }}>注册即送试用包 · 增量包买断不过期</p>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit,minmax(240px,1fr))', gap: 20, maxWidth: 1080, margin: '0 auto' }}>
        {pkgs.map((p, i) => {
          const price = Number(p.price_cents) > 0 ? '¥' + (Number(p.price_cents) / 100).toFixed(0) : '免费'
          return (
            <div key={i} style={{ background: '#fff', borderRadius: 12, padding: 26, boxShadow: '0 4px 18px rgba(0,0,0,.07)', position: 'relative' }} className={i === 2 ? 'ring-2 ring-indigo-500' : ''}>
              <b>{p.name}</b>
              <span style={{ position: 'absolute', top: 14, right: 14, background: '#edf2f7', color: '#4a5568', fontSize: 11, padding: '3px 9px', borderRadius: 10 }}>{p.p_type === 'free' ? '注册赠送' : p.p_type === 'paid' ? '包月' : '买断'}</span>
              <div style={{ fontSize: 30, fontWeight: 800, margin: '10px 0' }}>{price}{p.p_type === 'paid' && <small style={{ fontSize: 13, color: '#718096' }}>/月</small>}</div>
              <ul style={{ listStyle: 'none', margin: 0, padding: 0 }}>
                <li style={{ padding: '5px 0', fontSize: 14, color: '#4a5568' }}>{p.ai_calls} 次 AI 调用</li>
                <li style={{ padding: '5px 0', fontSize: 14, color: '#4a5568' }}>{p.duration_days ? p.duration_days + ' 天有效期' : '额度永不过期'}</li>
                {p.description && <li style={{ padding: '5px 0', fontSize: 14, color: '#4a5568' }}>{p.description}</li>}
              </ul>
              <a href="/register" style={{ display: 'block', textAlign: 'center', marginTop: 16, padding: 11, borderRadius: 8, background: 'var(--pri)', color: '#fff', textDecoration: 'none' }}>开通后订阅</a>
            </div>
          )
        })}
      </div>
      <div style={{ textAlign: 'center', marginTop: 28 }}><a href="/register" style={{ color: 'var(--pri)' }}>← 返回注册</a>　<a href="/" style={{ color: 'var(--pri)' }}>首页</a></div>
      <footer style={{ textAlign: 'center', padding: 16, color: '#94a3b8', fontSize: 12, borderTop: '1px solid #e5e7eb', marginTop: 28, lineHeight: 2 }}>
        <a href="/user-agreement" style={{ color: 'var(--pri)' }}>用户协议</a> · <a href="/privacy-policy" style={{ color: 'var(--pri)' }}>隐私政策</a> · {brand.brandName} AI-SCRM 平台
      </footer>
    </div>
  )
}
