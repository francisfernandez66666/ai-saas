/**
 * Login.tsx：登录/改密/找回密码页
 * 支持租户码登录、首登强改密、验证码找回密码四种模式
 * 依赖接口：/api/v1/auth/login、/api/v1/auth/change-password、/api/v1/auth/reset-password、/api/v1/auth/verify-reset-code
 * 后端 MustChangePasswordGuard 拦截后由 api.ts 跳转 /login?mcp=1，本页据此直接进入改密表单
 */
import { useState } from 'react'
import { Button, Input, MessagePlugin } from 'tdesign-react'
import { apiJSON, setToken, redirectByRole } from '../lib/api'
import { useBrand } from '../lib/branding'
import type { ApiResp, AuthResult } from '../types'

// 登录模式类型：login=登录、change=强制改密、resetReq=找回密码（发送验证码）、resetConfirm=找回密码（确认重置）
type Mode = 'login' | 'change' | 'resetReq' | 'resetConfirm'

/**
 * 登录/改密/找回密码页组件
 * 根据 URL 参数 mcp=1 自动进入改密模式（由后端 403 拦截触发）
 * 支持四种模式切换：登录 → 强制改密 → 找回密码请求 → 找回密码确认
 */
export default function Login() {
  const brand = useBrand()
  // 后端 MustChangePasswordGuard 拦截后由 api.ts 跳转 /login?mcp=1，直接进入改密表单
  const [mode, setMode] = useState<Mode>(
    new URLSearchParams(location.search).get('mcp') === '1' ? 'change' : 'login',
  )
  // 登录表单数据：tenant_code（企业码）、username（用户名）、password（密码）
  const [login, setLogin] = useState({ tenant_code: '', username: '', password: '' })
  // 强制改密表单数据：old_password（旧密码）、new_password（新密码）、confirm（确认新密码）
  const [change, setChange] = useState({ old_password: '', new_password: '', confirm: '' })
  // 找回密码表单数据：username（用户名）、contact（手机号/邮箱）、code（验证码）、new_password（新密码）
  const [reset, setReset] = useState({ username: '', contact: '', code: '', new_password: '' })
  // 提交按钮加载状态
  const [loading, setLoading] = useState(false)

  /**
   * 执行登录：调用 /api/v1/auth/login 接口
   * 登录成功后：存储 token → 判断是否需要强制改密 → 按角色分流跳转
   * 在 /app SPA 内登录则留在 /app（与原 Vue SPA 行为一致）
   */
  async function doLogin(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    const { res, json } = await apiJSON<ApiResp<AuthResult>>('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify(login),
    })
    setLoading(false)
    if (json?.code !== 0) {
      MessagePlugin.error(json?.message || '登录失败')
      return
    }
    // 存储 token 和用户信息到 localStorage
    setToken(json.data.token)
    localStorage.setItem('role', json.data.user.role)
    localStorage.setItem('username', json.data.user.username)
    // 首登强制改密：后端标记 must_change_password 时切换到改密模式
    if (json.data.user.must_change_password) {
      setMode('change')
      return
    }
    // P1-2：登录后跳转到 redirect 参数指定的页面（路由守卫带回的原路径）
    const urlParams = new URLSearchParams(location.search)
    const redirectPath = urlParams.get('redirect')
    if (redirectPath) {
      location.href = redirectPath
      return
    }
    // 在 /app SPA 内登录则留在 /app（与原 Vue SPA 行为一致），否则按角色分流
    // 角色分流涉及权限边界：超管进 /super，租户管理员进 /admin，其余进 /advisor
    if (location.pathname.startsWith('/app')) {
      location.href = json.data.user.role === 'super_admin' ? '/app' : '/app/advisor'
    } else {
      redirectByRole(json.data.user.role)
    }
  }

  /**
   * 执行强制改密：调用 /api/v1/auth/change-password 接口
   * 改密成功后延迟 800ms 跳回登录页，让用户看到成功提示
   */
  async function doForceChange(e: React.FormEvent) {
    e.preventDefault()
    if (change.new_password !== change.confirm) {
      MessagePlugin.error('两次输入不一致')
      return
    }
    setLoading(true)
    const { json } = await apiJSON('/api/v1/auth/change-password', {
      method: 'POST',
      body: JSON.stringify({ old_password: change.old_password, new_password: change.new_password }),
    })
    setLoading(false)
    if (json?.code !== 0) {
      MessagePlugin.error(json?.message || '修改失败')
      return
    }
    MessagePlugin.success('密码已更新，请重新登录')
    // 延迟 800ms 让用户看到成功提示后再跳登录页
    setTimeout(() => (location.href = location.pathname.startsWith('/app') ? '/app/login' : '/login'), 800)
  }

  /**
   * 找回密码第一步：发送验证码
   * 调用 /api/v1/auth/reset-password 接口，后端将验证码输出到服务端日志（开发模式）
   */
  async function doResetRequest(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    const { json } = await apiJSON('/api/v1/auth/reset-password', {
      method: 'POST',
      body: JSON.stringify({ username: reset.username, contact: reset.contact }),
    })
    setLoading(false)
    MessagePlugin.info(json?.message || '')
    if (json?.code === 0) {
      setMode('resetConfirm')
    }
  }

  /**
   * 找回密码第二步：验证验证码并重置密码
   * 调用 /api/v1/auth/verify-reset-code 接口
   */
  async function doResetConfirm(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    const { json } = await apiJSON('/api/v1/auth/verify-reset-code', {
      method: 'POST',
      body: JSON.stringify({ username: reset.username, code: reset.code, new_password: reset.new_password }),
    })
    setLoading(false)
    MessagePlugin.info(json?.message || '')
    if (json?.code === 0) setTimeout(() => (location.href = '/login'), 900)
  }

  return (
    <div style={wrap}>
      <div style={card}>
        <h2 style={{ marginBottom: 6 }}>登录工作台</h2>
        <div style={sub}>销售/管理员/平台超管统一入口</div>

        {/* 登录表单模式 */}
        {mode === 'login' && (
          <form onSubmit={doLogin}>
            <Label>企业码（租户用户必填）</Label>
            <Input value={login.tenant_code} onChange={(v) => setLogin({ ...login, tenant_code: v })} placeholder="如 acme" />
            <Label>用户名</Label>
            <Input value={login.username} onChange={(v) => setLogin({ ...login, username: v })} placeholder="用户名" />
            <Label>密码</Label>
            <Input type="password" value={login.password} onChange={(v) => setLogin({ ...login, password: v })} placeholder="密码" />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              登 录
            </Button>
            <div style={tip}>
              没有账号？<a href="/register">免费开通</a>　<a href="/">返回首页</a>　<a href="#" onClick={(e) => { e.preventDefault(); setMode('resetReq') }}>忘记密码</a>
            </div>
          </form>
        )}

        {/* 强制改密模式（首登或密码为出厂默认时触发） */}
        {mode === 'change' && (
          <form onSubmit={doForceChange}>
            <div style={{ background: '#fef3c7', color: '#92400e', padding: 10, borderRadius: 8, fontSize: 13, marginBottom: 6 }}>
              首次登录（或密码为出厂默认），请先设置新密码。要求：≥8位，含字母和数字。
            </div>
            <Label>当前密码</Label>
            <Input type="password" value={change.old_password} onChange={(v) => setChange({ ...change, old_password: v })} />
            <Label>新密码</Label>
            <Input type="password" value={change.new_password} onChange={(v) => setChange({ ...change, new_password: v })} />
            <Label>确认新密码</Label>
            <Input type="password" value={change.confirm} onChange={(v) => setChange({ ...change, confirm: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              确认修改
            </Button>
          </form>
        )}

        {/* 找回密码第一步：输入用户名和联系方式，发送验证码 */}
        {mode === 'resetReq' && (
          <form onSubmit={doResetRequest}>
            <Label>用户名</Label>
            <Input value={reset.username} onChange={(v) => setReset({ ...reset, username: v })} />
            <Label>注册手机号或邮箱</Label>
            <Input value={reset.contact} onChange={(v) => setReset({ ...reset, contact: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              发送验证码
            </Button>
            <div style={{ fontSize: 12, color: '#718096', marginTop: 6 }}>验证码将输出到服务端日志（开发模式），10分钟内有效</div>
          </form>
        )}

        {/* 找回密码第二步：输入验证码和新密码 */}
        {mode === 'resetConfirm' && (
          <form onSubmit={doResetConfirm}>
            <Label>验证码（查看服务端日志）</Label>
            <Input value={reset.code} onChange={(v) => setReset({ ...reset, code: v })} />
            <Label>新密码</Label>
            <Input type="password" value={reset.new_password} onChange={(v) => setReset({ ...reset, new_password: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              重置密码
            </Button>
          </form>
        )}

        {/* 页脚：法律链接与品牌名 */}
        <div className="footer-legal">
          <a href="/user-agreement">用户协议</a> ·{' '}
          <a href="/privacy-policy">隐私政策</a> · {brand.brandName} AI-SCRM 平台
        </div>
      </div>
    </div>
  )
}

// 页面外层容器样式（全屏居中，浅灰背景）
const wrap: React.CSSProperties = {
  minHeight: '100vh',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: '#f5f7fa',
  padding: 20,
}
// 表单卡片样式（白色圆角卡片，带阴影）
const card: React.CSSProperties = {
  background: '#fff',
  borderRadius: 12,
  padding: 36,
  boxShadow: '0 4px 24px rgba(0,0,0,.08)',
  width: 'min(420px, 92vw)',
}
// 副标题（登录说明）样式
const sub: React.CSSProperties = { color: '#718096', fontSize: 13, marginBottom: 22 }
// 表单字段标签组件：统一表单项标签样式
const Label: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <label style={{ display: 'block', fontSize: 13, margin: '14px 0 6px', color: '#4a5568' }}>{children}</label>
)
// 底部提示（注册/首页/忘记密码）样式
const tip: React.CSSProperties = { marginTop: 16, fontSize: 13, textAlign: 'center', color: '#718096' }
