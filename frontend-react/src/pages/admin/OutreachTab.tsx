// 主动触达队列 Tab（触达最小闭环 · 批次3，2026-09-23）：排期、查看裁决结果、撤回。
//
// 页面只做三件事——列队列、排一条、撤一条；"什么时候真的发出去、为什么没发"由后端裁决并
// 回吐稳定原因码（reason），文案在前端映射。刻意不提供"立即发送"按钮：合规判定（48h 窗口/
// 周内频次/静默时段）必须在派发时重新跑一遍，直发按钮等于绕开这套裁决。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, OutreachConfig, OutreachListResp, OutreachTaskRow } from '../../types'

// 行/列表/配置三类形状统一从 ../../types 取（与后端 outreachTaskView 同源，
// 由 cmd/apidump 的 apidump:ts 注解进契约，前端不再自持一份会漂移的副本）
type ApiResp<T> = { code: number; message?: string; data?: T }
// 顾问客户列表的分页信封：page_size 是**服务端钳制后的生效值**，
// 前端据它判断"这一页是否已经装满"，而不是按自己请求里写的数字猜。
type CustomerPage = { list: { id: number; name?: string; phone?: string }[]; total?: number; page_size?: number }
// Form 触达排期弹窗的本地表单态：目标客户（未选为 null）+ 触达内容 + 计划发送时间。
type Form = { customer_id: number | null; content: string; scheduled_at: string }

// 状态码 → 展示键 + 标签主题。状态码是后端契约；文案走 I18nKey，渲染期才取
const STATUS_META: Record<string, { key: I18nKey; theme: 'primary' | 'success' | 'warning' | 'danger' | 'default' }> = {
  pending: { key: 'outreach.statusPending', theme: 'default' },
  queued: { key: 'outreach.statusQueued', theme: 'primary' },
  sent: { key: 'outreach.statusSent', theme: 'success' },
  skipped: { key: 'outreach.statusSkipped', theme: 'warning' },
  failed: { key: 'outreach.statusFailed', theme: 'danger' },
  cancelled: { key: 'outreach.statusCancelled', theme: 'default' },
}

// 后端 reason 是稳定字面量（internal/model/outreach.go），改名即为契约破坏；
// 这里只把"码 → 中文话术"的映射换成"码 → I18nKey"，翻译照旧发生在渲染期
const REASON_KEYS: Record<string, I18nKey> = {
  disabled: 'outreach.reasonDisabled',
  no_channel: 'outreach.reasonNoChannel',
  out_of_window: 'outreach.reasonOutOfWindow',
  over_weekly_limit: 'outreach.reasonOverWeeklyLimit',
  channel_inactive: 'outreach.reasonChannelInactive',
  content_flagged: 'outreach.reasonContentFlagged',
  send_failed: 'outreach.reasonSendFailed',
}

/** 格式化触达时间字段。 */
function fmtTime(v?: string | null): string {
  return v ? new Date(v).toLocaleString('zh-CN', { hour12: false }) : '-'
}

