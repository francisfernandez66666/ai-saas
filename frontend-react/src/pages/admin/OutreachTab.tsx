// 主动触达队列 Tab（触达最小闭环 · 批次3，2026-09-23）：排期、查看裁决结果、撤回。
//
// 页面只做三件事——列队列、排一条、撤一条；"什么时候真的发出去、为什么没发"由后端裁决并
// 回吐稳定原因码（reason），文案在前端映射。刻意不提供"立即发送"按钮：合规判定（48h 窗口/
// 周内频次/静默时段）必须在派发时重新跑一遍，直发按钮等于绕开这套裁决。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, OutreachConfig, OutreachListResp, OutreachTaskRow } from '../../types'

// 行/列表/配置三类形状统一从 ../../types 取（与后端 outreachTaskView 同源，
// 由 cmd/apidump 的 apidump:ts 注解进契约，前端不再自持一份会漂移的副本）
type ApiResp<T> = { code: number; message?: string; data?: T }
type Form = { customer_id: number | null; content: string; scheduled_at: string }

const STATUS_META: Record<string, { label: string; theme: 'primary' | 'success' | 'warning' | 'danger' | 'default' }> = {
  pending: { label: '待派发', theme: 'default' },
  queued: { label: '投递中', theme: 'primary' },
  sent: { label: '已送达', theme: 'success' },
  skipped: { label: '已拦下', theme: 'warning' },
  failed: { label: '发送失败', theme: 'danger' },
  cancelled: { label: '已撤回', theme: 'default' },
}

// 后端 reason 是稳定字面量（internal/model/outreach.go），改名即为契约破坏
const REASON_TEXT: Record<string, string> = {
  disabled: '主动触达开关未开启',
  no_channel: '客户没有可用通道身份（发不出去）',
  out_of_window: '已超出微信侧 48 小时客服窗口',
  over_weekly_limit: '该客户周内触达已达上限',
  channel_inactive: '命中的通道已停用或未验证',
  content_flagged: '文案未通过内容安全校验',
  send_failed: '通道终判发送失败',
}

const STATUS_OPTIONS = Object.keys(STATUS_META).map((k) => ({ label: STATUS_META[k].label, value: k }))

/** 格式化触达时间字段。 */
function fmtTime(v?: string | null): string {
  return v ? new Date(v).toLocaleString('zh-CN', { hour12: false }) : '-'
}

