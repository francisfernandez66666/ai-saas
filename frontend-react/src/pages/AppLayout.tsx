/**
 * AppLayout.tsx：移动端布局壳
 * 提供顶栏导航 + Outlet 子路由渲染
 * 对受保护子路由做登录态守卫（无 token 跳登录并带 redirect 回跳）
 */
import { ReactNode } from 'react'
import { Link, Outlet, Navigate, useLocation, useNavigate } from 'react-router-dom'
import { clearToken, getToken } from '../lib/api'

// 需要登录态的 /app 子路由（原 Vue main.js 的 meta.auth 守卫）
const PROTECTED = ['/app/advisor', '/app/billing', '/app/referral', '/app/settings']

/**
 * /app 布局壳组件
 * 1. 登录态守卫：受保护路由无 token 时跳转 /app/login 并携带 redirect 参数
 * 2. 顶栏导航：展示 Logo、对话/顾问台/收银台/邀请/设置链接
 * 3. 退出登录：清除 token 并跳转登录页
 */
export default function AppLayout() {
  const token = getToken()
  const loc = useLocation()
  const nav = useNavigate()

  // 路由守卫：受保护页无 token → 跳登录（带 redirect 回跳）
  if (PROTECTED.includes(loc.pathname) && !token) {
    return <Navigate to={'/app/login?redirect=' + encodeURIComponent(loc.pathname)} replace />
  }

  /**
   * 退出登录：清除本地 token → 跳转登录页 → 刷新页面
   */
  const logout = () => {
    clearToken()
    nav('/app/login')
    if (typeof window !== 'undefined') window.location.reload()
  }

  /**
   * 导航链接组件：统一样式的路由链接
   * @param to - 目标路径
   * @param label - 显示文本
   */
  const link = (to: string, label: string) => (
    <Link to={to} style={{ fontSize: 14, color: '#4f46e5', textDecoration: 'none' }}>{label}</Link>
  )

  return (
    <div style={{ minHeight: '100vh', background: '#f5f6fa' }}>
      {/* 顶栏：Logo + 导航链接 + 退出按钮 */}
      <div style={{ position: 'sticky', top: 0, zIndex: 9, background: 'rgba(255,255,255,.85)', backdropFilter: 'blur(8px)', display: 'flex', alignItems: 'center', gap: 14, padding: '12px 16px', borderBottom: '1px solid #e8eaf0', flexWrap: 'wrap' }}>
        <b onClick={() => nav('/app')} style={{ cursor: 'pointer', color: '#4f46e5', fontSize: 16 }}>AI-SCRM</b>
        {link('/app/chat', '对话')}
        {/* 登录后才显示的导航链接 */}
        {token && link('/app/advisor', '顾问台')}
        {token && link('/app/billing', '收银台')}
        {token && link('/app/referral', '邀请')}
        {token && link('/app/settings', '设置')}
        <span style={{ flex: 1 }} />
        {/* 未登录显示登录链接，已登录显示退出按钮 */}
        {!token && link('/app/login', '登录')}
        {token && <a href="#" onClick={(e) => { e.preventDefault(); logout() }} style={{ fontSize: 14, color: '#ea580c', textDecoration: 'none' }}>退出</a>}
      </div>
      {/* 子路由渲染区域 */}
      <div style={{ paddingBottom: 24 }}><Outlet /></div>
    </div>
  )
}
