/**
 * AppReferral.tsx：移动端邀请推广页
 * 展示邀请码/链接/二维码与邀请记录
 * 依赖接口：/api/v1/admin/referral/info、/records、/qrcode
 */
import { useState, useEffect } from 'react'
import { AUTH, getToken } from '../lib/api'
import { useBrand } from '../lib/branding'
import type { ApiResp, ReferralInfo, ReferralRecord } from '../types'

// 邀请信息响应 data 类型（admin/referral/info）
type RefInfoResp = { referral: ReferralInfo; invite_url?: string }
// 邀请记录列表响应 data 类型（admin/referral/records）
type RecListResp = { list: ReferralRecord[] }

/**
 * /app 邀请推广页组件
 * 1. 展示邀请码与邀请链接（支持一键复制）
 * 2. 展示邀请二维码（扫码直达注册页，自动携带邀请码）
 * 3. 统计已邀请人数与已付费好友数
 * 4. 展示邀请记录列表（企业、邮箱、奖励状态）
 */
export default function AppReferral() {
  const brand = useBrand()
  // 邀请信息（邀请码、邀请人数等）
  const [info, setInfo] = useState<ReferralInfo | null>(null)
  // 邀请链接
  const [inviteUrl, setInviteUrl] = useState('')
  // 二维码图片 URL
  const [qr, setQr] = useState('')
  // 邀请记录列表
  const [recs, setRecs] = useState<ReferralRecord[]>([])

  useEffect(() => {
    // 加载邀请信息（邀请码、邀请人数等）
    AUTH<ApiResp<RefInfoResp>>('/api/v1/admin/referral/info').then((j) => {
      if (j.code === 0) { setInfo(j.data.referral || {}); setInviteUrl(j.data.invite_url || '') }
    }).catch(() => {})
    // 二维码图片地址（直接使用接口 URL，浏览器会自动带上 cookie）
    setQr('/api/v1/admin/referral/qrcode?size=240')
    // 加载邀请记录列表
    AUTH<ApiResp<RecListResp>>('/api/v1/admin/referral/records').then((j) => {
      if (j.code === 0) setRecs(j.data?.list || [])
    }).catch(() => {})
  }, [])

  /**
   * 复制邀请链接到剪贴板
   * 优先用后端返回的链接，否则本地拼装（origin + /register?ref=）
   */
  const copy = () => {
    const url = inviteUrl || (typeof window !== 'undefined' ? location.origin + '/register?ref=' + (info?.invite_code || '') : '')
    navigator.clipboard?.writeText(url)
  }

  return (
    <div style={{ maxWidth: 760, margin: '0 auto', padding: 16 }}>
      <h2 style={{ fontSize: 20, margin: '0 0 14px' }}>🎁 邀请推广</h2>
      {/* 邀请码与链接卡片 */}
      <div className="bg-white rounded-lg shadow-sm p-5" style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }}>
        <h3 style={{ margin: '0 0 10px', fontSize: 16 }}>我的邀请码</h3>
        <p style={{ fontSize: 14, color: '#475569' }}>邀请码：<b style={{ fontSize: 18, color: '#4f46e5' }}>{info?.invite_code || '—'}</b></p>
        <p style={{ fontSize: 13, color: '#475569', wordBreak: 'break-all' }}>邀请链接：<a href={inviteUrl} target="_blank" rel="noreferrer">{inviteUrl || '—'}</a></p>
        <button onClick={copy} style={{ marginTop: 8, padding: '8px 14px', borderRadius: 8, border: '1px solid #cbd5e1', background: '#fff', fontSize: 13, cursor: 'pointer' }}>复制邀请链接</button>
        {/* 邀请二维码：扫码直达注册页，自动携带邀请码 */}
        {qr && <div style={{ marginTop: 14 }}><img src={qr} alt="邀请二维码" style={{ width: 180, height: 180, border: '1px solid #e2e8f0', borderRadius: 8 }} /><p style={{ fontSize: 12, color: '#9ca3af', marginTop: 6 }}>扫码直达注册页（自动携带邀请码）</p></div>}
      </div>

      {/* 统计卡片：已邀请人数 + 已付费好友数 */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2,1fr)', gap: 12, marginBottom: 16 }}>
        <div style={{ background: '#fff', borderRadius: 12, padding: 16, boxShadow: '0 4px 18px rgba(0,0,0,.06)' }}><p style={{ fontSize: 12, color: '#9ca3af', margin: 0 }}>已成功邀请</p><b style={{ fontSize: 20, color: '#4f46e5' }}>{info?.invited_count ?? 0} 人</b></div>
        <div style={{ background: '#fff', borderRadius: 12, padding: 16, boxShadow: '0 4px 18px rgba(0,0,0,.06)' }}><p style={{ fontSize: 12, color: '#9ca3af', margin: 0 }}>已付费好友</p><b style={{ fontSize: 20, color: '#16a34a' }}>{info?.paid_count ?? 0} 人</b></div>
      </div>

      {/* 邀请记录列表 */}
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
