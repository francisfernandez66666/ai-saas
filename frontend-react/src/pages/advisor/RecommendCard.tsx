// 客户详情·AI 策略推荐卡（E2）：意向分 + 紧迫度 + 前 2 条推荐话术，可一键填入底部输入框
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
import { URGENCY_LABELS, type Recommend } from './shared'

export default function RecommendCard({ rec, onFill }: { rec: Recommend | null; onFill: (s: string) => void }) {
  if (!rec || (rec.recommends || []).length === 0) return null
  return (
    <div style={{ background: 'linear-gradient(135deg,#eef2ff,#faf5ff)', border: '1px solid #e0e7ff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 6 }}>
        <b style={{ fontSize: 13 }}>策略推荐</b>
        <span style={{ fontSize: 11, color: '#4338ca' }}>意向 {(Number(rec.intent_score) * 100).toFixed(0)}%{rec.urgency_level ? ` · ${URGENCY_LABELS[rec.urgency_level] || rec.urgency_level}` : ''}</span>
      </div>
      {(rec.recommends || []).slice(0, 2).map((r, i) => (
        <div key={i} style={{ background: '#fff', borderRadius: 8, padding: '8px 10px', marginTop: 6, display: 'flex', gap: 8, alignItems: 'flex-start' }}>
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 11, color: '#7c3aed' }}>{r.anchor_name || '话术'} · {r.template_name}</div>
            <div style={{ marginTop: 2, color: '#374151' }}>{(r.prompt_template || '').slice(0, 80)}</div>
          </div>
          <button aria-label="填入输入框" onClick={() => onFill(r.prompt_template || '')} style={{ flexShrink: 0, background: 'var(--pri)', color: '#fff', border: 'none', borderRadius: 6, padding: '4px 8px', fontSize: 11, cursor: 'pointer' }}>填入</button>
        </div>
      ))}
    </div>
  )
}
