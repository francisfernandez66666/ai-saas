// 素材审核 Tab（P1-9 零UI补齐批 2026-09-20）：KB 反馈素材池的超管人工评审落点。
// 后端三端点自 §素材采集起一直无前端消费者（超管台缺 Tab），此处补齐：
//   GET  /api/v1/super/materials?status=&source=&page=&page_size=（跨租户素材池分页）
//   POST /api/v1/super/materials/:id/review {status, human_score?, pack_code?, deal_closed?}（approved/rejected/pending）
//   POST /api/v1/super/materials/:id/evals（触发 AI 0-5 评分，回写 ai_score/ai_eval_note）
// 仅 super_admin（SuperRequired），平台路径无需 X-Tenant-ID。
import { useCallback, useEffect, useState } from 'react'
import { Button, Select, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, TableRowData } from '../../types'

// MaterialRow 素材行（对齐 model.KbFeedbackMaterial json 列；human_score/ai_score 指针列可为 null）
type MaterialRow = {
  id: number; tenant_id: number; conversation_id: number; message_id: number
  source: string; content: string; status: string
  ai_score: number | null; ai_eval_note?: string
  human_score: number | null; deal_closed?: boolean
  pack_code?: string; created_at: string
}
// ListResp 素材池分页列表接口的响应信封：data 为素材行数组加分页信息。
type ListResp = { code: number; data?: { list: MaterialRow[]; total: number; page: number; page_size: number } }
// EvalResp AI 评分接口的响应信封：data 为本次评分（0-5 分）与评语。
type EvalResp = { code: number; message?: string; data?: { score: number; note: string } }

// 状态 → 中文/配色（pending 待处理、approved 已通过、rejected 已拒绝）。
// 模块级拿不到 t()，只存「后端值 → I18nKey」，渲染时再 t()。
const STATUS_KEYS: Record<string, { label: I18nKey; theme: 'warning' | 'success' | 'danger' }> = {
  pending: { label: 'super.material.stPending', theme: 'warning' },
  approved: { label: 'super.material.stApproved', theme: 'success' },
  rejected: { label: 'super.material.stRejected', theme: 'danger' },
}

