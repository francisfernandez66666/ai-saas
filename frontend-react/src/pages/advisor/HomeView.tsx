// 顾问工作台·首页视图：统计卡片 + 状态筛选页签 + 客户列表
// 从原 Advisor.tsx 原样搬迁（C2 拆分），行为与 JSX 未改，仅把 openDetail/setStatus 改为 props 回调
import { Tag } from 'tdesign-react'
import { Cust } from '../../types'
import { H, HI, STAGE_COLORS, STAGE_LABELS, TABS, fmtShort, type Stat } from './shared'

export default function HomeView({ stats, list, status, onStatus, onOpen }: {
  stats: Stat[]
  list: Cust[]
  status: string
  onStatus: (k: string) => void
  onOpen: (id: number) => void
}) {
  return (
    <div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(3,1fr)', gap: 8, padding: 12 }}>
        {stats.map((s, i) => <div key={i} style={{ background: '#fff', borderRadius: 8, border: '1px solid #f0f0f0', padding: 10, textAlign: 'center' }}><p style={{ fontSize: 20, fontWeight: 700 }}>{s.value}</p><p style={{ fontSize: 11, color: '#718096', marginTop: 2 }}>{s.label}</p></div>)}
      </div>
      <div style={{ display: 'flex', gap: 6, padding: '0 12px 10px', overflowX: 'auto' }}>
        {TABS.map((t) => <button key={t.k} onClick={() => onStatus(t.k)} style={{ whiteSpace: 'nowrap', padding: '5px 12px', borderRadius: 16, fontSize: 13, border: 'none', background: status === t.k ? 'var(--pri)' : '#fff', color: status === t.k ? '#fff' : '#718096' }}>{t.t}</button>)}
      </div>
      <div style={{ background: '#fff' }}>
        {list.length === 0 && <div style={{ textAlign: 'center', color: '#a0aec0', padding: 40, fontSize: 13 }}>暂无客户</div>}
        {list.map((l) => (
          <div key={l.id} onClick={() => onOpen(l.id)} style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '12px 16px', cursor: 'pointer', borderBottom: '1px solid #f0f0f0' }}>
            <div style={{ width: 40, height: 40, background: '#f0f0f0', borderRadius: 20, display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0, color: '#718096', fontWeight: 600 }}>{HI(l.name)}</div>
            <div style={{ flex: 1, minWidth: 0 }}>
              <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
                <span style={{ fontSize: 14, fontWeight: 500 }}>{H(l.name)}</span>
                <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: (STAGE_COLORS[l.journey_stage || ''] || 'bg-gray-100') + ' ' + (STAGE_COLORS[l.journey_stage || ''] ? '' : 'text-gray-500') }}>{STAGE_LABELS[l.journey_stage || ''] || l.journey_stage || '-'}</span>
                {l.conv_mode === 'human' && <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#dbeafe', color: '#1d4ed8' }}>人工</span>}
                {/* §八-6 C 块：待接管徽标——列表接口当前未下发该列，仅在数据真带 pending_handoff=true 时渲染（勿造字段） */}
                {l.pending_handoff === true && <Tag size="small" theme="warning">待接管</Tag>}
              </div>
              <p style={{ fontSize: 12, color: '#a0aec0', marginTop: 2, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{l.last_message || '暂无消息'}</p>
            </div>
            <span style={{ fontSize: 11, color: '#cbd5e0' }}>{fmtShort(l.updated_at)}</span>
          </div>
        ))}
      </div>
    </div>
  )
}
