// 顾问工作台·跟进视图：今日待跟进 + 逾期跟进两个分区（按 next_follow_at 与 now 比较切分）
// 从原 Advisor.tsx 原样搬迁（C2 拆分），FU 条目组件一并收在此文件（仅本视图使用）
import { H, fmtShort, type Followup } from './shared'

export default function FollowupView({ followups, onOpen }: { followups: Followup[]; onOpen: (id: number) => void }) {
  return (
    <div style={{ padding: 12 }}>
      <h3 style={{ fontSize: 14, margin: '8px 0' }}>今日待跟进</h3>
      {followups.filter((f) => f.next_follow_at && new Date(f.next_follow_at) >= new Date()).map((f) => <FU key={'t' + f.customer_id} f={f} onClick={() => onOpen(f.customer_id)} />)}
      <h3 style={{ fontSize: 14, margin: '16px 0 8px' }}>逾期跟进</h3>
      {followups.filter((f) => f.next_follow_at && new Date(f.next_follow_at) < new Date()).map((f) => <FU key={'o' + f.customer_id} f={f} onClick={() => onOpen(f.customer_id)} />)}
      {followups.length === 0 && <p style={{ color: '#a0aec0', fontSize: 13 }}>暂无跟进提醒</p>}
    </div>
  )
}

// FU 跟进提醒条目：头像 + 客户名 + 跟进内容 + 下次跟进时间
function FU({ f, onClick }: { f: Followup; onClick: () => void }) {
  return (<div onClick={onClick} style={{ background: '#fff', borderRadius: 8, border: '1px solid #f0f0f0', padding: 12, marginBottom: 8, display: 'flex', gap: 10, cursor: 'pointer', alignItems: 'center' }}>
    <div style={{ width: 32, height: 32, background: '#f0f0f0', borderRadius: 16, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11 }}>{H(f.customer_name)[0]}</div>
    <div style={{ flex: 1, minWidth: 0 }}><p style={{ fontSize: 13, fontWeight: 500 }}>{H(f.customer_name)}</p><p style={{ fontSize: 12, color: '#a0aec0', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{f.content || '跟进提醒'}</p></div>
    <span style={{ fontSize: 11, color: '#cbd5e0' }}>{fmtShort(f.next_follow_at)}</span>
  </div>)
}
