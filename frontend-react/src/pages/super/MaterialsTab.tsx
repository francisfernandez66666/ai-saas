// 素材审核 Tab（P1-9 零UI补齐批 2026-09-20）：KB 反馈素材池的超管人工评审落点。
// 后端三端点自 §素材采集起一直无前端消费者（超管台缺 Tab），此处补齐：
//   GET  /api/v1/super/materials?status=&source=&page=&page_size=（跨租户素材池分页）
//   POST /api/v1/super/materials/:id/review {status, human_score?, pack_code?, deal_closed?}（approved/rejected/pending）
//   POST /api/v1/super/materials/:id/evals（触发 AI 0-5 评分，回写 ai_score/ai_eval_note）
// 仅 super_admin（SuperRequired），平台路径无需 X-Tenant-ID。
import { useCallback, useEffect, useState } from 'react'
import { Button, Select, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CellProps, TableRowData } from '../../types'

// MaterialRow 素材行（对齐 model.KbFeedbackMaterial json 列；human_score/ai_score 指针列可为 null）
type MaterialRow = {
  id: number; tenant_id: number; conversation_id: number; message_id: number
  source: string; content: string; status: string
  ai_score: number | null; ai_eval_note?: string
  human_score: number | null; deal_closed?: boolean
  pack_code?: string; created_at: string
}
type ListResp = { code: number; data?: { list: MaterialRow[]; total: number; page: number; page_size: number } }
type EvalResp = { code: number; message?: string; data?: { score: number; note: string } }

const STATUS_LABELS: Record<string, { label: string; theme: 'warning' | 'success' | 'danger' }> = {
  pending: { label: '待处理', theme: 'warning' },
  approved: { label: '已通过', theme: 'success' },
  rejected: { label: '已拒绝', theme: 'danger' },
}
const SCORE_OPTS = [1, 2, 3, 4, 5].map((n) => ({ label: String(n) + ' 分', value: n }))

/** 素材审核：跨租户 KB 反馈素材池，人工评审（通过/拒绝/待处理 + 1-5 分）与 AI 评分触发。 */
export function MaterialsTab() {
  const [rows, setRows] = useState<MaterialRow[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(1)
  const [status, setStatus] = useState('pending')
  const [source, setSource] = useState('')
  const [loading, setLoading] = useState(false)
  // 正在评审/评分的素材 id（按钮置灰防连点；AI 评分要等大模型返回）
  const [busyId, setBusyId] = useState<number | null>(null)

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
    if (j?.code === 0) { MessagePlugin.success(j.message || '已更新'); load() }
  }
  // AI 评分：调 evals 阶段模型打 0-5 分并回写，成功后直接展示本次返回分/理由
  async function evals(r: MaterialRow) {
    setBusyId(r.id)
    const j = await AUTH<EvalResp>(`/api/v1/super/materials/${r.id}/evals`, { method: 'POST', timeoutMs: 60000 })
    setBusyId(null)
    if (j?.code === 0) {
      MessagePlugin.success(`AI 评分 ${j.data?.score ?? '-'} 分：${j.data?.note || ''}`)
      load()
    }
  }

  const cols = [
    { colKey: 'id', title: 'ID', width: 70 },
    { colKey: 'tenant_id', title: '租户', width: 80, cell: (p: CellProps) => '#' + p.row.tenant_id },
    { colKey: 'source', title: '来源', width: 80, cell: (p: CellProps) => <Tag variant="light">{p.row.source === 'human' ? '人工' : p.row.source === 'ai' ? 'AI' : p.row.source}</Tag> },
    { colKey: 'content', title: '素材内容', width: 320, ellipsis: true },
    { colKey: 'ai_score', title: 'AI分', width: 80, cell: (p: CellProps) => p.row.ai_score == null ? '-' : String(p.row.ai_score) },
    { colKey: 'ai_eval_note', title: 'AI理由', width: 200, ellipsis: true, cell: (p: CellProps) => p.row.ai_eval_note || '-' },
    {
      colKey: 'human_score', title: '人工分', width: 120, cell: (p: CellProps) => (
        // review 端点只在 human_score 非空时更新该列（无清除语义），故 clearable 置空时不发请求
        <Select size="small" value={p.row.human_score ?? undefined} options={SCORE_OPTS}
          placeholder="未评" style={{ width: 100 }}
          onChange={(v) => { const r = p.row as MaterialRow; if (v != null) review(r, { status: r.status, human_score: v }) }} />
      ),
    },
    { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => { const m = STATUS_LABELS[String(p.row.status)] || { label: p.row.status as string, theme: 'warning' as const }; return <Tag theme={m.theme}>{m.label}</Tag> } },
    { colKey: 'created_at', title: '采集时间', width: 160, cell: (p: CellProps) => String(p.row.created_at || '').slice(0, 19).replace('T', ' ') || '-' },
    {
      colKey: 'op', title: '操作', width: 230, cell: (p: CellProps) => {
        const r = p.row as MaterialRow
        return (
          <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
            <Button size="small" theme="success" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'approved' })}>通过</Button>
            <Button size="small" theme="danger" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'rejected' })}>拒绝</Button>
            <Button size="small" variant="outline" disabled={busyId !== null} onClick={() => review(r, { status: 'pending' })}>待处理</Button>
            <Button size="small" theme="primary" loading={busyId === r.id} disabled={busyId !== null && busyId !== r.id} onClick={() => evals(r)}>AI评分</Button>
          </div>
        )
      },
    },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>素材审核</h2>
        <div style={{ display: 'flex', gap: 10, alignItems: 'center', flexWrap: 'wrap' }}>
          <Select value={status} options={[{ label: '全部状态', value: '' }, ...Object.entries(STATUS_LABELS).map(([k, v]) => ({ label: v.label, value: k }))]} onChange={(v) => { setStatus(v as string); setPage(1) }} style={{ width: 130 }} />
          <Select value={source} options={[{ label: '全部来源', value: '' }, { label: 'AI 回复', value: 'ai' }, { label: '人工回复', value: 'human' }]} onChange={(v) => { setSource(v as string); setPage(1) }} style={{ width: 130 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>刷新</Button>
        </div>
      </div>
      <div className="overflow-x-auto">
        <Table rowKey="id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
          empty="暂无素材" pagination={{ current: page, pageSize: 20, total, onChange: (p: { current?: number }) => setPage(p.current || 1) }} />
      </div>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        素材池来自对话链路自动采集（AI 高钩子回复/人工优秀回复）。通过=进 KB 候选，拒绝=淘汰；AI 评分调 evals 阶段模型按口语自然度/需求针对性/推进有效性打 0-5 分，人工分（1-5）覆盖评审口径。
      </p>
    </div>
  )
}
