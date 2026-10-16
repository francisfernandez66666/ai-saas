/**
 * AppSettings.tsx：移动端账号设置页
 * 支持改密、换绑邮箱（含验证码倒计时）、企业知识库（管理员可传删/成员只读）、账号注销（次日零点停用）
 * 依赖接口：/api/v1/auth/change-password、/api/v1/auth/email/code、/api/v1/auth/email/change、
 * /api/v1/admin/kb/*（写）或 /api/v1/advisor/kb/my（成员只读，G-23）、/api/v1/admin/account/cancel
 */
import { useState, useEffect } from 'react'
import { confirmDialog, toast } from '../lib/confirm'
import { AUTH } from '../lib/api'
import { isStaff } from '../lib/roles'
import { DEFAULT_CATEGORY } from '../lib/tenantKb'
import { useI18n } from '../i18n/useI18n'
import type { ApiResp, KbMaterial } from '../types'

// 企业知识库列表分页响应 data 结构（admin/kb/my）
type KbListResp = { list: KbMaterial[]; total: number; page: number; page_size: number }

/**
 * /app 账号设置页组件
 * 四大功能模块：
 * 1. 修改密码：输入旧密码和新密码
 * 2. 换绑邮箱：输入新邮箱 → 发送验证码 → 输入验证码完成换绑
 * 3. 企业知识库：上传标题+内容切片 → 列表展示 → 支持删除
 * 4. 账号注销：输入密码确认 → 次日零点停用（数据保留）
 */