/** 素材审核：跨租户 KB 反馈素材池，人工评审（通过/拒绝/待处理 + 1-5 分）与 AI 评分触发。 */
export function MaterialsTab() {
  const { t } = useI18n()
  const [rows, setRows] = useState<MaterialRow[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('pending')
  const [source, setSource] = useState('')
  const [loading, setLoading] = useState(false)
  // 正在评审/评分的素材 id（按钮置灰防连点；AI 评分要等大模型返回）
  const [busyId, setBusyId] = useState<number | null>(null)

  // 人工分下拉选项（label 需 t()，模块级常量无法调用 t，故在渲染期构建）
  const scoreOpts = [1, 2, 3, 4, 5].map((n) => ({ label: t('super.material.scoreUnit', { n }), value: n }))

  const load = useCallback(async () => {
    setLoading(true)
    const q = new URLSearchParams({ page: String(page), page_size: '20' })
    if (status) q.set('status', status)
    if (source) q.set('source', source)
    const j = await AUTH<ListResp>('/api/v1/super/materials?' + q)
    if (j?.code === 0 && j.data) { setRows(j.data.list || []); setTotal(j.data.total || 0) }
    setLoading(false)
  }, [page, status, source])
  useEffect(() => { load() }, [load])

  // 评审：改状态/人工分统一走 review 端点（status 必带——提交当前态即只更新分数）
  async function review(r: MaterialRow, body: Record<string, unknown>) {
    setBusyId(r.id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/materials/${r.id}/review`, { method: 'POST', body })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.material.updated')); load() }
  }
  // AI 评分：调 evals 阶段模型打 0-5 分并回写，成功后直接展示本次返回分/理由
  async function evals(r: MaterialRow) {
    setBusyId(r.id)
    const j = await AUTH<EvalResp>(`/api/v1/super/materials/${r.id}/evals`, { method: 'POST', timeoutMs: 60000 })
    setBusyId(null)
    if (j?.code === 0) {
      MessagePlugin.success(t('super.material.aiScoreNote', { score: j.data?.score ?? '-', note: j.data?.note || '' }))
      load()
    }
  }

  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'tenant_id', title: t('common.tenant'), width: 80, cell: (p: CellProps) => '#' + p.row.tenant_id },
    { colKey: 'source', title: t('common.source'), width: 80, cell: (p: CellProps) => <Tag variant="light">{p.row.source === 'human' ? t('super.material.srcHuman') : p.row.source === 'ai' ? t('super.material.srcAi') : p.row.source}</Tag> },
    { colKey: 'content', title: t('super.material.colContent'), width: 320, ellipsis: true },
    { colKey: 'ai_score', title: t('super.material.colAiScore'), width: 80, cell: (p: CellProps) => p.row.ai_score == null ? '-' : String(p.row.ai_score) },
    { colKey: 'ai_eval_note', title: t('super.material.colAiReason'), width: 200, ellipsis: true, cell: (p: CellProps) => p.row.ai_eval_note || '-' },
    {
      colKey: 'human_score', title: t('super.material.colHumanScore'), width: 120, cell: (p: CellProps) => (
        // review 端点只在 human_score 非空时更新该列（无清除语义），故 clearable 置空时不发请求
        <Select size="small" value={p.row.human_score ?? undefined} options={scoreOpts}
          placeholder={t('super.material.scorePlaceholder')} style={{ width: 100 }}
          onChange={(v) => { const r = p.row as MaterialRow; if (v != null) review(r, { status: r.status, human_score: v }) }} />
      ),
    },
    { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => { const m = STATUS_KEYS[String(p.row.status)] || { label: p.row.status as I18nKey, theme: 'warning' as const }; return <Tag theme={m.theme}>{t(m.label)}</Tag> } },
    { colKey: 'created_at', title: t('super.material.colCollected'), width: 160, cell: (p: CellProps) => String(p.row.created_at || '').slice(0, 19).replace('T', ' ') || '-' },
    {
      colKey: 'op', title: t('super.material.colOp'), width: 230, cell: (p: CellProps) => {
        const r = p.row as MaterialRow
        return (
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <Button size="small" theme="success" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'approved' })}>{t('super.material.approve')}</Button>
            <Button size="small" theme="danger" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'rejected' })}>{t('super.material.reject')}</Button>
            <Button size="small" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'pending' })}>{t('super.material.stPending')}</Button>
            <Button size="small" theme="primary" loading={busyId === r.id} disabled={busyId !== null && busyId !== r.id} onClick={() => evals(r)}>{t('super.material.aiScore')}</Button>
          </div>
        )
      },
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>{t('super.material.title')}</h2>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <Select value={status} options={[{ label: t('super.material.allStatus'), value: '' }, ...Object.entries(STATUS_KEYS).map(([k, v]) => ({ label: t(v.label), value: k }))]} onChange={(v) => { setStatus(v as string); setPage(1) }} style={{ width: 130 }} />
          <Select value={source} options={[{ label: t('super.material.allSource'), value: '' }, { label: t('super.material.srcAiReply'), value: 'ai' }, { label: t('super.material.srcHumanReply'), value: 'human' }]} onChange={(v) => { setSource(v as string); setPage(1) }} style={{ width: 130 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>{t('super.material.refresh')}</Button>
        </div>
      </div>
      <div className="overflow-x-auto">
        <Table rowKey="id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
          empty={t('super.material.empty')} pagination={{ current: page, pageSize: 20, total, onChange: (p: { current?: number }) => setPage(p.current || 1) }} />
      </div>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        {t('super.material.footNote')}
      </p>
    </div>
  )
}
