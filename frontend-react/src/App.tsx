// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// D1：业务大页路由级 lazy，首屏只加载当前页；TDesign 由 vite 自动分块拆包（P2-16 纠偏：曾注释称 manualChunks 手工拆包而 vite.config 实无该配置，口径以 vite.config.ts 为准）
import { lazy, Suspense, useEffect, useState } from 'react'
import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { logoutAndRedirect, verifySession, type SessionCheck } from './lib/api'
import { RequireRole } from './components/RequireRole'
import { ADMIN_ROLES } from './lib/roles'
/** 导航落地页懒加载入口。 */
const Index = lazy(() => import('./pages/Index'))
/** 登录页懒加载入口。 */
const Login = lazy(() => import('./pages/Login'))
/** 注册页懒加载入口。 */
const Register = lazy(() => import('./pages/Register'))
/** 用户协议页懒加载入口。 */
const UserAgreement = lazy(() => import('./pages/UserAgreement'))
/** 隐私政策页懒加载入口。 */
const PrivacyPolicy = lazy(() => import('./pages/PrivacyPolicy'))
/** 后台管理页懒加载入口。 */
const Admin = lazy(() => import('./pages/Admin'))
/** 平台超管页懒加载入口。 */
const SuperAdmin = lazy(() => import('./pages/SuperAdmin'))
/** 顾问工作台懒加载入口。 */
const Advisor = lazy(() => import('./pages/Advisor'))
/** 客户端对话页懒加载入口。 */
const Client = lazy(() => import('./pages/Client'))
/** 收银台页面懒加载入口。 */
const Billing = lazy(() => import('./pages/Billing'))
/** 组织架构页面懒加载入口。 */
const Org = lazy(() => import('./pages/Org'))
/** 定价页懒加载入口。 */
const Pricing = lazy(() => import('./pages/Pricing'))
/** 开放 API 文档站懒加载入口（E10，公开页）。 */
const ApiDocs = lazy(() => import('./pages/ApiDocs'))
/** 移动端工作台首页懒加载入口。 */
const AppHome = lazy(() => import('./pages/AppHome'))
/** 移动端邀请页懒加载入口。 */
const AppReferral = lazy(() => import('./pages/AppReferral'))
/** 移动端设置页懒加载入口。 */
const AppSettings = lazy(() => import('./pages/AppSettings'))
/** 移动端共享布局懒加载入口。 */
const AppLayout = lazy(() => import('./pages/AppLayout'))

// ============================================================
// P1-2 前端路由守卫（2026-08-30）
//
// 目标：受保护路由无前端守卫，未登录直接访问会闪屏
// 方案：<ProtectedRoute> 检查 token，无则跳 /login，保留原路径供登录后跳回
// ============================================================