export default function AppSettings() {
  const { t } = useI18n()
  // P1-42(2026-09-09)：企业知识库管理/账号注销接口须管理员权限——
  // 成员角色(sales/user)进入 /app/settings 不应 403 白屏：改密/换绑邮箱全员可用，
  // KB 管理区块仅管理员展示
  const isAdmin = isStaff(localStorage.getItem('role') || '')
  // 是否为首次登录强制改密模式
  const [must, setMust] = useState(false)
  // 改密相关状态
  const [oldPwd, setOldPwd] = useState('')      // 旧密码
  const [newPwd, setNewPwd] = useState('')      // 新密码
  const [pwdMsg, setPwdMsg] = useState('')      // 改密结果提示
  // 换绑邮箱相关状态
  const [newEmail, setNewEmail] = useState('')  // 新邮箱地址
  const [emailCode, setEmailCode] = useState('') // 邮箱验证码
  const [emMsg, setEmMsg] = useState('')        // 换绑结果提示
  const [cd, setCd] = useState(0)               // 验证码发送倒计时（秒）
  // 知识库相关状态
  const [kbTitle, setKbTitle] = useState('')    // 知识库标题
  const [kbContent, setKbContent] = useState('') // 知识库内容
  const [kbList, setKbList] = useState<KbMaterial[]>([]) // 知识库列表
  // 注销相关状态
  const [cancelPwd, setCancelPwd] = useState('') // 注销确认密码
  const [cMsg, setCMsg] = useState('')          // 注销结果提示
  const [delBusy, setDelBusy] = useState(false)
  const [dMsg, setDMsg] = useState('')

  useEffect(() => {
    // 检查 URL 参数 must=1，首次登录强制改密时展示提示
    if (typeof window !== 'undefined') {
      const p = new URLSearchParams(window.location.search)
      if (p.get('must') === '1') setMust(true)
    }
    // G-23(2026-09-25)：知识库列表全员可查——管理员读 /admin/kb/my，成员读 /advisor/kb/my，
    // 不再是"成员整块隐藏"（旧 P1-42 做法让销售看不到公司沉淀的知识）。
    loadKb()
  }, [])

  /**
   * 修改登录密码：调用 /api/v1/auth/change-password 接口
   */
  async function changePwd() {
    const j: ApiResp<unknown> = await AUTH('/api/v1/auth/change-password', { method: 'POST', body: { old_password: oldPwd, new_password: newPwd } })
    setPwdMsg(j.code === 0 ? t('appSettings.pwdChanged') : (j.message || t('common.failed')))
  }

  /**
   * 发送邮箱验证码：调用 /api/v1/auth/email/code 接口（绑定专用）
   * H6 修复：换绑邮箱应调用绑定专用验证码接口，原 /auth/email-code 是注册验证码接口
   */
  async function sendCode() {
    if (!newEmail) return
    // H6 修复：换绑邮箱应调用绑定专用验证码接口 /auth/email/code，
    // 原 /auth/email-code 是注册验证码接口，导致换绑验证码校验失败、换绑永久坏掉。
    await AUTH('/api/v1/auth/email/code', { method: 'POST', body: { new_email: newEmail } })
    // 发送后进入 60s 倒计时，禁止重复点击
    setCd(60)
    // 局部变量不叫 t——t 是 useI18n 的翻译函数，遮蔽会让按钮文案变成 key 原文
    const timer = setInterval(() => { setCd((c) => { if (c <= 1) { clearInterval(timer); return 0 } return c - 1 }) }, 1000)
  }

  /**
   * 用验证码换绑新邮箱：调用 /api/v1/auth/email/change 接口
   */
  async function bindEmail() {
    const j: ApiResp<unknown> = await AUTH('/api/v1/auth/email/change', { method: 'POST', body: { new_email: newEmail, code: emailCode } })
    setEmMsg(j.message || '')
  }

  /**
   * 加载企业知识库列表：管理员走 /admin/kb/my，成员走 /advisor/kb/my（G-23 只读子集）
   * 两个端点返回同一份租户企业知识列表，成员端只开读、上传/删除仍在 admin 组。
   */
  async function loadKb() {
    const j: ApiResp<KbListResp> = await AUTH((isAdmin ? '/api/v1/admin/kb/my' : '/api/v1/advisor/kb/my') + '?page=1&page_size=50')
    if (j.code === 0) setKbList(j.data?.list || [])
  }

  /**
   * 上传知识库切片：调用 /api/v1/admin/kb/upload 接口
   * 上传成功后清空表单并刷新列表
   */
  async function uploadKb() {
    const j: ApiResp<unknown> = await AUTH('/api/v1/admin/kb/upload', { method: 'POST', body: { title: kbTitle, content: kbContent, category: DEFAULT_CATEGORY } })
    if (j.code === 0) { setKbTitle(''); setKbContent(''); loadKb() }
    toast.info(j.message || t('appSettings.uploaded'))
  }

  /**
   * 删除知识库条目：调用 /api/v1/admin/kb/my/:id DELETE 接口
   */
  async function delKb(id: number) {
    // P2-14 修复(2026-09-20 批三)：删除知识切片原一键直删——补二次确认（与注销/PII 删除同口径）
    if (!(await confirmDialog(t('appSettings.delKbConfirm'), t('appSettings.delKbTitle')))) return
    await AUTH('/api/v1/admin/kb/my/' + id, { method: 'DELETE' })
    loadKb()
  }

  /**
   * 申请账号注销：调用 /api/v1/admin/account/cancel 接口
   * 需二次确认密码；平台级动作，名下 API Key 同步禁用
   */
  async function cancelAccount() {
    if (!(await confirmDialog(t('appSettings.cancelConfirm')))) return
    // 注销需二次确认密码；平台级动作，名下 API Key 同步禁用
    const j: ApiResp<unknown> = await AUTH('/api/v1/admin/account/cancel', { method: 'POST', body: { password: cancelPwd } })
    setCMsg(j.message || '')
  }

  /**
   * F13/C2：账号 PII 删除请求。
   * 与账号注销不同：注销只停用，删除申请到期后匿名化账号侧 PII。
   */
  async function requestUserDeletion() {
    if (!(await confirmDialog(t('appSettings.userDelConfirm')))) return
    setDelBusy(true)
    const j: ApiResp<{ deadline?: string; duplicated?: boolean }> = await AUTH('/api/v1/privacy/deletion-request', { method: 'POST', body: { scope: 'user' } })
    const deadline = j.data?.deadline ? new Date(j.data.deadline).toLocaleString('zh-CN', { hour12: false }) : ''
    setDMsg(j.code === 0 ? t('appSettings.accepted', { deadline: deadline ? t('appSettings.deadline', { deadline }) : '' }) : (j.message || t('appSettings.submitFailed')))
    setDelBusy(false)
  }

  // 卡片样式
  const card: React.CSSProperties = { background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }
  // 输入框样式
  const inputStyle: React.CSSProperties = { width: '100%', padding: 9, border: '1px solid #e2e8f0', borderRadius: 6, marginBottom: 10, boxSizing: 'border-box' }
  // 标签样式
  const labelStyle: React.CSSProperties = { display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }

  return (
    <div style={{ maxWidth: 680, margin: '0 auto', padding: 16 }}>
      <h2 style={{ fontSize: 20, margin: '0 0 14px' }}>{t('appSettings.title')}</h2>
      {/* 首次登录强制改密提示 */}
      {must && <div style={{ ...card, border: '1px solid #fde68a', background: '#fffbeb' }}><p style={{ color: '#b45309', margin: 0 }}>{t('appSettings.mustChangePwd')}</p></div>}

      {/* 修改密码模块 */}
      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>{t('appSettings.changePwdTitle')}</h3>
        <label style={labelStyle}>{t('appSettings.oldPwd')}</label><input type="password" value={oldPwd} onChange={(e) => setOldPwd(e.target.value)} style={inputStyle} />
        <label style={labelStyle}>{t('appSettings.newPwd')}</label><input type="password" value={newPwd} onChange={(e) => setNewPwd(e.target.value)} style={inputStyle} />
        <button onClick={changePwd} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>{t('appSettings.changeBtn')}</button>
        {pwdMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{pwdMsg}</p>}
      </div>

      {/* 换绑邮箱模块 */}
      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>{t('appSettings.changeEmailTitle')}</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>{t('appSettings.emailRewardWarn')}</p>
        <div style={{ display: 'flex', gap: 8, marginBottom: 10 }}>
          <input placeholder={t('appSettings.newEmailPh')} value={newEmail} onChange={(e) => setNewEmail(e.target.value)} style={{ ...inputStyle, marginBottom: 0, flex: 1 }} />
          <button disabled={cd > 0} onClick={sendCode} style={{ padding: '9px 14px', borderRadius: 8, border: '1px solid #cbd5e1', background: '#fff', cursor: 'pointer', whiteSpace: 'nowrap' }}>{cd > 0 ? cd + 's' : t('appSettings.sendCode')}</button>
        </div>
        <label style={labelStyle}>{t('appSettings.codeLabel')}</label><input value={emailCode} onChange={(e) => setEmailCode(e.target.value)} style={inputStyle} />
        <button onClick={bindEmail} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>{t('appSettings.bindBtn')}</button>
        {emMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{emMsg}</p>}
      </div>

      {/* 个人数据删除申请（F13/C2：登录态所有成员可撤回本人账号 PII） */}
      <div style={{ ...card, border: '1px solid #ddd6fe' }}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>{t('appSettings.piiTitle')}</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>{t('appSettings.piiDesc')}</p>
        <button disabled={delBusy} onClick={requestUserDeletion} style={{ padding: '9px 16px', borderRadius: 8, border: '1px solid #7c3aed', background: '#fff', color: '#7c3aed', cursor: 'pointer' }}>{delBusy ? t('appSettings.piiSubmitting') : t('appSettings.piiBtn')}</button>
        {dMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{dMsg}</p>}
      </div>

      {/* 企业知识库模块（G-23：全员只读可见，上传/删除仍仅管理员） */}
      <div style={card}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>{t('appSettings.kbTitle')}</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>
          {isAdmin ? t('appSettings.kbAdminDesc') : t('appSettings.kbMemberDesc')}
        </p>
        {isAdmin && <>
          <label style={labelStyle}>{t('common.title')}</label><input placeholder={t('appSettings.kbTitlePh')} value={kbTitle} onChange={(e) => setKbTitle(e.target.value)} style={inputStyle} />
          <label style={labelStyle}>{t('common.content')}</label><textarea rows={4} value={kbContent} onChange={(e) => setKbContent(e.target.value)} style={{ ...inputStyle, resize: 'vertical' }} />
          <button onClick={uploadKb} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', cursor: 'pointer' }}>{t('appSettings.kbUploadBtn')}</button>
        </>}
        {/* 知识库列表：管理员可删，成员只看 */}
        <ul style={{ marginTop: 12, paddingLeft: 18, fontSize: 14 }}>
          {kbList.map((f) => (
            <li key={f.id} style={{ margin: '6px 0' }}>{f.title}
              {isAdmin && <a href="#" onClick={(e) => { e.preventDefault(); delKb(f.id) }} style={{ color: '#ef4444', marginLeft: 8 }}>{t('crud.delete')}</a>}
            </li>
          ))}
        </ul>
        {!isAdmin && kbList.length === 0 && <p style={{ fontSize: 13, color: '#9ca3af', margin: '12px 0 0' }}>{t('appSettings.kbEmptyMember')}</p>}
      </div>

      {/* 账号注销模块（P1-42：仅管理员展示；成员角色无此权限） */}
      {isAdmin && <div style={{ ...card, border: '1px solid #fecaca' }}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16, color: '#ef4444' }}>{t('appSettings.cancelTitle')}</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 0 }}>{t('appSettings.cancelDesc')}</p>
        <label style={labelStyle}>{t('appSettings.cancelPwdLabel')}</label><input type="password" value={cancelPwd} onChange={(e) => setCancelPwd(e.target.value)} style={inputStyle} />
        <button onClick={cancelAccount} style={{ padding: '9px 16px', borderRadius: 8, border: 'none', background: '#ef4444', color: '#fff', cursor: 'pointer' }}>{t('appSettings.cancelBtn')}</button>
        {cMsg && <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{cMsg}</p>}
      </div>}
    </div>
  )
}
