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
      {/* 租户管理员后台：管理成员、部门、客户等租户级资源 */}
      <Route path="/admin" element={<Admin />} />
      {/* 平台超管后台：管理租户、套餐、全局配置等平台级资源 */}
      <Route path="/super" element={<SuperAdmin />} />
      {/* 顾问工作台：管理客户会话、跟进记录、AI 接待策略 */}
      <Route path="/advisor" element={<Advisor />} />
      {/* C 端客户对话页：匿名访客与 AI 对话，支持人机验证 */}
      <Route path="/client" element={<Client />} />
      {/* 收银台：展示套餐用量、商业包列表与订单，支持支付 */}
      <Route path="/billing" element={<Billing />} />
      {/* 组织架构管理：部门树（增删改/移动）与成员列表（启停/新增） */}
      <Route path="/org" element={<Org />} />
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

