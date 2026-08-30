import { useState, useEffect } from 'react'
import { AUTH, getToken } from '../lib/api'
import { useBrand } from '../lib/branding'

// 邀请信息（码/链接/奖励余额）
type RefInfo = {
  invite_code?: string
  invited_count?: number
  paid_count?: number
  free_token_balance?: number
  token_balance?: number
}
// 单条邀请记录（被邀请企业）
type Rec = {
  tenant_id: number
  company_name: string
  email: string
  invited_ok: boolean
  paid_ok: boolean
  paid_rewarded: boolean
  signup_reward: boolean
  registered_at: string
}

// /app 邀请推广页：展示邀请码/链接/二维码与邀请记录，依赖 /api/v1/admin/referral/info、/records、/qrcode
export default function AppReferral() {
  const brand = useBrand()
  const [info, setInfo] = useState<RefInfo | null>(null)
  const [inviteUrl, setInviteUrl] = useState('')
  const [qr, setQr] = useState('')
  const [recs, setRecs] = useState<Rec[]>([])

  useEffect(() => {
    AUTH('/api/v1/admin/referral/info').then((j: any) => {
      if (j.code === 0) { setInfo(j.data.referral || {}); setInviteUrl(j.data.invite_url || '') }
    }).catch(() => {})
    setQr('/api/v1/admin/referral/qrcode?size=240')
    AUTH('/api/v1/admin/referral/records').then((j: any) => {
      if (j.code === 0) setRecs(j.data?.list || j.data || [])
    }).catch(() => {})
  }, [])

  const copy = () => {
    const url = inviteUrl || (typeof window !== 'undefined' ? location.origin + '/register?ref=' + (info?.invite_code || '') : '')
    navigator.clipboard?.writeText(url)
  }

  return (
    <div style={{ maxWidth: 760, margin: '0 auto', padding: 16 }}>
      <h2 style={{ fontSize: 20, margin: '0 0 14px' }}>🎁 邀请推广</h2>
      <div className="bg-white rounded-lg shadow-sm p-5" style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }}>
        <h3 style={{ margin: '0 0 10px', fontSize: 16 }}>我的邀请码</h3>
        <p style={{ fontSize: 14, color: '#475569' }}>邀请码：<b style={{ fontSize: 18, color: '#4f46e5' }}>{info?.invite_code || '—'}</b></p>
        <p style={{ fontSize: 13, color: '#475569', wordBreak: 'break-all' }}>邀请链接：<a href={inviteUrl} target="_blank" rel="noreferrer">{inviteUrl || '—'}</a></p>
        <button onClick={copy} style={{ marginTop: 8, padding: '8px 14px', borderRadius: 8, border: '1px solid #cbd5e1', background: '#fff', fontSize: 13, cursor: 'pointer' }}>复制邀请链接</button>
        {qr && <div style={{ marginTop: 14 }}><img src={qr} alt="邀请二维码" style={{ width: 180, height: 180, border: '1px solid #e2e8f0', borderRadius: 8 }} /><p style={{ fontSize: 12, color: '#9ca3af', marginTop: 6 }}>扫码直达注册页（自动携带邀请码）</p></div>}
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2,1fr)', gap: 12, marginBottom: 16 }}>
        <div style={{ background: '#fff', borderRadius: 12, padding: 16, boxShadow: '0 4px 18px rgba(0,0,0,.06)' }}><p style={{ fontSize: 12, color: '#9ca3af', margin: 0 }}>已成功邀请</p><b style={{ fontSize: 20, color: '#4f46e5' }}>{info?.invited_count ?? 0} 人</b></div>
        <div style={{ background: '#fff', borderRadius: 12, padding: 16, boxShadow: '0 4px 18px rgba(0,0,0,.06)' }}><p style={{ fontSize: 12, color: '#9ca3af', margin: 0 }}>已付费好友</p><b style={{ fontSize: 20, color: '#16a34a' }}>{info?.paid_count ?? 0} 人</b></div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5" style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)' }}>
        <h3 style={{ margin: '0 0 12px', fontSize: 16 }}>邀请记录</h3>
        {recs.length === 0 && <p style={{ fontSize: 13, color: '#9ca3af' }}>暂无邀请记录</p>}
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: 13 }}>
          <thead><tr style={{ textAlign: 'left', color: '#9ca3af' }}><th style={{ padding: '6px 4px' }}>企业</th><th>邮箱</th><th>注册奖励</th><th>付费奖励</th></tr></thead>
          <tbody>
            {recs.map((r) => (
              <tr key={r.tenant_id} style={{ borderTop: '1px solid #f1f5f9' }}>
                <td style={{ padding: '8px 4px' }}>{r.company_name || '—'}</td>
                <td style={{ color: '#475569' }}>{r.email}</td>
                <td>{r.signup_reward ? '✅' : '—'}</td>
                <td>{r.paid_rewarded ? '✅' : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
