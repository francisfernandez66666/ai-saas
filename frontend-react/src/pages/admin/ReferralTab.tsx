// 邀请推广 Tab（F1 从 Admin.tsx 拆出）：邀请码、链接、二维码与余额概览。
import { useEffect, useState } from 'react'
import { authHeaders, getToken } from '../../lib/api'
import type { TableRowData } from '../../types'

/** 邀请推广 Tab：展示邀请码、奖励规则和推荐记录。 */
export function ReferralTab() {
  const [info, setInfo] = useState<TableRowData | null>(null)
  const [qr, setQr] = useState('')
  // E6 修复(2026-09-14)：加载失败给错误态而非永久"加载中"
  const [err, setErr] = useState('')
  // 拉取当前用户的邀请奖励信息（E6：非 0 码/网络异常落错误态，不再永久"加载中"）
  const load = () => {
    setErr('')
    fetch('/api/v1/advisor/referral/info', { headers: authHeaders() }).then((r) => r.json()).then((j) => { if (j?.code === 0) setInfo(j.data); else setErr(j?.message || '加载失败') }).catch(() => setErr('网络异常，加载失败'))
    ;(async () => {
      try {
        const res = await fetch('/api/v1/advisor/referral/qrcode?size=280', {
          headers: getToken() ? authHeaders() : {},
        })
        if (!res.ok) return
        const blob = await res.blob()
        setQr(URL.createObjectURL(blob))
      } catch { /* 二维码加载失败静默 */ }
    })()
  }
  useEffect(() => { load() }, [])
  if (err) return <p style={{ color: '#e53e3e', padding: 40, textAlign: 'center' }}>{err}　<button onClick={load} style={{ background: 'none', border: '1px solid #e53e3e', borderRadius: 6, padding: '2px 10px', cursor: 'pointer' }}>重试</button></p>
  if (!info) return <p style={{ color: '#9ca3af', padding: 40, textAlign: 'center' }}>加载中...</p>
  const r = info.referral || {}
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-6">
        <h3 style={{ fontSize: 16, fontWeight: 600 }}>邀请推广</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>邀请码：<b>{r.invite_code}</b>　邀请链接：<a href={info.invite_url} target="_blank" rel="noreferrer">{info.invite_url}</a></p>
      </div>
      <div className="bg-white rounded-lg shadow-sm p-6 mt-4 grid grid-cols-2 md:grid-cols-4 gap-4">
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>已成功邀请</p><b style={{ fontSize: 20, color: '#4f46e5' }}>{r.invited_count} 人</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>已付费好友</p><b style={{ fontSize: 20, color: '#16a34a' }}>{r.paid_count} 人</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>免费体验桶余额</p><b style={{ fontSize: 20, color: '#f59e0b' }}>{r.free_token_balance}</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>永久token余额</p><b style={{ fontSize: 20, color: '#10b981' }}>{r.token_balance}</b></div>
      </div>
      {qr && <div className="bg-white rounded-lg shadow-sm p-6 mt-4 inline-block"><img src={qr} alt="邀请二维码" /><p style={{ fontSize: 12, color: '#9ca3af', marginTop: 8 }}>扫码直达注册页（已自动携带邀请码）</p></div>}
    </div>
  )
}
