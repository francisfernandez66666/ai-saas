import { ReactNode } from 'react'
import { Link, Outlet, Navigate, useLocation, useNavigate } from 'react-router-dom'
import { clearToken, getToken } from '../lib/api'

// 需要登录态的 /app 子路由（原 Vue main.js 的 meta.auth 守卫）
const PROTECTED = ['/app/advisor', '/app/billing', '/app/referral', '/app/settings']

// /app 布局壳：顶栏 + Outlet，对受保护子路由做登录态守卫（无 token 跳登录并带 redirect 回跳）
export default function AppLayout() {
  const token = getToken()
  const loc = useLocation()
  const nav = useNavigate()

  // 路由守卫：受保护页无 token → 跳登录（带 redirect 回跳）
  if (PROTECTED.includes(loc.pathname) && !token) {
    return <Navigate to={'/app/login?redirect=' + encodeURIComponent(loc.pathname)} replace />
  }

  const logout = () => {
    clearToken()
    nav('/app/login')
    if (typeof window !== 'undefined') window.location.reload()
  }

  const link = (to: string, label: string) => (
    <Link to={to} style={{ fontSize: 14, color: '#4f46e5', textDecoration: 'none' }}>{label}</Link>
  )

  return (
    <div style={{ minHeight: '100vh', background: '#f5f6fa' }}>
      <div style={{ position: 'sticky', top: 0, zIndex: 9, background: 'rgba(255,255,255,.85)', backdropFilter: 'blur(8px)', display: 'flex', alignItems: 'center', gap: 14, padding: '12px 16px', borderBottom: '1px solid #e8eaf0', flexWrap: 'wrap' }}>
        <b onClick={() => nav('/app')} style={{ cursor: 'pointer', color: '#4f46e5', fontSize: 16 }}>AI-SCRM</b>
        {link('/app/chat', '对话')}
        {token && link('/app/advisor', '顾问台')}
        {token && link('/app/billing', '收银台')}
        {token && link('/app/referral', '邀请')}
        {token && link('/app/settings', '设置')}
        <span style={{ flex: 1 }} />
        {!token && link('/app/login', '登录')}
        {token && <a href="#" onClick={(e) => { e.preventDefault(); logout() }} style={{ fontSize: 14, color: '#ea580c', textDecoration: 'none' }}>退出</a>}
      </div>
      <div style={{ paddingBottom: 24 }}><Outlet /></div>
    </div>
  )
}
