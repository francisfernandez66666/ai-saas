// 平台健康监控 Tab（F3，2026-09-14）：/super/monitor/health 可视化。
// 此前健康快照（ComputeHealth）仅 /status JSON 与 Prometheus 文本，超管后台无渲染页；
// 补此页把 DB/合并队列/24h 严重事件/goroutine/实例协调/死信积压等探针按 ok/warn/crit 分级呈现。
// P1-7 迁移(2026-09-20 审计批)：裸 fetch → lib/api AUTH（网络异常归一 code:-1 信封，不再抛错）；P2-14 文案迁移至 i18n
import { useCallback, useEffect, useState } from 'react'
import { Button, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'

// HealthCheck 单个健康探针：名称、ok/warn/crit 状态、当前值与告警/危急阈值说明。
type HealthCheck = { name: string; status: 'ok' | 'warn' | 'crit'; value: string; warn_at: string; crit_at: string; desc: string }
// Snapshot /monitor/health 的健康快照：数据库状态、全部探针与是否存在 warn/crit 的汇总位。
type Snapshot = { db_ok: boolean; checks: HealthCheck[]; has_crit: boolean; has_warn: boolean }

const THEME: Record<string, 'success' | 'warning' | 'danger'> = { ok: 'success', warn: 'warning', crit: 'danger' }
const STATUS_LABEL: Record<string, I18nKey> = { ok: 'super.monitor.stOk', warn: 'super.monitor.stWarn', crit: 'super.monitor.stCrit' }

/** 平台健康监控：结构化探针 + 阈值分级 + 主动刷新。 */
export function MonitorTab() {
  const { t } = useI18n()
  const [snap, setSnap] = useState<Snapshot | null>(null)
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)
  const load = useCallback(async () => {
    setLoading(true)
    setErr('')
    // AUTH 内部已兜网络异常（code:-1），此处无需 try/catch
    const j = await AUTH('/api/v1/super/monitor/health')
    if (j?.code === 0) setSnap(j.data)
    else setErr(j?.message || t('common.loadFailed'))
    setLoading(false)
  }, [t])
  useEffect(() => { load() }, [load])
  // 30s 自动刷新（低频哨兵）
  useEffect(() => {
    const timer = setInterval(load, 30000)
    return () => clearInterval(timer)
  }, [load])

  const overall = snap?.has_crit ? 'crit' : snap?.has_warn ? 'warn' : 'ok'
  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16 }}>
        <h2 style={{ margin: 0 }}>{t('super.monitor.title')}</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {snap && <Tag theme={THEME[overall]} size="large">{t('super.monitor.overall', { status: t(STATUS_LABEL[overall]) })}</Tag>}
          {snap && <span style={{ fontSize: 13, color: '#718096' }}>DB {snap.db_ok ? t('super.monitor.dbOk') : t('super.monitor.dbDown')}</span>}
          <Button theme="primary" onClick={load} loading={loading}>{t('super.monitor.refresh')}</Button>
        </div>
      </div>
      {err && <p style={{ color: '#e53e3e' }}>{err}</p>}
      {!snap && !err && <p style={{ color: '#718096' }}>{t('common.loading')}</p>}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: 12 }}>
        {(snap?.checks || []).map((c) => (
          <div key={c.name} style={{ background: '#fff', borderRadius: 8, boxShadow: '0 1px 3px rgba(0,0,0,.08)', padding: 14, borderLeft: `4px solid var(--pri)` }}>
            <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
              <span style={{ fontWeight: 600 }}>{c.name}</span>
              <Tag theme={THEME[c.status] || 'default'}>{STATUS_LABEL[c.status] ? t(STATUS_LABEL[c.status]) : c.status}</Tag>
            </div>
            <div style={{ fontSize: 22, fontWeight: 700, margin: '6px 0' }}>{c.value}</div>
            <div style={{ fontSize: 12, color: '#4a5568' }}>{c.desc}</div>
            <div style={{ fontSize: 11, color: '#a0aec0', marginTop: 4 }}>{t('super.monitor.threshold', { warn: c.warn_at, crit: c.crit_at })}</div>
          </div>
        ))}
      </div>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>{t('super.monitor.footNote')}</p>
    </div>
  )
}
