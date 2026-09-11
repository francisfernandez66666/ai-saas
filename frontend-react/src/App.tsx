// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// 注意：BrowserRouter 已由 main.tsx 统一提供，此处不再嵌套（避免双 Router 冲突）
import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import Index from './pages/Index'
import Login from './pages/Login'
import Register from './pages/Register'
import UserAgreement from './pages/UserAgreement'
import PrivacyPolicy from './pages/PrivacyPolicy'
import Admin from './pages/Admin'
import SuperAdmin from './pages/SuperAdmin'
import Advisor from './pages/Advisor'
import Client from './pages/Client'
import Billing from './pages/Billing'
import Org from './pages/Org'
import Pricing from './pages/Pricing'
import AppHome from './pages/AppHome'
import AppReferral from './pages/AppReferral'
import AppSettings from './pages/AppSettings'
import AppLayout from './pages/AppLayout'
import { getToken, verifySession } from './lib/api'
import { useState, useEffect } from 'react'

// ============================================================
// P1-2 前端路由守卫（2026-08-30）
//
// 目标：受保护路由无前端守卫，未登录直接访问会闪屏
// 方案：<ProtectedRoute> 检查 token，无则跳 /login，保留原路径供登录后跳回
// ============================================================

// ProtectedRoute 受保护路由守卫组件
// P1-50(2026-09-09)：不再只查 token 存在——token 可被篡改/过期，角色可能被 localStorage
// 造假。现在挂 /auth/me 会话校验（60s 缓存）：无效→登录页；需改密→改密页；有效→放行。
function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const location = useLocation()
  const [checking, setChecking] = useState(true)
  const [mcp, setMcp] = useState(false)

  useEffect(() => {
    let alive = true
    ;(async () => {
      if (!getToken()) {
        if (alive) setChecking(false)
        return
      }
      const info = await verifySession()
      if (!alive) return
      if (info === null) {
        // token 校验失败：token 被 verifySession 清掉(401)→走下方 !token 跳登录；
        // 仍持有 token 则说明服务端要求改密（must_change_password）→ 跳改密页
        if (getToken()) { if (alive) setMcp(true) }
      }
      if (alive) setChecking(false)
    })()
    return () => { alive = false }
  }, [location.pathname])

  if (checking) return null // 校验中：空白占位（避免闪现受保护内容）

  const token = getToken()

  if (!token) {
    // 无 token（含 401 被 verifySession 清除）：跳登录页，保留原路径供登录后跳回
    return <Navigate to={`/login?redirect=${encodeURIComponent(location.pathname)}`} replace />
  }
  if (mcp) {
    // 首登需改密：跳改密表单（change-password 接口在白名单内可用）
    return <Navigate to="/login?mcp=1" replace />
  }

  return <>{children}</>
}

// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// 注意：BrowserRouter 已由 main.tsx 统一提供，此处不再嵌套（避免双 Router 冲突）
export default function App() {
  // /app 为嵌套布局路由，内部 login/register/chat/advisor/billing/referral/settings
  // 由 AppLayout 统一做登录态守卫与顶栏
  return (
    <Routes>
      {/* 首页：产品官网落地页，展示平台能力与 CTA */}
      <Route path="/" element={<Index />} />
      {/* 登录页：支持租户码登录、首登强改密、验证码找回密码 */}
      <Route path="/login" element={<Login />} />
      {/* 注册页：租户自助开通试用，填写企业信息并创建管理员账号 */}
      <Route path="/register" element={<Register />} />
      {/* 用户协议静态页：展示平台服务条款与责任声明 */}
      <Route path="/user-agreement" element={<UserAgreement />} />
      {/* 隐私政策静态页：展示《个人信息保护法》等合规条款 */}
      <Route path="/privacy-policy" element={<PrivacyPolicy />} />
      {/* 租户管理员后台：管理成员、部门、客户等租户级资源（需登录） */}
      <Route path="/admin" element={<ProtectedRoute><Admin /></ProtectedRoute>} />
      {/* 平台超管后台：管理租户、套餐、全局配置等平台级资源（需登录） */}
      <Route path="/super" element={<ProtectedRoute><SuperAdmin /></ProtectedRoute>} />
      {/* 顾问工作台：管理客户会话、跟进记录、AI 接待策略（需登录） */}
      <Route path="/advisor" element={<ProtectedRoute><Advisor /></ProtectedRoute>} />
      {/* C 端客户对话页：匿名访客与 AI 对话，支持人机验证（免登录） */}
      <Route path="/client" element={<Client />} />
      {/* 收银台：展示套餐用量、商业包列表与订单，支持支付（需登录） */}
      <Route path="/billing" element={<ProtectedRoute><Billing /></ProtectedRoute>} />
      {/* 组织架构管理：部门树（增删改/移动）与成员列表（启停/新增）（需登录） */}
      <Route path="/org" element={<ProtectedRoute><Org /></ProtectedRoute>} />
      {/* 定价页：展示套餐（plan）与 AI 商业包（package） */}
      <Route path="/pricing" element={<Pricing />} />
      {/* /app 嵌套布局路由：移动端工作台，AppLayout 统一做登录态守卫与顶栏 */}
      <Route path="/app" element={<AppLayout />}>
        {/* /app 首页：卡片入口导航至对话/顾问台/收银台/邀请/设置 */}
        <Route index element={<AppHome />} />
        {/* /app 登录页：复用 PC 端登录组件 */}
        <Route path="login" element={<Login />} />
        {/* /app 注册页：复用 PC 端注册组件 */}
        <Route path="register" element={<Register />} />
        {/* /app 客户对话页：复用 PC 端 Client 组件 */}
        <Route path="chat" element={<Client />} />
        {/* /app 顾问工作台：复用 PC 端 Advisor 组件 */}
        <Route path="advisor" element={<Advisor />} />
        {/* /app 收银台：复用 PC 端 Billing 组件 */}
        <Route path="billing" element={<Billing />} />
        {/* /app 邀请推广页：展示邀请码/链接/二维码与邀请记录 */}
        <Route path="referral" element={<AppReferral />} />
        {/* /app 账号设置：改密、换绑邮箱、知识库、账号注销 */}
        <Route path="settings" element={<AppSettings />} />
      </Route>
      {/* 兜底路由：未匹配路径跳转首页 */}
      <Route path="*" element={<Index />} />
    </Routes>
  )
}

