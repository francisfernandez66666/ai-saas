import { useState, useEffect } from 'react'
import { AUTH, getToken } from '../lib/api'

// 企业知识库条目（我的知识库列表）
type Kb = { id: number; title: string; category?: string }

// /app 账号设置：改密、换绑邮箱（含验证码倒计时）、企业知识库上传/删除、账号注销（次日零点停用）
// 依赖 /api/v1/auth/change-password、/api/v1/auth/email/code（注意端点应为 email/code）、/api/v1/auth/email/change、/api/v1/admin/kb/*、/api/v1/admin/account/cancel

export default function AppSettings() {
  const [must, setMust] = useState(false)
  // 改密
  const [oldPwd, setOldPwd] = useState('')
  const [newPwd, setNewPwd] = useState('')
  const [pwdMsg, setPwdMsg] = useState('')
  // 换绑邮箱
  const [newEmail, setNewEmail] = useState('')
  const [emailCode, setEmailCode] = useState('')
  const [emMsg, setEmMsg] = useState('')
  const [cd, setCd] = useState(0)
  // 知识库
  const [kbTitle, setKbTitle] = useState('')
  const [kbContent, setKbContent] = useState('')
  const [kbList, setKbList] = useState<Kb[]>([])
  // 注销
  const [cancelPwd, setCancelPwd] = useState('')
  const [cMsg, setCMsg] = useState('')

  useEffect(() => {
    if (typeof window !== 'undefined') {
      const p = new URLSearchParams(window.location.search)
      if (p.get('must') === '1') setMust(true)
    }
    loadKb()
  }, [])

  async function changePwd() {
    const j: any = await AUTH('/api/v1/auth/change-password', { method: 'POST', body: { old_password: oldPwd, new_password: newPwd } })
    setPwdMsg(j.code === 0 ? '✓ 已修改' : (j.message || '失败'))
  }
  async function sendCode() {
    if (!newEmail) return
    // H6 修复：换绑邮箱应调用绑定专用验证码接口 /auth/email/code，
    // 原 /auth/email-code 是注册验证码接口，导致换绑验证码校验失败、换绑永久坏掉。
    await AUTH('/api/v1/auth/email/code', { method: 'POST', body: { email: newEmail } })
    // 发送后进入 60s 倒计时，禁止重复点击
    setCd(60)
    const t = setInterval(() => { setCd((c) => { if (c <= 1) { clearInterval(t); return 0 } return c - 1 }) }, 1000)
  }
  async function bindEmail() {
    const j: any = await AUTH('/api/v1/auth/email/change', { method: 'POST', body: { new_email: newEmail, code: emailCode } })
    setEmMsg(j.message || '')
  }
  async function loadKb() {
    const j: any = await AUTH('/api/v1/admin/kb/my?page=1&page_size=50')
    if (j.code === 0) setKbList(j.data?.list || [])
  }
  async function uploadKb() {
    const j: any = await AUTH('/api/v1/admin/kb/upload', { method: 'POST', body: { title: kbTitle, content: kbContent, category: '企业知识' } })
    if (j.code === 0) { setKbTitle(''); setKbContent(''); loadKb() }
    alert(j.message)
  }
  async function delKb(id: number) {
    await AUTH('/api/v1/admin/kb/my/' + id, { method: 'DELETE' })
    loadKb()
  }
  async function cancelAccount() {
    if (!confirm('确认注销？次日零点起账号停用（数据保留）')) return
    // 注销需二次确认密码；平台级动作，名下 API Key 同步禁用
    const j: any = await AUTH('/api/v1/admin/account/cancel', { method: 'POST', body: { password: cancelPwd } })
    setCMsg(j.message || '')
  }

  const card: React.CSSProperties = { background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }
  const inputStyle: React.CSSProperties = { width: '100%', padding: 9, border: '1px solid #e2e8f0', borderRadius: 6, marginBottom: 10, boxSizing: 'border-box' }
  const labelStyle: React.CSSProperties = { display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }

  return (
    <div style={{ maxWidth: 680, margin: '0 auto', padding: 16 }}>
      <h2 style={{ fontSize: 20, margin: '0 0 14px' }}>⚙️ 账号设置</h2>
      {must && <div style={{ ...card, border: '1px solid #fde68a', background: '#fffbeb' }}><p style={{ color: '#b45309', margin: 0 }}>⚠️ 首次登录请先修改密码后再使用其他功能</p></div>}

      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>修改密码</h3>
        <label style={labelStyle}>旧密码</label><input type="password" value={oldPwd} onChange={(e) => setOldPwd(e.target.value)} style={inputStyle} />
        <label style={labelStyle}>新密码（≥8位含字母数字）</label><input type="password" value={newPwd} onChange={(e) => setNewPwd(e.target.value)} style={inputStyle} />
        <button onClick={changePwd} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>修改</button>
        {pwdMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{pwdMsg}</p>}
      </div>

      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>换绑邮箱</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>新邮箱曾参与奖励领取时不可用于换绑</p>
        <div style={{ display: 'flex', gap: 8, marginBottom: 10 }}>
          <input placeholder="新邮箱" value={newEmail} onChange={(e) => setNewEmail(e.target.value)} style={{ ...inputStyle, marginBottom: 0, flex: 1 }} />
          <button disabled={cd > 0} onClick={sendCode} style={{ padding: '9px 14px', borderRadius: 8, border: '1px solid #cbd5e1', background: '#fff', cursor: 'pointer', whiteSpace: 'nowrap' }}>{cd > 0 ? cd + 's' : '发验证码'}</button>
        </div>
        <label style={labelStyle}>验证码</label><input value={emailCode} onChange={(e) => setEmailCode(e.target.value)} style={inputStyle} />
        <button onClick={bindEmail} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>换绑</button>
        {emMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{emMsg}</p>}
      </div>

      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>我的企业知识库</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>上传产品/企业资料，AI 对话自动融合检索（租户层优先）</p>
        <label style={labelStyle}>标题</label><input placeholder="如：售后政策" value={kbTitle} onChange={(e) => setKbTitle(e.target.value)} style={inputStyle} />
        <label style={labelStyle}>内容</label><textarea rows={4} value={kbContent} onChange={(e) => setKbContent(e.target.value)} style={{ ...inputStyle, resize: 'vertical' }} />
        <button onClick={uploadKb} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>上传切片入库</button>
        <ul style={{ marginTop: 12, paddingLeft: 18, fontSize: 14 }}>
          {kbList.map((f) => (
            <li key={f.id} style={{ margin: '6px 0' }}>{f.title} <a href="#" onClick={(e) => { e.preventDefault(); delKb(f.id) }} style={{ color: '#ef4444', marginLeft: 8 }}>删除</a></li>
          ))}
        </ul>
      </div>

      <div style={{ ...card, border: '1px solid #fecaca' }}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16, color: '#ef4444' }}>⚠️ 账号注销</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>今日内仍可登录，明日零点起停用；数据保留不删除；名下 API Key 同步禁用。</p>
        <label style={labelStyle}>输入登录密码确认</label><input type="password" value={cancelPwd} onChange={(e) => setCancelPwd(e.target.value)} style={inputStyle} />
        <button onClick={cancelAccount} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: '#ef4444', color: '#fff', cursor: 'pointer' }}>申请注销</button>
        {cMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{cMsg}</p>}
      </div>
    </div>
  )
}
