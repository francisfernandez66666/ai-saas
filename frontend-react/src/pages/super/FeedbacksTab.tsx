// 超管·用户反馈视图：状态/类型双筛选 + 反馈表（C6 前端异常也进入这里）
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Button, Select, Table } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { FB_TYPES, Section, esc, type Fb } from './shared'

// FeedbacksTab 平台后台「反馈处理」页：按状态与对象筛选并处理用户反馈。
export default function FeedbacksTab({ fbs, status, onStatus, target, onTarget, onResolve }: {
  fbs: Fb[]
  status: string
  onStatus: (v: string) => void
  target: string
  onTarget: (v: string) => void
  onResolve: (id: number) => void
}) {
  const { t } = useI18n()
  // 用户反馈列定义（租户/提交人/类型/意见/标记处理）
  const fbCols = [
    { colKey: 'created_at', title: t('common.time'), width: 140, cell: (p: CellProps) => (p.row.created_at || '').replace('T', ' ').slice(0, 16) },
    { colKey: 'tenant', title: t('common.tenant'), width: 160, cell: (p: CellProps) => `#${p.row.tenant_id} ${esc(p.row.tenant_name)}` },
    { colKey: 'username', title: t('super.fb.colSubmitter'), width: 100, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'target_type', title: t('common.type'), width: 90, cell: (p: CellProps) => { const k = FB_TYPES[p.row.target_type]; return k ? t(k) : p.row.target_type } },
    { colKey: 'content', title: t('super.fb.colContent'), width: 220, ellipsis: true },
    { colKey: 'op', title: t('common.op'), width: 100, cell: (p: CellProps) => p.row.status === 'open' ? <Button size="small" theme="success" onClick={() => onResolve(p.row.id)}>{t('super.fb.markResolve')}</Button> : <span style={{ fontSize: 12, color: '#718096' }}>{t('super.fb.resolved')}</span> },
  ]

  return (
    <Section title={t('super.fb.title')} desc={t('super.fb.desc')}>
      <div style={{ marginBottom: 12, display: 'flex', gap: 10, flexWrap: 'wrap' }}>
        <Select value={status} onChange={(v) => onStatus(v as string)} options={[{ label: t('common.pending'), value: 'open' }, { label: t('super.fb.resolved'), value: 'resolved' }, { label: t('common.all'), value: '' }]} style={{ width: 160 }} />
        <Select value={target} onChange={(v) => onTarget(v as string)} options={[{ label: t('super.fb.optAllType'), value: '' }, { label: t('super.shared.fbAiReply'), value: 'ai_reply' }, { label: t('super.shared.fbFeature'), value: 'feature' }, { label: t('super.shared.fbRating'), value: 'rating' }, { label: t('super.shared.fbClientError'), value: 'client_error' }, { label: t('super.shared.fbOther'), value: 'other' }]} style={{ width: 160 }} />
      </div>
      <Table rowKey="id" data={fbs} columns={fbCols} size="small" empty={t('super.fb.empty')} />
    </Section>
  )
}
