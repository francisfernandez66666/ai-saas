// 客户详情·跨会话历史时间线（P1-9 零 UI 补齐 2026-09-20）：
// GET /conversations/:id/messages 此前前端零消费者，历史会话内容无处可查；点行展开该会话全量消息（后端 ASC，≤200 条）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
import { SENDER_LABELS, fmtShort, type ConvBrief } from './shared'
import type { Msg } from '../../types'

// CONV_STATUS_KEYS 会话状态码 → i18n key（未知码回退到 common.none）
const CONV_STATUS_KEYS: Record<string, I18nKey> = { active: 'common.active', closed: 'advisor.tlEnded' }

// HistoryTimeline 顾问工作台「历史会话时间线」：按会话展开/收起消息记录。
export default function HistoryTimeline({ conversations, tlConv, tlMsgs, onToggle }: {
  conversations: ConvBrief[]
  tlConv: number | null
  tlMsgs: Msg[]
  onToggle: (cid: number) => void
}) {
  const { t } = useI18n()
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <b style={{ fontSize: 13 }}>{t('advisor.tlTitle')}</b>
      {conversations.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>{t('advisor.tlEmpty')}</div>}
      {conversations.map((cv) => (
        <div key={cv.id}>
          <div onClick={() => onToggle(cv.id)} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0', display: 'flex', gap: 8, alignItems: 'center', cursor: 'pointer' }}>
            <span style={{ flex: 1 }}>{t('advisor.tlConvPrefix')} #{cv.id}
              <span style={{ color: '#a0aec0' }}> · {cv.channel || 'web'} · {t(CONV_STATUS_KEYS[cv.status || ''] || 'common.none')}</span>
              {cv.last_message_at ? <span style={{ color: '#a0aec0' }}> · {fmtShort(cv.last_message_at)}</span> : null}
            </span>
            <span style={{ color: 'var(--pri)', fontSize: 12 }}>{tlConv === cv.id ? t('common.collapse') : t('common.expand')}</span>
          </div>
          {tlConv === cv.id && (
            <div style={{ margin: '6px 0 2px', padding: '8px 10px', background: '#f8fafc', borderRadius: 8, maxHeight: 240, overflowY: 'auto' }}>
              {tlMsgs.length === 0 && <span style={{ color: '#a0aec0', fontSize: 12 }}>{t('advisor.tlNoMsg')}</span>}
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
