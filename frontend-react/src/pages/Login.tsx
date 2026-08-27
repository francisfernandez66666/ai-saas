import { useState } from 'react'
import { Button, Input, MessagePlugin } from 'tdesign-react'
import { apiJSON, setToken, redirectByRole } from '../lib/api'
import { useBrand } from '../lib/branding'

type Mode = 'login' | 'change' | 'resetReq' | 'resetConfirm'

// 登录/改密/找回密码页：支持租户码登录、首登强改密、验证码找回；
// 依赖 /api/v1/auth/login、/api/v1/auth/change-password、/api/v1/auth/reset-password、/api/v1/auth/verify-reset-code
export default function Login() {
  const brand = useBrand()
  const [mode, setMode] = useState<Mode>('login')
  const [login, setLogin] = useState({ tenant_code: '', username: '', password: '' })
  const [change, setChange] = useState({ old_password: '', new_password: '', confirm: '' })
  const [reset, setReset] = useState({ username: '', contact: '', code: '', new_password: '' })
  const [loading, setLoading] = useState(false)

  async function doLogin(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    const { res, json } = await apiJSON('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify(login),
    })
    setLoading(false)
    if (json?.code !== 0) {
      MessagePlugin.error(json?.message || '登录失败')
      return
    }
    setToken(json.data.token)
    localStorage.setItem('role', json.data.user.role)
    localStorage.setItem('username', json.data.user.username)
    if (json.data.user.must_change_password) {
      setMode('change')
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

        <div className="footer-legal">
          <a href="/user-agreement">用户协议</a> ·{' '}
          <a href="/privacy-policy">隐私政策</a> · {brand.brandName} AI-SCRM 平台
        </div>
      </div>
    </div>
  )
}

const wrap: React.CSSProperties = {
  minHeight: '100vh',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  background: '#f5f7fa',
  padding: 20,
}
const card: React.CSSProperties = {
  background: '#fff',
  borderRadius: 12,
  padding: 36,
  boxShadow: '0 4px 24px rgba(0,0,0,.08)',
  width: 'min(420px, 92vw)',
}
const sub: React.CSSProperties = { color: '#718096', fontSize: 13, marginBottom: 22 }
const Label: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <label style={{ display: 'block', fontSize: 13, margin: '14px 0 6px', color: '#4a5568' }}>{children}</label>
)
const tip: React.CSSProperties = { marginTop: 16, fontSize: 13, textAlign: 'center', color: '#718096' }
