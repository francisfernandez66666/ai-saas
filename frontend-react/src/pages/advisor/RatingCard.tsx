// 客户详情·满意度评分卡（P1-9）：POST /feedback/rating，登录态按 customer_id 记录 1-5 分 + 评语
// 后端限同人同客户 5 次/天，超限 429 由请求层统一提示
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { useI18n } from '../../i18n/useI18n'
import { Button, Textarea } from 'tdesign-react'

// RatingCard 顾问工作台「服务评分」卡片：采集评分与评语并提交。
export default function RatingCard({ rate, onRate, onSubmit }: {
  rate: { score: number; comment: string }
  onRate: (r: { score: number; comment: string }) => void
  onSubmit: () => void
}) {
  const { t } = useI18n()
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <b style={{ fontSize: 13 }}>{t('advisor.ratingTitle')}</b>
      <div style={{ display: 'flex', gap: 6, marginTop: 8, alignItems: 'center' }}>
        {[1, 2, 3, 4, 5].map((n) => (
          <button key={n} onClick={() => onRate({ ...rate, score: n })} aria-label={t('advisor.ratingScore', { n })}
            style={{ border: 'none', background: 'none', cursor: 'pointer', fontSize: 20, color: n <= rate.score ? '#f59e0b' : '#d1d5db' }}>★</button>
        ))}
        <span style={{ color: '#a0aec0', fontSize: 12 }}>{rate.score ? t('advisor.ratingScore', { n: rate.score }) : t('advisor.ratingUnrated')}</span>
      </div>
      <Textarea value={rate.comment} onChange={(v) => onRate({ ...rate, comment: v })} placeholder={t('advisor.ratingComment')} autosize={{ minRows: 2 }} style={{ marginTop: 8 }} />
      <Button size="small" theme="primary" variant="outline" onClick={onSubmit} style={{ marginTop: 8 }}>{t('advisor.ratingSubmit')}</Button>
    </div>
  )
}
