/**
 * Register.tsx：租户自助开通注册页
 * 填写企业信息并创建管理员账号；支持邮箱验证（平台热开关）、邀请码（ref）返利
 * 依赖接口：/api/v1/auth/register-config、/api/v1/tenant/check-code、/api/v1/auth/email-code、/api/v1/tenant/signup
 */
import { useState, useEffect, useRef } from 'react'
import { Button, Input, MessagePlugin } from 'tdesign-react'
import { apiJSON } from '../lib/api'
import { useBrand } from '../lib/branding'

/**
 * 租户自助开通页组件
 * 1. 加载注册配置（是否开启邮箱验证）
 * 2. 校验访问标识（子域名）可用性
 * 3. 支持邮箱验证码发送与倒计时
 * 4. 提交注册表单，成功后跳转登录页
 */
export default function Register() {
  // 读取品牌配置（用于页脚展示品牌名）
  const brand = useBrand()
  // 平台是否开启邮箱验证（来自 register-config 热开关）
  const [emailVerifyOn, setEmailVerifyOn] = useState(false)
  // 开通表单数据
  const [form, setForm] = useState({
    company_name: '',   // 企业名称
    code: '',           // 访问标识（子域名，如 acme）
    username: '',       // 管理员账号
    password: '',       // 管理员密码
    admin_email: '',    // 管理员邮箱
    email_code: '',     // 邮箱验证码
    contact_name: '',   // 联系人姓名
    contact_phone: '',  // 联系手机
  })
  // 访问标识（子域名）校验提示：ok 表示可用
  const [codeTip, setCodeTip] = useState<{ text: string; ok: boolean }>({ text: '', ok: false })
  // 提交结果提示文案
  const [msg, setMsg] = useState('')
  // 预留倒计时（当前未启用，保留兼容）
  const [cd, setCd] = useState(0)
  // 防抖/倒计时定时器引用
  const timer = useRef<number | null>(null)

  useEffect(() => {
    // 加载注册配置：获取邮箱验证是否开启
    apiJSON('/api/v1/auth/register-config').then(({ json }) => {
      if (json?.data?.email_verify_enabled) setEmailVerifyOn(true)
    })
    // 邀请注册：URL 带 ?ref= 时展示邀请返利横幅（注册双方获赠 token）
    const ref = new URLSearchParams(location.search).get('ref')
    if (ref) {
      const el = document.getElementById('refBanner')
      if (el) el.style.display = 'block'
    }
    return () => {
      if (timer.current) window.clearInterval(timer.current)
    }
  }, [])

  /**
   * 校验访问标识（子域名）可用性
   * 防抖 400ms：避免每次输入都打校验接口
   * @param v - 用户输入的访问标识
   */
  function checkCode(v: string) {
    if (!v) {
      setCodeTip({ text: '', ok: false })
      return
    }
    if (timer.current) window.clearTimeout(timer.current)
    // 防抖 400ms：避免每次输入都打校验接口
    timer.current = window.setTimeout(async () => {
      const { json } = await apiJSON('/api/v1/tenant/check-code?code=' + encodeURIComponent(v))
      const d = json?.data || {}
      setCodeTip({ text: d.reason || '', ok: !!d.available })
    }, 400)
  }

  /**
   * 发送邮箱验证码
   * 调用 /api/v1/auth/email-code 接口，发送后进入 60s 倒计时防重复发送
   */
  async function sendCode() {
    if (!form.admin_email) {
      MessagePlugin.warning('请先填写邮箱')
      return
    }
    const btn = document.getElementById('sendBtn') as HTMLButtonElement
    if (btn) btn.disabled = true
    const { json } = await apiJSON('/api/v1/auth/email-code', {
      method: 'POST',
      body: JSON.stringify({ email: form.admin_email }),
    })
    if (json?.code === 0) {
      MessagePlugin.success('验证码已发送，请查收邮箱')
      // 60s 倒计时防重复发送
      let left = 60
      if (btn) btn.textContent = left + 's'
      timer.current = window.setInterval(() => {
        left -= 1
        if (btn) btn.textContent = left + 's'
        if (left <= 0 && timer.current) {
          window.clearInterval(timer.current)
          if (btn) {
            btn.disabled = false
            btn.textContent = '重新获取'
          }
        }
      }, 1000)
    } else {
      MessagePlugin.error(json?.message || '发送失败')
      if (btn) btn.disabled = false
    }
  }

  /**
   * 提交注册表单
   * 调用 /api/v1/tenant/signup 接口
   * 成功后：展示提示 → 延迟 1500ms 跳转登录页
   * 邀请码（ref）统一大写提交，用于防薅返利与幂等发放
   */
  async function submitForm(e: React.FormEvent) {
    e.preventDefault()
    setMsg('开通中...')
    const body: any = { ...form }
    // 将邀请码（统一大写）一并提交，用于防薅返利与幂等发放
    const ref = new URLSearchParams(location.search).get('ref')
    if (ref) body.ref = ref.trim().toUpperCase()
    const { json } = await apiJSON('/api/v1/tenant/signup', {
      method: 'POST',
      body: JSON.stringify(body),
    })
    if (json?.code === 0) {
      setMsg((json.message || '开通成功') + '！即将跳转登录...')
      setTimeout(() => (location.href = (json.data?.login_url || (location.pathname.startsWith('/app') ? '/app/login' : '/login'))), 1500)
    } else {
      setMsg(json?.message || '开通失败')
    }
  }

  // 通用表单字段 setter：根据 key 更新 form 对应字段
  const set = (k: keyof typeof form) => (v: string) => setForm({ ...form, [k]: v })

  return (
    <div style={wrap}>
      <div style={card}>
        <h2 style={{ marginBottom: 6 }}>免费开通试用</h2>
        <div style={sub}>7 天全功能试用，无需绑卡</div>
        {/* 邀请返利横幅：URL 带 ?ref= 时显示 */}
        <div id="refBanner" style={{ display: 'none', background: '#eef2ff', color: '#4f46e5', padding: '8px 12px', borderRadius: 8, fontSize: 13, marginBottom: 14 }}>
          🎁 已应用好友邀请，注册成功后双方均可获赠 token
        </div>

        <form onSubmit={submitForm}>
          <Label>企业名称 *</Label>
          <Input value={form.company_name} onChange={set('company_name')} />
          <Label>访问标识 *（将作为子域名 acme.example.com）</Label>
          <Input value={form.code} onChange={(v) => { setForm({ ...form, code: v }); checkCode(v) }} placeholder="小写字母开头，3-20位" />
          <div style={{ fontSize: 13, minHeight: 18, color: codeTip.ok ? '#38a169' : '#e53e3e' }}>{codeTip.text}</div>
          <Label>管理员账号 *</Label>
          <Input value={form.username} onChange={set('username')} placeholder="登录用户名" />
          <Label>管理员密码 *（至少6位）</Label>
          <Input type="password" value={form.password} onChange={set('password')} />

          {/* 邮箱验证模块：平台开启时展示邮箱输入与验证码获取 */}
          {emailVerifyOn && (
            <>
              <Label>管理员邮箱 *（用于接收验证码与账号找回）</Label>
              <div style={{ display: 'flex', gap: 8 }}>
                <Input style={{ flex: 1 }} value={form.admin_email} onChange={set('admin_email')} placeholder="you@company.com" />
                <Button id="sendBtn" variant="outline" theme="primary" onClick={sendCode} style={{ whiteSpace: 'nowrap' }}>
                  获取验证码
                </Button>
              </div>
              <Label style={{ marginTop: 8 }}>邮箱验证码 *</Label>
              <Input value={form.email_code} onChange={set('email_code')} placeholder="6位数字验证码" maxlength={6} />
            </>
          )}

          <Label>联系人</Label>
          <Input value={form.contact_name} onChange={set('contact_name')} />
          <Label>联系手机</Label>
          <Input value={form.contact_phone} onChange={set('contact_phone')} />

          {/* 法律声明：注册即视为同意用户协议与隐私政策 */}
          <div style={{ fontSize: 12, color: '#64748b', margin: '12px 0', lineHeight: 1.6 }}>
            注册即代表您已阅读并同意 <a href="/user-agreement">《用户协议》</a> 与 <a href="/privacy-policy">《隐私政策》</a>，
            平台将记录您的签署时间与状态。
          </div>
          <Button theme="primary" type="submit" style={{ width: '100%' }}>立即开通</Button>
          <div style={{ fontSize: 13, minHeight: 18, marginTop: 12 }}>{msg}</div>
        </form>

        {/* 底部导航：已有账号/查看套餐 */}
        <div style={tip}>
          已有账号？<a href="/login">直接登录</a>　<a href="/pricing">查看套餐</a>
        </div>
      </div>
      {/* 页脚：法律链接与品牌名 */}
      <div className="footer-legal">
        <a href="/user-agreement">用户协议</a> ·{' '}
        <a href="/privacy-policy">隐私政策</a> · {brand.brandName} AI-SCRM 平台
      </div>
    </div>
  )
}

// 页面外层容器样式（居中纵向排列）
const wrap: React.CSSProperties = {
  minHeight: '100vh',
  display: 'flex',
  flexDirection: 'column',
  alignItems: 'center',
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
// 副标题（试用说明）样式
const sub: React.CSSProperties = { color: '#718096', fontSize: 13, marginBottom: 22 }
// 表单字段标签组件
const Label: React.FC<{ children: React.ReactNode; style?: React.CSSProperties }> = ({ children, style }) => (
  <label style={{ display: 'block', fontSize: 13, margin: '14px 0 6px', color: '#4a5568', ...style }}>{children}</label>
)
// 底部提示（已注册/查看套餐）样式
const tip: React.CSSProperties = { marginTop: 16, fontSize: 13, textAlign: 'center', color: '#718096' }
