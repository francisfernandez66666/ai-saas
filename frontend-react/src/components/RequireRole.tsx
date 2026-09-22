// 路由级角色守卫组件（P2-8a）
// 与 ProtectedRoute 配套：ProtectedRoute 先校验登录态，RequireRole 再校验角色集合。
// 无权限时跳 /login 并携带 redirect 参数（登录后回跳当前路径），与 ProtectedRoute 的跳转风格一致。
import type { ReactNode } from 'react'
import { Navigate, useLocation } from 'react-router-dom'

/** 角色守卫：仅当 localStorage 中的角色落在 allow 集合内才渲染 children，否则跳登录页。
 * @param allow - 允许访问的角色字符串集合（建议传 ADMIN_ROLES / [ROLES.superAdmin] 等）
 * @param children - 通过守卫后才渲染的内容
 */
export function RequireRole({ allow, children }: { allow: string[]; children: ReactNode }) {
  const loc = useLocation()
  // 以 localStorage role 为准（verifySession 已纠偏为服务端权威值；后端 403 才是真实边界）
  const role = localStorage.getItem('role') || ''
  if (!allow.includes(role)) {
    return <Navigate to={`/login?redirect=${encodeURIComponent(loc.pathname)}`} replace />
  }
  return <>{children}</>
}
