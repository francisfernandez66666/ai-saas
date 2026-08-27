// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// 注意：BrowserRouter 已由 main.tsx 统一提供，此处不再嵌套（避免双 Router 冲突）
import { Routes, Route } from 'react-router-dom'
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

// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// 注意：BrowserRouter 已由 main.tsx 统一提供，此处不再嵌套（避免双 Router 冲突）
export default function App() {
  // /app 为嵌套布局路由，内部 login/register/chat/advisor/billing/referral/settings
  // 由 AppLayout 统一做登录态守卫与顶栏
  return (
    <Routes>
      <Route path="/" element={<Index />} />
      <Route path="/login" element={<Login />} />
      <Route path="/register" element={<Register />} />
      <Route path="/user-agreement" element={<UserAgreement />} />
      <Route path="/privacy-policy" element={<PrivacyPolicy />} />
      <Route path="/admin" element={<Admin />} />
      <Route path="/super" element={<SuperAdmin />} />
      <Route path="/advisor" element={<Advisor />} />
      <Route path="/client" element={<Client />} />
      <Route path="/billing" element={<Billing />} />
      <Route path="/org" element={<Org />} />
      <Route path="/pricing" element={<Pricing />} />
      <Route path="/app" element={<AppLayout />}>
        <Route index element={<AppHome />} />
        <Route path="login" element={<Login />} />
        <Route path="register" element={<Register />} />
        <Route path="chat" element={<Client />} />
        <Route path="advisor" element={<Advisor />} />
        <Route path="billing" element={<Billing />} />
        <Route path="referral" element={<AppReferral />} />
        <Route path="settings" element={<AppSettings />} />
      </Route>
      <Route path="*" element={<Index />} />
    </Routes>
  )
}

