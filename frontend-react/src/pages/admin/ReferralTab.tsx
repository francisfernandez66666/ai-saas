// 邀请推广 Tab（F1 从 Admin.tsx 拆出）：邀请码、链接、二维码与余额概览。
// P1-7 迁移(2026-09-20 审计批)：信息接口走 AUTH；二维码是图片 blob 不能走 JSON 层，
// 改走 apiFetch（保留原始 Response，统一获得超时/鉴权头/401 处理）。
import { useEffect, useState } from 'react'
import { AUTH, apiFetch } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import type { TableRowData } from '../../types'

/** 邀请推广 Tab：展示邀请码、奖励规则和推荐记录。 */
export function ReferralTab() {
  const { t } = useI18n()
  const [info, setInfo] = useState<TableRowData | null>(null)
  const [qr, setQr] = useState('')
  // E6 修复(2026-09-14)：加载失败给错误态而非永久"加载中"
  const [err, setErr] = useState('')
  // 拉取当前用户的邀请奖励信息（E6：非 0 码/网络异常落错误态，不再永久"加载中"；AUTH 网络异常归一 code:-1）
  const load = () => {
    setErr('')
    void AUTH('/api/v1/advisor/referral/info').then((j) => { if (j?.code === 0) setInfo(j.data); else setErr(j?.message || t('common.loadFailed')) })
    ;(async () => {
      try {
        const res = await apiFetch('/api/v1/advisor/referral/qrcode?size=280')
        if (!res.ok) return
        const blob = await res.blob()
        setQr(URL.createObjectURL(blob))
      } catch { /* 二维码加载失败静默 */ }
    })()
  }
  useEffect(() => { load() }, [])
  if (err) return <p style={{ color: '#e53e3e', padding: 40, textAlign: 'center' }}>{err}　<button onClick={load} style={{ background: 'none', border: '1px solid #e53e3e', borderRadius: 6, padding: '2px 10px', cursor: 'pointer' }}>{t('common.retry')}</button></p>
  if (!info) return <p style={{ color: '#9ca3af', padding: 40, textAlign: 'center' }}>{t('common.loading')}</p>
  const r = info.referral || {}
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-6">
        <h3 style={{ fontSize: 16, fontWeight: 600 }}>{t('admin.referral.title')}</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>{t('admin.referral.inviteCode')}：<b>{r.invite_code}</b>　{t('admin.referral.inviteLink')}：<a href={info.invite_url} target="_blank" rel="noreferrer">{info.invite_url}</a></p>
      </div>
      <div className="bg-white rounded-lg shadow-sm p-6 mt-4 grid grid-cols-2 md:grid-cols-4 gap-4">
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>{t('admin.referral.invitedLabel')}</p><b style={{ fontSize: 20, color: '#4f46e5' }}>{t('admin.referral.peopleCount', { n: r.invited_count })}</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>{t('admin.referral.paidLabel')}</p><b style={{ fontSize: 20, color: '#16a34a' }}>{t('admin.referral.peopleCount', { n: r.paid_count })}</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>{t('admin.referral.freeBucket')}</p><b style={{ fontSize: 20, color: '#f59e0b' }}>{r.free_token_balance}</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>{t('admin.referral.tokenBalance')}</p><b style={{ fontSize: 20, color: '#10b981' }}>{r.token_balance}</b></div>
      </div>
      {qr && <div className="bg-white rounded-lg shadow-sm p-6 mt-4 inline-block"><img src={qr} alt={t('admin.referral.qrAlt')} /><p style={{ fontSize: 12, color: '#9ca3af', marginTop: 8 }}>{t('admin.referral.qrCaption')}</p></div>}
    </div>
  )
}