/** 主动触达队列：状态筛选 + 排期弹窗 + 待派发撤回。 */
export function OutreachTab() {
  const [rows, setRows] = useState<OutreachTaskRow[]>([])
  const [total, setTotal] = useState(0)
  const [status, setStatus] = useState('')
  const [cfg, setCfg] = useState<OutreachConfig | null>(null)
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<Form | null>(null)
  const [saving, setSaving] = useState(false)
  // 客户下拉：复用顾问侧客户列表（一次拉 200 条足够选人，超出靠可搜索过滤）
  const [custOptions, setCustOptions] = useState<{ label: string; value: number }[]>([])

  const load = useCallback(async () => {
    setLoading(true)
    const j = (await AUTH(`/api/v1/admin/outreach/tasks?status=${status}&limit=50&offset=0`)) as ApiResp<OutreachListResp> | null
    if (j?.code === 0) {
      setRows(j.data?.list || [])
      setTotal(j.data?.total || 0)
      setCfg(j.data?.config || null)
    } else MessagePlugin.error(j?.message || '触达队列加载失败')
    setLoading(false)
  }, [status])

  useEffect(() => { void load() }, [load])

  // 客户下拉懒加载：点"新建触达"才拉（一次 200 条够选人，超出靠可搜索过滤）。
  // 刻意不放 useEffect——列表页首屏不必为一个还没打开的弹窗多打一次请求。
  async function ensureCustomers() {
    if (custOptions.length > 0) return
    const j = (await AUTH('/api/v1/advisor/customers?page_size=200&assigned=all')) as ApiResp<{ list: { id: number; name?: string; phone?: string }[] }> | null
    const list = j?.code === 0 ? (j.data?.list || []) : []
    setCustOptions(list.map((x) => ({ label: x.name || x.phone || `客户 #${x.id}`, value: x.id })))
  }

  // 提交排期：空正文本地先拦（后端还会过内容安全与长度上限）；时间留空=立即排期（落在静默段自动顺延）
  async function submit() {
    if (!form) return
    if (!form.customer_id) { MessagePlugin.warning('请选择触达客户'); return }
    if (!form.content.trim()) { MessagePlugin.warning('请填写触达文案'); return }
    setSaving(true)
    const body: Record<string, unknown> = { customer_id: form.customer_id, content: form.content.trim() }
    if (form.scheduled_at) body.scheduled_at = new Date(form.scheduled_at).toISOString()
    const j = (await AUTH('/api/v1/admin/outreach/tasks', { method: 'POST', body })) as ApiResp<OutreachTaskRow> | null
    setSaving(false)
    if (j?.code !== 0) { MessagePlugin.error(j?.message || '排期失败'); return }
    MessagePlugin.success('已排期，到点由后台派发')
    setForm(null)
    load()
  }

  // 撤回：仅待派发（pending）可撤，服务端按状态机裁决，撤不动即提示
  async function cancel(row: OutreachTaskRow) {
    if (!(await confirmDialog(`确认撤回这条触达（${row.customer_name || '客户 #' + row.customer_id}）？`))) return
    const j = (await AUTH(`/api/v1/admin/outreach/tasks/${row.id}/cancel`, { method: 'POST' })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '撤回失败（仅待派发可撤）')
    else MessagePlugin.success('已撤回')
    load()
  }

  const cols = [
    { colKey: 'customer', title: '客户', width: 150, cell: (p: CellProps) => p.row.customer_name || `客户 #${p.row.customer_id}` },
    { colKey: 'content', title: '文案', width: 260, ellipsis: true },
    { colKey: 'scheduled_at', title: '计划时间', width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.scheduled_at)}</span> },
    { colKey: 'status', title: '状态', width: 190, cell: (p: CellProps) => {
      const s = STATUS_META[p.row.status] || { label: p.row.status, theme: 'default' as const }
      return (
        <div className="flex flex-col gap-1">
          <Tag theme={s.theme}>{s.label}</Tag>
          {p.row.reason ? <span className="text-[10px] text-gray-500">{REASON_TEXT[p.row.reason] || p.row.reason}</span> : null}
          {p.row.error ? <span className="text-[10px] text-red-500 break-all">{p.row.error}</span> : null}
        </div>
      )
    } },
    { colKey: 'attempts', title: '尝试', width: 70, cell: (p: CellProps) => p.row.attempts || 0 },
    { colKey: 'sent_at', title: '送达时间', width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.sent_at)}</span> },
    { colKey: 'op', title: '操作', width: 100, cell: (p: CellProps) => (
      p.row.status === 'pending'
        ? <Button size="small" variant="text" theme="danger" onClick={() => void cancel(p.row as OutreachTaskRow)}>撤回</Button>
        : <span className="text-xs text-gray-400">-</span>
    ) },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div className="text-[13px] text-gray-500">
          {cfg && !cfg.enabled
            ? <span className="text-orange-600">主动触达未开启：到「系统配置 · 触达通知」打开 outreach_enabled 后才能排期。</span>
            : '到点由后台统一派发（48 小时窗口、周内频次上限、静默时段都会重新裁决），拦下的任务会留原因。'}
          {cfg?.quiet_hours ? <span className="text-gray-400"> 静默时段 {cfg.quiet_hours}</span> : null}
          {cfg?.weekly_limit ? <span className="text-gray-400"> 单客户 7 天上限 {cfg.weekly_limit} 条</span> : null}
        </div>
        <div className="flex gap-2 items-center">
          <Select value={status} onChange={(v) => setStatus(String(v ?? ''))} options={[{ label: '全部状态', value: '' }, ...STATUS_OPTIONS]} style={{ width: 140 }} size="small" />
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? '刷新中…' : '刷新'}</Button>
          <Button theme="primary" onClick={() => { setForm({ customer_id: null, content: '', scheduled_at: '' }); void ensureCustomers() }}>+ 新建触达</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty="暂无触达任务" loading={loading} />
      </div>
      <div className="text-xs text-gray-400">共 {total} 条</div>

      <Dialog
        header="新建主动触达"
        visible={!!form}
        onClose={() => setForm(null)}
        onConfirm={submit}
        confirmBtn={saving ? '提交中…' : '排期'}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">触达客户</label>
              <Select value={form.customer_id ?? undefined} onChange={(v) => setForm({ ...form, customer_id: Number(v) || null })}
                options={custOptions} filterable placeholder="选择客户（需已有通道身份）" style={{ width: '100%' }} />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">计划时间（留空=立即排期）</label>
              <input type="datetime-local" value={form.scheduled_at} onChange={(e) => setForm({ ...form, scheduled_at: (e.target as HTMLInputElement).value })}
                className="px-3 py-2 border rounded-lg text-sm w-full" />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">触达文案</label>
              <Textarea value={form.content} onChange={(v) => setForm({ ...form, content: String(v) })} maxlength={500}
                placeholder="如：哥，上次您看的车型到店了，周末有空来看两眼" autosize={{ minRows: 3, maxRows: 6 }} />
            </div>
            <p className="text-xs text-gray-400">落在静默时段会自动顺延到时段结束后，不会在夜里打扰客户。</p>
          </div>
        )}
      </Dialog>
    </div>
  )
}
