// 客户详情·满意度评分卡（P1-9）：POST /feedback/rating，登录态按 customer_id 记录 1-5 分 + 评语
// 后端限同人同客户 5 次/天，超限 429 由请求层统一提示
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { Button, Textarea } from 'tdesign-react'

export default function RatingCard({ rate, onRate, onSubmit }: {
  rate: { score: number; comment: string }
  onRate: (r: { score: number; comment: string }) => void
  onSubmit: () => void
}) {
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <b style={{ fontSize: 13 }}>满意度评分</b>
      <div style={{ display: 'flex', gap: 6, marginTop: 8, alignItems: 'center' }}>
        {[1, 2, 3, 4, 5].map((n) => (
          <button key={n} onClick={() => onRate({ ...rate, score: n })} aria-label={n + ' 分'}
            style={{ border: 'none', background: 'none', cursor: 'pointer', fontSize: 20, color: n <= rate.score ? '#f59e0b' : '#d1d5db' }}>★</button>
        ))}
        <span style={{ color: '#a0aec0', fontSize: 12 }}>{rate.score ? rate.score + ' 分' : '未评分'}</span>
      </div>
      <Textarea value={rate.comment} onChange={(v) => onRate({ ...rate, comment: v })} placeholder="评语（可选）" autosize={{ minRows: 2 }} style={{ marginTop: 8 }} />
      <Button size="small" theme="primary" variant="outline" onClick={onSubmit} style={{ marginTop: 8 }}>提交评分</Button>
    </div>
  )
}