/** 主动触达队列：状态筛选 + 排期弹窗 + 待派发撤回。 */
export function OutreachTab() {
  const { t } = useI18n()
  // 状态下拉选项：状态码是契约、标签是文案，故在渲染期由 STATUS_META 组装
  // （原先是模块级 STATUS_OPTIONS，模块常量拿不到 t()，只能整表搬进组件体）
  const statusOptions = Object.keys(STATUS_META).map((k) => ({ label: t(STATUS_META[k].key), value: k }))
  const [rows, setRows] = useState<OutreachTaskRow[]>([])
  const [total, setTotal] = useState(0)
  const [status, setStatus] = useState('')
  const [cfg, setCfg] = useState<OutreachConfig | null>(null)
  const [loading, setLoading] = useState(false)
  const [form, setForm] = useState<Form | null>(null)
  const [saving, setSaving] = useState(false)
  // 客户下拉：复用顾问侧客户列表（按回显页长逐页续拉，见 ensureCustomers）
  const [custOptions, setCustOptions] = useState<{ label: string; value: number }[]>([])

  const load = useCallback(async () => {
    setLoading(true)
    const j = (await AUTH(`/api/v1/admin/outreach/tasks?status=${status}&limit=50&offset=0`)) as ApiResp<OutreachListResp> | null
    if (j?.code === 0) {
      setRows(j.data?.list || [])
      setTotal(j.data?.total || 0)
      setCfg(j.data?.config || null)
    } else MessagePlugin.error(j?.message || t('outreach.loadFailed'))
    setLoading(false)
  }, [status, t])

  useEffect(() => { void load() }, [load])

  // 客户下拉懒加载：点"新建触达"才拉，按服务端回显的 page_size 逐页续拉到 total 为止。
  // 刻意不放 useEffect——列表页首屏不必为一个还没打开的弹窗多打一次请求。
  async function ensureCustomers() {
    if (custOptions.length > 0) return
    const collected: { id: number; name?: string; phone?: string }[] = []
    // 最多 5 页：下拉是"选人"用的，不是导数用的。服务端硬顶 100/页（FIX-4 收口），
    // 旧写法一次 `page_size=200` 会被静默截成 100——租户客户多于 100 位时，
    // 第 101 位之后的客户在界面上根本选不到，且没有任何"还有更多"的提示。
    for (let page = 1; page <= 5; page++) {
      const j = (await AUTH(`/api/v1/advisor/customers?page=${page}&page_size=100&assigned=all`)) as ApiResp<CustomerPage> | null
      if (j?.code !== 0) break
      const list = j.data?.list || []
      collected.push(...list)
      const size = Number(j.data?.page_size || list.length || 100)
      if (list.length < size || collected.length >= Number(j.data?.total || 0)) break
    }
    setCustOptions(collected.map((x) => ({ label: x.name || x.phone || t('common.customerId', { id: x.id }), value: x.id })))
  }

  // 提交排期：空正文本地先拦（后端还会过内容安全与长度上限）；时间留空=立即排期（落在静默段自动顺延）
  async function submit() {
    if (!form) return
    if (!form.customer_id) { MessagePlugin.warning(t('outreach.customerRequired')); return }
    if (!form.content.trim()) { MessagePlugin.warning(t('outreach.contentRequired')); return }
    setSaving(true)
    const body: Record<string, unknown> = { customer_id: form.customer_id, content: form.content.trim() }
    if (form.scheduled_at) body.scheduled_at = new Date(form.scheduled_at).toISOString()
    const j = (await AUTH('/api/v1/admin/outreach/tasks', { method: 'POST', body })) as ApiResp<OutreachTaskRow> | null
    setSaving(false)
    if (j?.code !== 0) { MessagePlugin.error(j?.message || t('outreach.scheduleFailed')); return }
    MessagePlugin.success(t('outreach.scheduled'))
    setForm(null)
    load()
  }

  // 撤回：仅待派发（pending）可撤，服务端按状态机裁决，撤不动即提示
  async function cancel(row: OutreachTaskRow) {
    if (!(await confirmDialog(t('outreach.confirmCancel', { name: row.customer_name || t('common.customerId', { id: row.customer_id }) })))) return
    const j = (await AUTH(`/api/v1/admin/outreach/tasks/${row.id}/cancel`, { method: 'POST' })) as ApiResp<null> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || t('outreach.cancelFailed'))
    else MessagePlugin.success(t('outreach.cancelled'))
    load()
  }

  const cols = [
    { colKey: 'customer', title: t('common.customer'), width: 150, cell: (p: CellProps) => p.row.customer_name || t('common.customerId', { id: p.row.customer_id }) },
    { colKey: 'content', title: t('outreach.colContent'), width: 260, ellipsis: true },
    { colKey: 'scheduled_at', title: t('outreach.colScheduledAt'), width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.scheduled_at)}</span> },
    { colKey: 'status', title: t('common.status'), width: 190, cell: (p: CellProps) => {
      const s = STATUS_META[p.row.status]
      return (
        <div className="flex flex-col gap-1">
          <Tag theme={s ? s.theme : 'default'}>{s ? t(s.key) : p.row.status}</Tag>
          {p.row.reason ? <span className="text-[10px] text-gray-500">{REASON_KEYS[p.row.reason] ? t(REASON_KEYS[p.row.reason]) : p.row.reason}</span> : null}
          {p.row.error ? <span className="text-[10px] text-red-500 break-all">{p.row.error}</span> : null}
        </div>
      )
    } },
    { colKey: 'attempts', title: t('outreach.colAttempts'), width: 70, cell: (p: CellProps) => p.row.attempts || 0 },
    { colKey: 'sent_at', title: t('outreach.colSentAt'), width: 160, cell: (p: CellProps) => <span className="text-xs text-gray-500">{fmtTime(p.row.sent_at)}</span> },
    { colKey: 'op', title: t('crud.actions'), width: 100, cell: (p: CellProps) => (
      p.row.status === 'pending'
        ? <Button size="small" variant="text" theme="danger" onClick={() => void cancel(p.row as OutreachTaskRow)}>{t('outreach.cancel')}</Button>
        : <span className="text-xs text-gray-400">-</span>
    ) },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div className="text-[13px] text-gray-500">
          {cfg && !cfg.enabled
            ? <span className="text-orange-600">{t('outreach.disabledHint')}</span>
            : t('outreach.intro')}
          {cfg?.quiet_hours ? <span className="text-gray-400">{t('outreach.quietHours', { hours: cfg.quiet_hours })}</span> : null}
          {cfg?.weekly_limit ? <span className="text-gray-400">{t('outreach.weeklyLimit', { limit: cfg.weekly_limit })}</span> : null}
        </div>
        <div className="flex gap-2 items-center">
          <Select value={status} onChange={(v) => setStatus(String(v ?? ''))} options={[{ label: t('common.statusAll'), value: '' }, ...statusOptions]} style={{ width: 140 }} size="small" />
          <Button theme="default" variant="outline" onClick={load} disabled={loading}>{loading ? t('outreach.refreshing') : t('crud.refresh')}</Button>
          <Button theme="primary" onClick={() => { setForm({ customer_id: null, content: '', scheduled_at: '' }); void ensureCustomers() }}>{t('outreach.createBtn')}</Button>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty={t('outreach.empty')} loading={loading} />
      </div>
      <div className="text-xs text-gray-400">{t('outreach.totalCount', { count: total })}</div>

      <Dialog
        header={t('outreach.createTitle')}
        visible={!!form}
        onClose={() => setForm(null)}
        onConfirm={submit}
        confirmBtn={saving ? t('outreach.submitting') : t('outreach.scheduleBtn')}
        width="640px"
      >
        {form && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('outreach.customerLabel')}</label>
              <Select value={form.customer_id ?? undefined} onChange={(v) => setForm({ ...form, customer_id: Number(v) || null })}
                options={custOptions} filterable placeholder={t('outreach.customerPh')} style={{ width: '100%' }} />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('outreach.scheduledAtLabel')}</label>
              <input type="datetime-local" value={form.scheduled_at} onChange={(e) => setForm({ ...form, scheduled_at: (e.target as HTMLInputElement).value })}
                className="px-3 py-2 border rounded-lg text-sm w-full" />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('outreach.contentLabel')}</label>
              <Textarea value={form.content} onChange={(v) => setForm({ ...form, content: String(v) })} maxlength={500}
                placeholder={t('outreach.contentPh')} autosize={{ minRows: 3, maxRows: 6 }} />
            </div>
            <p className="text-xs text-gray-400">{t('outreach.quietNote')}</p>
          </div>
        )}
      </Dialog>
    </div>
  )
}
