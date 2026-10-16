/**
 * Login.tsx：登录/改密/找回密码页
 * 支持租户码登录、首登强改密、验证码找回密码四种模式
 * 依赖接口：/api/v1/auth/login、/api/v1/auth/change-password、/api/v1/auth/reset-password、/api/v1/auth/verify-reset-code、/api/v1/auth/reset-channel
 * 后端 MustChangePasswordGuard 拦截后由 api.ts 跳转 /login?mcp=1，本页据此直接进入改密表单
 */
import { useEffect, useState } from 'react'
import { Button, Input, MessagePlugin } from 'tdesign-react'
import { apiJSON, setToken, redirectByRole } from '../lib/api'
import { useBrand } from '../lib/branding'
import { useI18n } from '../i18n/useI18n'
import { LanguageSwitcher } from '../components/LanguageSwitcher'
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
  // P2-14：全站文案经 t() 下发（键类型以中文词典为源真相，写错编译期报错）
  const { t } = useI18n()
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
  // 找回密码实际生效通道（S2 修复，2026-09-23 批二）：由后端 GET /auth/reset-channel 驱动。
  // 原页面把"验证码将输出到服务端日志（开发模式）"硬编码给最终用户——既泄露实现细节，
  // 又在 SMTP 已配好的生产环境说假话（用户去翻日志、其实邮件已发出）。
  // null 表示尚未取到：此时不渲染通道相关提示，宁可少说也不说错。
  const [resetChannel, setResetChannel] = useState<'smtp' | 'log' | null>(null)
  useEffect(() => {
    let alive = true
    void (async () => {
      const { json } = await apiJSON<ApiResp<{ channel?: string }>>('/api/v1/auth/reset-channel')
      if (!alive) return
      // 后端只在"配置 smtp 且 SMTP 环境变量齐"时才回 smtp；取不到响应保持 null（不猜）
      if (json?.code === 0 && json.data?.channel) setResetChannel(json.data.channel === 'smtp' ? 'smtp' : 'log')
    })()
    return () => {
      alive = false
    }
  }, [])

  /**
   * 执行登录：调用 /api/v1/auth/login 接口
   * 登录成功后：存储 token → 判断是否需要强制改密 → 按角色分流跳转
   * 在 /app SPA 内登录则留在 /app（与原 Vue SPA 行为一致）
   */
  async function doLogin(e: React.FormEvent) {
    e.preventDefault()
    setLoading(true)
    // P1-6 修复(2026-09-20 审计批)：try/finally 兜底——apiJSON 网络层失败虽已归一为
    // {res:null,json:null}，仍防任何意外抛错让按钮永久 loading（断网提交后页面卡死）。
    let res: Response | null
    let json: ApiResp<AuthResult> | null
    try {
      ;({ res, json } = await apiJSON<ApiResp<AuthResult>>('/api/v1/auth/login', {
        method: 'POST',
        body: JSON.stringify(login),
      }))
    } finally {
      setLoading(false)
    }
    if (!res) {
      MessagePlugin.error(t('login.networkError'))
      return
    }
    if (json?.code !== 0) {
      MessagePlugin.error(json?.message || t('login.failed'))
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
    // P1-46 修复：仅允许站内相对路径——reject https://evil.com 与 //evil.com（协议相对）
    const urlParams = new URLSearchParams(location.search)
    const rawRedirect = urlParams.get('redirect')
    const redirectPath =
      rawRedirect && rawRedirect.startsWith('/') && !rawRedirect.startsWith('//') && !rawRedirect.startsWith('/\\') // P2 修复(2026-09-15)：浏览器把 /\evil.com 也按协议相对解析→可外跳站外，一并挡掉
        ? rawRedirect
        : ''
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
      MessagePlugin.error(t('login.mismatch'))
      return
    }
    setLoading(true)
    let json: ApiResp<unknown> | null
    try {
      json = (await apiJSON('/api/v1/auth/change-password', {
        method: 'POST',
        body: JSON.stringify({ old_password: change.old_password, new_password: change.new_password }),
      })).json as ApiResp<unknown> | null
    } finally {
      setLoading(false) // P1-6(2026-09-20)：同 doLogin，防抛错卡死 loading
    }
    if (json?.code !== 0) {
      MessagePlugin.error(json?.message || t('login.updateFailed'))
      return
    }
    MessagePlugin.success(t('login.passwordUpdated'))
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
    let json: ApiResp<unknown> | null
    try {
      json = (await apiJSON('/api/v1/auth/reset-password', {
        method: 'POST',
        body: JSON.stringify({ username: reset.username, contact: reset.contact }),
      })).json as ApiResp<unknown> | null
    } finally {
      setLoading(false) // P1-6(2026-09-20)：同 doLogin，防抛错卡死 loading
    }
    MessagePlugin.info(json?.message || (json ? '' : t('login.resetSendFailed')))
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
    let json: ApiResp<unknown> | null
    try {
      json = (await apiJSON('/api/v1/auth/verify-reset-code', {
        method: 'POST',
        body: JSON.stringify({ username: reset.username, code: reset.code, new_password: reset.new_password }),
      })).json as ApiResp<unknown> | null
    } finally {
      setLoading(false) // P1-6(2026-09-20)：同 doLogin，防抛错卡死 loading
    }
    MessagePlugin.info(json?.message || (json ? '' : t('login.resetSubmitFailed')))
    if (json?.code === 0) setTimeout(() => (location.href = '/login'), 900)
  }

  return (
    <div style={wrap}>
      <div style={card}>
        {/* 语言切换：右上角窄位，登录前即可切（出海首屏） */}
        <div style={{ display: 'flex', justifyContent: 'flex-end', marginBottom: 4 }}>
          <LanguageSwitcher />
        </div>
        <h2 style={{ marginBottom: 6 }}>{t('login.title')}</h2>
        <div style={sub}>{t('login.subtitle')}</div>

        {/* 登录表单模式 */}
        {mode === 'login' && (
          <form onSubmit={doLogin}>
            <Label>{t('login.tenantCode')}</Label>
            <Input value={login.tenant_code} onChange={(v) => setLogin({ ...login, tenant_code: v })} placeholder={t('login.tenantCodePlaceholder')} />
            <Label>{t('login.username')}</Label>
            <Input value={login.username} onChange={(v) => setLogin({ ...login, username: v })} placeholder={t('login.usernamePlaceholder')} />
            <Label>{t('login.password')}</Label>
            <Input type="password" value={login.password} onChange={(v) => setLogin({ ...login, password: v })} placeholder={t('login.passwordPlaceholder')} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              {t('login.submit')}
            </Button>
            <div style={tip}>
              {t('login.noAccount')}<a href="/register">{t('login.freeSignup')}</a>　<a href="/">{t('login.backHome')}</a>　<a href="#" onClick={(e) => { e.preventDefault(); setMode('resetReq') }}>{t('login.forgotPassword')}</a>
            </div>
          </form>
        )}

        {/* 强制改密模式（首登或密码为出厂默认时触发） */}
        {mode === 'change' && (
          <form onSubmit={doForceChange}>
            <div style={{ background: '#fef3c7', color: '#92400e', padding: 10, borderRadius: 8, fontSize: 13, marginBottom: 6 }}>
              {t('login.mcpNotice')}
            </div>
            <Label>{t('login.currentPassword')}</Label>
            <Input type="password" value={change.old_password} onChange={(v) => setChange({ ...change, old_password: v })} />
            <Label>{t('login.newPassword')}</Label>
            <Input type="password" value={change.new_password} onChange={(v) => setChange({ ...change, new_password: v })} />
            <Label>{t('login.confirmPassword')}</Label>
            <Input type="password" value={change.confirm} onChange={(v) => setChange({ ...change, confirm: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              {t('login.confirmSubmit')}
            </Button>
          </form>
        )}

        {/* 找回密码第一步：输入用户名和联系方式，发送验证码 */}
        {mode === 'resetReq' && (
          <form onSubmit={doResetRequest}>
            <Label>{t('login.username')}</Label>
            <Input value={reset.username} onChange={(v) => setReset({ ...reset, username: v })} />
            <Label>{t('login.resetContact')}</Label>
            <Input value={reset.contact} onChange={(v) => setReset({ ...reset, contact: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              {t('login.sendCode')}
            </Button>
            {/* S2：文案按后端实际生效通道渲染，不再向最终用户暴露"服务端日志"这一内部实现 */}
            <div style={{ fontSize: 12, color: '#718096', marginTop: 6 }}>
              {resetChannel === 'smtp'
                ? t('login.resetHintSmtp')
                : resetChannel === 'log'
                  ? t('login.resetHintLog')
                  : t('login.resetHintDefault')}
            </div>
          </form>
        )}

        {/* 找回密码第二步：输入验证码和新密码 */}
        {mode === 'resetConfirm' && (
          <form onSubmit={doResetConfirm}>
            {/* S2：不再提示"查看服务端日志"——smtp 态应看邮箱，log 态该找管理员，两者都不该看到内部实现 */}
            <Label>{resetChannel === 'log' ? t('login.codeFromAdmin') : t('login.codeFromEmail')}</Label>
            <Input value={reset.code} onChange={(v) => setReset({ ...reset, code: v })} />
            <Label>{t('login.newPassword')}</Label>
            <Input type="password" value={reset.new_password} onChange={(v) => setReset({ ...reset, new_password: v })} />
            <Button theme="primary" type="submit" loading={loading} style={{ width: '100%', marginTop: 22 }}>
              {t('login.resetSubmit')}
            </Button>
          </form>
        )}

        {/* 页脚：法律链接与品牌名 */}
        <div className="footer-legal">
          <a href="/user-agreement">{t('login.userAgreement')}</a> ·{' '}
          <a href="/privacy-policy">{t('login.privacyPolicy')}</a> · {t('login.footer', { brand: brand.brandName })}
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
  padding: 'clamp(20px, 5vw, 36px)',
  boxShadow: '0 4px 24px rgba(0,0,0,.08)',
  width: 'min(420px, 92vw)',
  maxWidth: '100vw',
}
// 副标题（登录说明）样式
const sub: React.CSSProperties = { color: '#718096', fontSize: 13, marginBottom: 22 }
// 表单字段标签组件：统一表单项标签样式
const Label: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <label style={{ display: 'block', fontSize: 13, margin: '14px 0 6px', color: '#4a5568' }}>{children}</label>
)
// 底部提示（注册/首页/忘记密码）样式
const tip: React.CSSProperties = { marginTop: 16, fontSize: 13, textAlign: 'center', color: '#718096' }
