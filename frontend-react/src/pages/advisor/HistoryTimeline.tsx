// 客户详情·跨会话历史时间线（P1-9 零 UI 补齐 2026-09-20）：
// GET /conversations/:id/messages 此前前端零消费者，历史会话内容无处可查；点行展开该会话全量消息（后端 ASC，≤200 条）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { SENDER_LABELS, fmtShort, type ConvBrief } from './shared'
import type { Msg } from '../../types'

// CONV_STATUS_LABELS 会话状态码 → 中文（未知码原样呈现）
const CONV_STATUS_LABELS: Record<string, string> = { active: '进行中', closed: '已结束' }

// HistoryTimeline 顾问工作台「历史会话时间线」：按会话展开/收起消息记录。
export default function HistoryTimeline({ conversations, tlConv, tlMsgs, onToggle }: {
  conversations: ConvBrief[]
  tlConv: number | null
  tlMsgs: Msg[]
  onToggle: (cid: number) => void
}) {
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <b style={{ fontSize: 13 }}>历史会话</b>
      {conversations.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>暂无历史会话</div>}
      {conversations.map((cv) => (
        <div key={cv.id}>
          <div onClick={() => onToggle(cv.id)} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0', display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <span style={{ flex: 1 }}>会话 #{cv.id}
              <span style={{ color: '#a0aec0' }}> · {cv.channel || 'web'} · {CONV_STATUS_LABELS[cv.status || ''] || cv.status || '-'}</span>
              {cv.last_message_at ? <span style={{ color: '#a0aec0' }}> · {fmtShort(cv.last_message_at)}</span> : null}
            </span>
            <span style={{ color: 'var(--pri)', fontSize: 12 }}>{tlConv === cv.id ? '收起' : '展开'}</span>
          </div>
          {tlConv === cv.id && (
            <div style={{ margin: '6px 0 2px', padding: '8px 10px', background: '#f8fafc', borderRadius: 8, maxHeight: 240, overflowY: 'auto' }}>
              {tlMsgs.length === 0 && <span style={{ color: '#a0aec0', fontSize: 12 }}>无消息</span>}
              {tlMsgs.map((m, i) => (
                <div key={i} style={{ fontSize: 12, marginBottom: 4 }}>
                  <span style={{ color: m.sender_type === 'human' ? '#4338ca' : m.sender_type === 'ai' ? '#047857' : '#64748b' }}>
                    [{SENDER_LABELS[m.sender_type] || m.sender_type}]
                  </span>{' '}{m.content}
                </div>
              ))}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