// ProtectedRoute 受保护路由守卫组件
// P1-50(2026-09-09)：不再只查 token 存在——token 可被篡改/过期，角色可能被 localStorage
// 造假。现在挂 /auth/me 会话校验（60s 缓存）：无效→登录页；需改密→改密页；有效→放行。
//
// FIX-6(2026-09-27)：守卫改成**按会话校验返回的状态分支**，不再靠"info===null 且 token 还在"
// 反推原因。旧推断把"网络压根没通"读成"服务端要求改密"——用户在地铁上刷新后台会被甩进
// 强制改密表单（/login?mcp=1），既解释不了也回不去原来的页面。
// 现在：network_error 留在原地给一条"网络异常，点击重试"；forbidden（非改密的 403，
// 如租户停用/配额）也留在原地提示并给退出入口，不再顺手销毁一次没有被判死的会话。
// 为何 export（FIX-6 验收，2026-09-27）：断网/403 这两条分支的验收要看"用户留在原地还是被甩去
// /login?mcp=1"，这是守卫本身的分支逻辑，不该为了测它把整棵路由树搬进用例；导出后
// `src/pages/__tests__/ProtectedRoute.test.tsx` 直接挂进 MemoryRouter 断言落点。
export function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const location = useLocation()
  // 校验结论**连同它属于哪条路径**一起存：`checking` 由"结论没落地 / 结论是别的路径留下的"派生，
  // 不再在 effect 里同步 setChecking(true)（react-hooks/set-state-in-effect 会把这条刷成 warning，
  // 而这里它本来就是多余的——一次校验只产出一个结论，结论没来就是"校验中"，无需第二个布尔位）。
  // 带路径的另一层意思：切页时旧结论立刻失效，不会出现"上一条页的 ok 让新一页闪现内容"。
  const [verdict, setVerdict] = useState<{ path: string; res: SessionCheck } | null>(null)
  const [attempt, setAttempt] = useState(0) // 点"重试"自增，驱动下面 effect 重跑

  useEffect(() => {
    let alive = true
    ;(async () => {
      const res = await verifySession()
      if (!alive) return
      setVerdict({ path: location.pathname, res })
    })()
    return () => {
      alive = false
    }
    // location.pathname：切页重新校验；attempt：手动重试
  }, [location.pathname, attempt])

  if (!verdict || verdict.path !== location.pathname) return null // 校验中：空白占位（避免闪现受保护内容）

  const st = verdict.res.state
  if (st === 'ok') return <>{children}</>
  if (st === 'network_error' || st === 'forbidden') {
    // 不跳走：这两种情况下用户的会话没有被服务端否定（或否定的原因不是"该重新登录"），
    // 跳登录页只会让他丢掉当前页面，而且登录后回不来同一条深链接的概率很高。
    return (
      <div style={{ padding: 24, maxWidth: 420, margin: '80px auto', textAlign: 'center' }}>
        <div style={{ fontSize: 15, marginBottom: 12 }}>
          {st === 'network_error'
            ? '网络异常，暂时连不上服务器。你的登录状态没有丢，恢复网络后点重试即可。'
            : '当前账号暂时无法访问该页面（可能是租户已停用、配额用尽或权限变更）。'}
        </div>
        {st === 'network_error' && (
          <button
            type="button"
            onClick={() => setAttempt((a) => a + 1)}
            style={{ marginRight: 8, padding: '6px 14px' }}
          >
            重试
          </button>
        )}
        <button type="button" onClick={() => logoutAndRedirect()} style={{ padding: '6px 14px' }}>
          退出登录
        </button>
      </div>
    )
  }
  if (st === 'must_change_password') {
    // 首登需改密：跳改密表单（change-password 接口在白名单内可用，且 token 已被刻意保留）
    return <Navigate to="/login?mcp=1" replace />
  }
  // anonymous / revoked：跳登录页，保留原路径供登录后跳回
  return <Navigate to={`/login?redirect=${encodeURIComponent(location.pathname)}`} replace />
}

// 全站根路由组件：声明路由表，区分公开页（登录/注册/协议）与业务页（/admin、/super、/advisor、/client、/billing 等）
// /app 为嵌套布局路由，其内部子页由 AppLayout 统一做登录态守卫与顶栏
// 注意：BrowserRouter 已由 main.tsx 统一提供，此处不再嵌套（避免双 Router 冲突）
export default function App() {
  return (
      <Suspense fallback={<RouteLoading />}>
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
      <Route path="/admin" element={<ProtectedRoute><RequireRole allow={ADMIN_ROLES}><Admin /></RequireRole></ProtectedRoute>} />
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
      {/* 开放 API 文档站（E10）：渲染后端 OpenAPI 规格，免登录对外可见 */}
      <Route path="/docs/api" element={<ApiDocs />} />
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
      </Suspense>
  )
}

/** 路由懒加载占位组件，避免大页面切换时白屏。 */
function RouteLoading() {
  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f5f7fa' }}>
      <div style={{ color: '#6b7280', fontSize: 14 }}>加载中…</div>
    </div>
  )
}

