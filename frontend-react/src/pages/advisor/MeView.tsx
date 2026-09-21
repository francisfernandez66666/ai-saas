// 顾问工作台·我的视图：套餐三桶用量（本月 AI 调用 / 增量余额 / 到期日）+ 产品反馈入口
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { Button } from 'tdesign-react'
import type { Quota } from './shared'

export default function MeView({ quota, onFeedback }: { quota: Quota | null; onFeedback: () => void }) {
  return (
    <div style={{ padding: 16 }}>
      {quota && <div style={{ background: '#fff', borderRadius: 12, padding: 16, marginBottom: 16, display: 'flex', gap: 20 }}>
        <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.used_ai_calls}/{quota.max_ai_calls || '∞'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>本月AI调用</span></div>
        <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.ai_call_balance}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>增量余额</span></div>
        <div style={{ textAlign: 'center' }}><b style={{ fontSize: 18, color: 'var(--pri)' }}>{quota.expired_at ? new Date(quota.expired_at).toLocaleDateString() : '-'}</b><span style={{ fontSize: 11, color: '#718096', display: 'block' }}>到期日</span></div>
      </div>}
      <Button theme="primary" block onClick={onFeedback}>提交产品反馈</Button>
    </div>
  )
}
