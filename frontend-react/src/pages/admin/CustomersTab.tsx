// 客户线索 Tab（F1/F10）：阶段筛选、列表、详情抽屉、编辑、打标、导出。
// D4(2026-09-23) 追加「下钻态」：从 AI 贡献度看板点客户数进来时，本 Tab 改打
// /stats/ai-contribution/customers 只展示那个数字背后的客户。
// 下钻态刻意隐藏阶段筛选与"新建线索/导出客户"——名单是后端按同一谓词算出的归因结果，
// 再叠一层前端筛选就会和卡片上的数字对不上，而"对不上"正是这张看板最不能出现的形态。
// P2-14：阶段名/来源名/状态名等界面文案落成 I18nKey，中文原文进词典；
// 后端口径字符串（journey_stage 码、drillResp.note、sender_type 等）是数据，不做翻译。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Drawer, Input, InputNumber, MessagePlugin, Select, Tag, Textarea } from 'tdesign-react'
import { apiFetch, AUTH, type ApiEnvelope } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import { isVisitorName } from '../../lib/visitor'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
import type { CrudRow } from '../../hooks/useCrud'
import type { AIContributionDrillResp, ContributionDrill, TableRowData } from '../../types'

/** 阶段码 → 文案键（界面显示名随语言走，码本身是后端口径不翻译）。 */
const STAGE_KEYS: Record<string, I18nKey> = {
  ai_connected: 'customers.stageAiConnected', human_connected: 'customers.stageHumanConnected', lead_captured: 'customers.stageLeadCaptured',
  arrived: 'customers.stageArrived', ordered: 'customers.stageOrdered', delivered: 'customers.stageDelivered', lost: 'customers.stageLost',
}
const STAGE_CODES = ['ai_connected', 'human_connected', 'lead_captured', 'arrived', 'ordered', 'delivered', 'lost']
const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }
const STATUS_OPTS = ['', 'ai_connected', 'human_connected', 'lead_captured', 'arrived', 'ordered', 'delivered', 'lost']
/** D4 下钻名单页大小：与后端默认一致，后端硬上限 100（下钻是核对用，不是导数用）。 */
const DRILL_PAGE_SIZE = 20
const STATUS_EDIT_KEYS: { key: I18nKey; value: number }[] = [{ key: 'common.valid', value: 1 }, { key: 'common.disable', value: 0 }]
const SOURCE_KEYS: { key: I18nKey; value: string }[] = [
  { key: 'customers.sourceManual', value: 'manual' }, { key: 'customers.sourceAd', value: 'ad' },
  { key: 'customers.sourceReferral', value: 'referral' }, { key: 'customers.sourceWalkin', value: 'walkin' },
  { key: 'customers.sourceOnline', value: 'online' },
]

/** 清洗客户展示名，访客占位名统一显示为客户。 */
function nameOf(v: string | undefined, fallback: string) { return isVisitorName(v) ? fallback : (v as string) }
/** 取客户名首字作为头像占位符。 */
function initialOf(v: string | undefined, fallback: string) { return isVisitorName(v) ? fallback : ((v as string)?.[0] || '?') }

/** 触发浏览器下载后台导出的 CSV 文件。 */
async function downloadCsv(url: string, filename: string, failMsg: string, okMsg: string) {
  const res = await apiFetch(url)
  if (!res.ok) { MessagePlugin.error(failMsg); return }
  const blob = await res.blob()
  const a = document.createElement('a')
  a.href = URL.createObjectURL(blob)
  a.download = filename
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(a.href)
  MessagePlugin.success(okMsg)
}

/** 客户列表 Tab：支持筛选、标签、阶段和 CSV 导出；带 drill 时进入 AI 贡献度下钻名单态。 */
export function CustomersTab({ drill, onExitDrill }: { drill?: ContributionDrill | null; onExitDrill?: () => void }) {
  const { t } = useI18n()
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(1)
  const [total, setTotal] = useState(0)
  const [leads, setLeads] = useState<TableRowData[]>([])
  // 下钻响应（指标名/窗口/口径说明随行下发，前端不复写第二套文案）
  const [drillResp, setDrillResp] = useState<AIContributionDrillResp | null>(null)
  const [loading, setLoading] = useState(false)
  const [detail, setDetail] = useState<TableRowData | null>(null)
  const [drawer, setDrawer] = useState(false)
  const [testDrives, setTestDrives] = useState<TableRowData[]>([])
  const [chat, setChat] = useState<TableRowData[]>([])
  const [showTd, setShowTd] = useState(false)
  const [showChat, setShowChat] = useState(false)
  const [editVisible, setEditVisible] = useState(false)
  const [tagVisible, setTagVisible] = useState(false)
  // P2-10：表单态两袋（编辑/新建）值要直填 TDesign 受控输入框（string/number 混排），
  // 与 useCrud.CrudRow 同款判据保留 Record<string, any>；写入入口 setField 已收敛为 unknown
  const [form, setForm] = useState<Record<string, any>>({})
  const [tagForm, setTagForm] = useState<string[]>([])
  const [tagOptions, setTagOptions] = useState<{ label: string; value: string }[]>([])
  // P1-9(2026-09-20)：抽屉打开时经 GET /customers/:id/tags 拉取的权威标签行（含 tag_id，供差量提交与单标直删）
  const [custTags, setCustTags] = useState<TableRowData[]>([])
  // P1-9：历史会话面板（GET /customers/:id/conversations）与单会话消息时间线（GET /conversations/:id/messages）
  const [convs, setConvs] = useState<TableRowData[]>([])
  const [showConvs, setShowConvs] = useState(false)
  const [openConv, setOpenConv] = useState<number | null>(null)
  const [convMsgs, setConvMsgs] = useState<TableRowData[]>([])
  const [users, setUsers] = useState<{ label: string; value: number }[]>([])
  const [saving, setSaving] = useState(false)
  const [createVisible, setCreateVisible] = useState(false)
  const [create, setCreate] = useState<Record<string, any>>({ name: '', phone: '', city: '', interest_model: '', budget: 0, source: 'manual', assigned_user_id: 0, remark: '' })

  const H = (v?: string) => nameOf(v, t('common.customer'))
  const HI = (v?: string) => initialOf(v, t('customers.initialGuest'))

  // 拉取客户线索列表（阶段筛选 + 分页 + 归属=all），useCallback 随筛选/页码重建。
  // drill 在位时走另一条真相源：/stats/ai-contribution/customers，只传 metric + days，
  // 名单与看板卡片数字由后端同一谓词算出（analytics.MatchContributionMetric），前端不补条件。
  const load = useCallback(async () => {
    setLoading(true)
    if (drill) {
      const q = new URLSearchParams({ metric: drill.metric, days: String(drill.days), page: String(page), page_size: String(DRILL_PAGE_SIZE) })
      const j = await AUTH<ApiEnvelope<'/api/v1/stats/ai-contribution/customers', 'GET'>>(`/api/v1/stats/ai-contribution/customers?${q}`)
      if (j?.code === 0 && j.data) {
        setDrillResp(j.data)
        setLeads(j.data.list || [])
        setTotal(j.data.total || 0)
        // 越界页后端只回空列表、total 如实（例如换了指标但页码还停在第 3 页）：收回首页，
        // 否则会停在"共 12 位客户 / 第 3 页空白"这种自相矛盾的形态
        if (j.data.total > 0 && (j.data.list || []).length === 0 && page > 1) setPage(1)
      } else {
        setDrillResp(null)
        setLeads([])
        setTotal(0)
      }
      setLoading(false)
      return
    }
    const r = await apiFetch(`/api/v1/advisor/customers?status=${filter}&page=${page}&page_size=20&assigned=all`)
    const j = await r.json().catch(() => null)
    if (j?.code === 0) { setLeads(j.data?.list || []); setTotal(j.data?.total || (j.data?.list || []).length) }
    setLoading(false)
  }, [filter, page, drill])

  useEffect(() => { void load() }, [load])
  // 指标或窗口一变就是"另一份名单"，页码必须回到第 1 页（留在旧页码会得到一页空白）
  useEffect(() => { setPage(1) }, [drill?.metric, drill?.days])
  useEffect(() => {
    ;(async () => {
      // GET 收敛第二参：同路径 POST /admin/tags 返回单条 Tag，不指定方法会取到 Tag|Paginated<Tag> 联合
      // FIX-4(2026-09-27)：打标弹窗要的是**全量字典**不是"第一页"，改 ?all=1（上限 1000，只回 id/name/code）。
      // 旧写法 page_size=200 被服务端硬顶 100 静默截断：标签多于 100 个时，界面里的可选标签就是缺的，
      // 而且提交时后端按 tag_ids 认，用户根本不知道自己少打了哪几个。
      const [t2, u] = await Promise.all([AUTH<ApiEnvelope<'/api/v1/admin/tags', 'GET'>>('/api/v1/admin/tags?all=1'), AUTH<ApiEnvelope<'/api/v1/org/users'>>('/api/v1/org/users')])
      // P1-9(2026-09-20)：选项 value 从标签名改为标签 ID——POST /customers/:id/tags 契约是 tag_ids，
      // 名称仍留在 label 里展示；旧的 PUT /advisor/customer/:id/tags（按名覆盖）保留给移动端工作台
      if (t2?.code === 0) setTagOptions((t2.data?.list || []).map((x: CrudRow) => ({ label: `${x.name}（${x.code || x.id}）`, value: String(x.id) })))
      if (u?.code === 0) setUsers([{ label: t('common.unassigned'), value: 0 }, ...(u.data || []).map((x: CrudRow) => ({ label: x.real_name || x.username || t('customers.userFallback', { id: x.id }), value: Number(x.id) }))])
      else setUsers([{ label: t('common.unassigned'), value: 0 }])
    })()
  }, [t])

  // 打开客户详情抽屉，并重置试驾/聊天/会话面板展开态；P1-9 详情成功后拉权威标签（含 tag_id）
  async function openDetail(id: number) {
    const r = await apiFetch(`/api/v1/advisor/customer/${id}`)
    const j = await r.json().catch(() => null)
    if (j?.code === 0) {
      setDetail(j.data); setDrawer(true); setShowTd(false); setShowChat(false); setTestDrives([]); setChat([])
      setShowConvs(false); setOpenConv(null); setConvMsgs([])
      const tg = await AUTH(`/api/v1/customers/${id}/tags`)
      setCustTags(tg?.code === 0 ? (tg.data || []) : [])
    } else MessagePlugin.error(t('customers.detailLoadFailed'))
  }

  // 展开或收起该客户的试驾记录（再次点击收起，不重复拉取）
  async function loadTd() {
    if (showTd) { setShowTd(false); return }
    const cid = detail?.customer?.id
    const j = await AUTH(`/api/v1/advisor/test-drives?customer_id=${cid}`)
    setTestDrives(j?.code === 0 ? (j.data || []) : []); setShowTd(true)
  }
  // 展开或收起该客户的最近聊天记录
  async function loadChat() {
    if (showChat) { setShowChat(false); return }
    const cid = detail?.customer?.id
    const j = await AUTH(`/api/v1/chat/history?customer_id=${cid}&limit=30`)
    setChat(j?.code === 0 ? (j.data || []) : []); setShowChat(true)
  }

  // P1-9：展开/收起历史会话列表（GET /customers/:id/conversations，最近 50 条，每次展开重拉）
  async function loadConvs() {
    if (showConvs) { setShowConvs(false); setOpenConv(null); setConvMsgs([]); return }
    const cid = detail?.customer?.id
    const j = await AUTH(`/api/v1/customers/${cid}/conversations`)
    setConvs(j?.code === 0 ? (j.data || []) : []); setShowConvs(true)
  }
  // P1-9：展开某条会话的消息时间线（GET /conversations/:id/messages，ASC ≤200 条），再次点击收起
  async function toggleConv(id: number) {
    if (openConv === id) { setOpenConv(null); setConvMsgs([]); return }
    setOpenConv(id); setConvMsgs([])
    const j = await AUTH(`/api/v1/conversations/${id}/messages`)
    if (j?.code === 0) setConvMsgs(j.data || [])
  }

  // 用当前详情预填编辑表单并打开编辑弹窗
  function openEdit() {
    const c = detail?.customer || {}
    setForm({
      name: c.name || '',
      phone: c.phone || '',
      age: c.age || 0,
      gender: c.gender || 0,
      city: c.city || '',
      region: c.region || '',
      career: c.career || '',
      interest_model: c.interest_model || '',
      budget: c.budget || 0,
      intent_score: c.intent_score || 0,
      journey_stage: c.journey_stage || '',
      assigned_user_id: c.assigned_user_id || 0,
      status: c.status ?? 1,
      remark: c.remark || '',
    })
    setEditVisible(true)
  }

  // 提交客户资料编辑（PUT info），成功后刷新详情与列表
  async function submitEdit() {
    if (!detail?.customer?.id) return
    setSaving(true)
    const cid = Number(detail.customer.id)
    const info = await AUTH(`/api/v1/advisor/customer/${cid}/info`, {
      method: 'PUT',
      body: {
        name: form.name || undefined,
        phone: form.phone || undefined,
        age: Number(form.age) || undefined,
        gender: Number(form.gender) || undefined,
        city: form.city || undefined,
        region: form.region || undefined,
        career: form.career || undefined,
        interest_model: form.interest_model || undefined,
        budget: Number(form.budget) || undefined,
        journey_stage: form.journey_stage || undefined,
        remark: form.remark || undefined,
      },
    })
    const cust = await AUTH<ApiEnvelope<'/api/v1/customers/:id'>>(`/api/v1/customers/${cid}`, {
      method: 'PUT',
      body: {
        intent_score: Number(form.intent_score) || undefined,
        assigned_user_id: Number(form.assigned_user_id) || undefined,
        status: Number(form.status) || undefined,
      },
    })
    setSaving(false)
    if (info?.code === 0 || cust?.code === 0) {
      MessagePlugin.success(t('customers.infoUpdated'))
      setEditVisible(false)
      await openDetail(cid)
      await load()
    }
  }

  // 打开标签编辑器：以权威标签（custTags，含 tag_id）预填选中项
  function openTagEditor() {
    setTagForm(custTags.map((t2: CrudRow) => String(t2.tag_id)).filter(Boolean))
    setTagVisible(true)
  }

  // P1-9(2026-09-20)：标签覆盖式编辑改为差量提交——新增走 POST /customers/:id/tags {tag_ids}，
  // 移除逐个走 DELETE /customers/:id/tags/:tag_id（旧 PUT 按名覆盖仅移动端保留）
  async function submitTags() {
    if (!detail?.customer?.id) return
    setSaving(true)
    const cid = Number(detail.customer.id)
    const cur = custTags.map((t2: CrudRow) => String(t2.tag_id))
    const adds = tagForm.filter((x) => !cur.includes(x)).map(Number)
    const dels = cur.filter((x) => !tagForm.includes(x))
    let ok = true
    if (adds.length > 0) {
      const j = await AUTH(`/api/v1/customers/${cid}/tags`, { method: 'POST', body: { tag_ids: adds } })
      if (j?.code !== 0) ok = false
    }
    for (const tid of dels) {
      const j = await AUTH(`/api/v1/customers/${cid}/tags/${tid}`, { method: 'DELETE' })
      if (j?.code !== 0) ok = false
    }
    setSaving(false)
    if (ok) {
      MessagePlugin.success(t('customers.tagsUpdated'))
      setTagVisible(false)
      await openDetail(cid)
      await load()
    }
  }

  // P1-9：抽屉标签 chip 直删（DELETE /customers/:id/tags/:tag_id），成功后刷新详情与列表
  async function removeTag(tagId: number) {
    if (!detail?.customer?.id) return
    if (!(await confirmDialog(t('customers.removeTagConfirm'), t('customers.removeTagTitle')))) return
    const cid = Number(detail.customer.id)
    const j = await AUTH(`/api/v1/customers/${cid}/tags/${tagId}`, { method: 'DELETE' })
    if (j?.code === 0) {
      MessagePlugin.success(t('customers.tagRemoved'))
      await openDetail(cid)
      await load()
    }
  }

  // 新建线索（手工建客）：姓名/手机号至少一项，成功后关窗清空并刷新
  async function submitCreate() {
    if (!String(create.name || '').trim() && !String(create.phone || '').trim()) { MessagePlugin.error(t('customers.nameOrPhoneRequired')); return }
    setSaving(true)
    const j = await AUTH<ApiEnvelope<'/api/v1/customers'>>('/api/v1/customers', {
      method: 'POST',
      body: {
        name: create.name || undefined,
        phone: create.phone || undefined,
        city: create.city || undefined,
        interest_model: create.interest_model || undefined,
        budget: Number(create.budget) || undefined,
        source: create.source || undefined,
        assigned_user_id: Number(create.assigned_user_id) || undefined,
        remark: create.remark || undefined,
      },
    })
    setSaving(false)
    if (j?.code === 0) {
      MessagePlugin.success(t('customers.leadCreated'))
      setCreateVisible(false)
      setCreate({ name: '', phone: '', city: '', interest_model: '', budget: 0, source: 'manual', assigned_user_id: 0, remark: '' })
      await load()
    }
    // AUTH 失败已内置 toastError，此处不再手动报错防双弹（P1-7 口径）
  }

  // 删除客户线索（二次确认；会话历史按合规保留），成功后关抽屉刷新
  async function deleteCustomer(id: number) {
    if (!(await confirmDialog(t('customers.deleteConfirm'), t('customers.deleteTitle')))) return
    const j = await AUTH(`/api/v1/customers/${id}`, { method: 'DELETE' })
    if (j?.code === 0) {
      MessagePlugin.success(t('customers.deleted'))
      setDrawer(false)
      await load()
    }
  }

  const c = detail?.customer
  // 下钻态派生值：名单标题/页码/口径说明都以后端响应为准，响应未回来时用入参兜底显示
  const inDrill = !!drill
  const drillLabel = drillResp?.label || drill?.metric || ''
  const drillDays = drillResp?.period_days || drill?.days || 0
  const drillPages = Math.max(1, Math.ceil(total / DRILL_PAGE_SIZE))
  // 聊天发送方展示名（sender_type 是后端口径码，展示名随语言走）
  const who = (sender: string) => (sender === 'customer' ? t('customers.whoCustomer') : sender === 'ai' ? 'AI' : sender === 'human' ? t('customers.whoHuman') : t('customers.whoSystem'))
  const wcolor = (sender: string) => (sender === 'customer' ? 'text-blue-600' : sender === 'ai' ? 'text-green-600' : 'text-orange-600')
  // P2-10：表单值袋按 key 动态写入，v 收敛为 unknown（form 仍为 Record<string, any>：
  // 其值要直填 TDesign 各受控输入框，string/number 混排，详见 useCrud CrudRow 同款判据）
  const setField = (k: string, v: unknown) => setForm((s) => ({ ...s, [k]: v }))

  const stageOptions = STAGE_CODES.map((v) => ({ label: t(STAGE_KEYS[v]), value: v }))
  const statusEditOptions = STATUS_EDIT_KEYS.map((x) => ({ label: t(x.key), value: x.value }))
  const sourceOptions = SOURCE_KEYS.map((x) => ({ label: t(x.key), value: x.value }))

  return (
    <div>
      {inDrill ? (
        /* D4 下钻横幅：说清"这是谁的名单、哪段窗口、多少人"，并给出唯一的退出口 */
        <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <p className="text-sm font-medium text-gray-800">
              {t('customers.drillPrefix')}<span className="text-indigo-600">{drillLabel}</span>{t('customers.drillMeta', { days: drillDays, total })}
            </p>
            <p className="text-xs text-gray-400 mt-1">{drillResp?.note || t('customers.drillNoteFallback')}</p>
            {drillResp && (
              <p className="text-[10px] text-gray-300 mt-0.5">
                {t('customers.drillWindow', { since: drillResp.since?.slice(0, 10) ?? '', until: drillResp.until?.slice(0, 10) ?? '' })}
                {drillPages > 1 ? t('customers.drillPage', { page, pages: drillPages }) : ''}
              </p>
            )}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Button variant="outline" onClick={() => { setPage(1); void load() }}>{t('crud.refresh')}</Button>
            <Button theme="primary" variant="outline" onClick={() => onExitDrill?.()}>{t('customers.backToBoard')}</Button>
          </div>
        </div>
      ) : (
        <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
          <span className="text-sm text-gray-500">{t('customers.filterStage')}</span>
          <select value={filter} onChange={(e) => { setFilter((e.target as HTMLSelectElement).value); setPage(1) }} aria-label={t('customers.filterStageAria')} className="px-3 py-2 border rounded-lg text-sm">
            {STATUS_OPTS.map((s) => <option key={s} value={s}>{s === '' ? t('common.all') : (STAGE_KEYS[s] ? t(STAGE_KEYS[s]) : s)}</option>)}
          </select>
          <Button theme="primary" onClick={() => setCreateVisible(true)}>{t('customers.newLead')}</Button>
          <Button variant="outline" onClick={() => { setPage(1); void load() }}>{t('crud.refresh')}</Button>
          <Button variant="outline" theme="success" onClick={() => void downloadCsv('/api/v1/admin/export/customers.csv', 'customers.csv', t('customers.exportFailed'), t('customers.downloadStarted'))}>{t('customers.exportCustomers')}</Button>
          <span className="text-xs text-gray-400">{t('crud.total', { count: total || leads.length })}</span>
        </div>
      )}
      <div className="space-y-2">
        {loading && <div className="text-center text-gray-400 py-6">{t('customers.loadingDots')}</div>}
        {!loading && leads.length === 0 && <div className="text-center text-gray-400 py-6">{inDrill ? t('customers.emptyDrill') : t('customers.emptyLeads')}</div>}
        {leads.map((l) => (
          <div key={l.id} className="bg-white rounded-lg border border-gray-100 p-3 hover:bg-gray-50 flex items-center justify-between gap-3">
            <div className="flex items-center gap-3 cursor-pointer min-w-0" onClick={() => openDetail(l.id)}>
              <div className="w-9 h-9 bg-gray-100 rounded-full flex items-center justify-center text-sm font-medium text-gray-500">{HI(l.name)}</div>
              <div className="min-w-0">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-gray-800">{H(l.name)}</span>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded-full ${STAGE_COLORS[l.journey_stage] || 'bg-gray-100 text-gray-500'}`}>{STAGE_KEYS[l.journey_stage] ? t(STAGE_KEYS[l.journey_stage]) : (l.journey_stage || '-')}</span>
                  {l.assigned_user_name && <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-indigo-100 text-indigo-600">{l.assigned_user_name}</span>}
                </div>
                <p className="text-xs text-gray-400 truncate">{l.phone || ''} {l.interest_model ? '· ' + l.interest_model : ''}</p>
              </div>
            </div>
            <div className="flex items-center gap-2 shrink-0">
              {inDrill ? (
                /* 下钻态右侧给的是"为什么在这个名单里"：接待归属 + 当前意向分 */
                <div className="text-right hidden md:block">
                  <p className="text-xs text-gray-500">{l.served_by === 'human' ? t('customers.servedHuman') : t('customers.servedAi')}</p>
                  <p className="text-[10px] text-gray-300">{t('customers.intentPct', { pct: ((Number(l.intent_score) || 0) * 100).toFixed(0) })}</p>
                </div>
              ) : (
                <div className="text-right hidden md:block">
                  <p className="text-xs text-gray-400 truncate max-w-[240px]">{l.last_message ? l.last_message.slice(0, 30) + '...' : t('common.noMessages')}</p>
                  <p className="text-[10px] text-gray-300">{l.updated_at ? new Date(l.updated_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''}</p>
                </div>
              )}
              <Button size="small" variant="text" onClick={() => openDetail(l.id)}>{t('common.detail')}</Button>
            </div>
          </div>
        ))}
      </div>
      {/* D4：下钻名单可超过一页（后端页大小上限 100），核对完一个指标还要能翻完 */}
      {inDrill && drillPages > 1 && (
        <div className="flex items-center justify-center gap-3 mt-3">
          <Button size="small" variant="outline" disabled={page <= 1} onClick={() => setPage(page - 1)}>{t('deals.prevPage')}</Button>
          <span className="text-xs text-gray-500">{t('customers.pageMeta', { page, pages: drillPages })}</span>
          <Button size="small" variant="outline" disabled={page >= drillPages} onClick={() => setPage(page + 1)}>{t('deals.nextPage')}</Button>
        </div>
      )}

      <Drawer visible={drawer} onClose={() => setDrawer(false)} size="560px" header={t('customers.drawerTitle')}>
        {c && (
          <div className="space-y-3">
            <div className="flex flex-wrap gap-2">
              <Button size="small" theme="primary" onClick={openEdit}>{t('customers.editInfo')}</Button>
              <Button size="small" theme="success" variant="outline" onClick={openTagEditor}>{t('customers.manageTags')}</Button>
              <Button size="small" variant="outline" onClick={() => void downloadCsv(`/api/v1/admin/export/conversations.csv?customer_id=${c.id}`, `customer_${c.id}_conversations.csv`, t('customers.exportFailed'), t('customers.downloadStarted'))}>{t('customers.exportConv')}</Button>
              <Button size="small" theme="danger" variant="outline" onClick={() => void deleteCustomer(Number(c.id))}>{t('customers.deleteLead')}</Button>
            </div>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div><span className="text-xs text-gray-400">{t('common.fullName')}</span><p className="text-sm font-medium">{H(c.name)}</p></div>
              <div><span className="text-xs text-gray-400">{t('common.phoneShort')}</span><p className="text-sm font-medium">{c.phone || '-'}</p></div>
              <div><span className="text-xs text-gray-400">{t('customers.fieldInterest')}</span><p className="text-sm">{c.interest_model || '-'}</p></div>
              <div><span className="text-xs text-gray-400">{t('common.budget')}</span><p className="text-sm">{c.budget > 0 ? c.budget + t('customers.wan') : '-'}</p></div>
              <div><span className="text-xs text-gray-400">{t('customers.fieldIntent')}</span><p className="text-sm">{((c.intent_score || 0) * 100).toFixed(0)}%</p></div>
              <div><span className="text-xs text-gray-400">{t('common.city')}</span><p className="text-sm">{c.city || '-'}</p></div>
              <div><span className="text-xs text-gray-400">{t('common.stage')}</span><p className="text-sm">{STAGE_KEYS[c.journey_stage] ? t(STAGE_KEYS[c.journey_stage]) : (c.journey_stage || '-')}</p></div>
              <div><span className="text-xs text-gray-400">{t('common.advisor')}</span><p className="text-sm">{detail.assigned_user_name || (c.assigned_user_id > 0 ? t('common.advisor') + c.assigned_user_id : t('common.unassigned'))}</p></div>
            </div>
            {/* P1-9：标签改用权威 custTags（GET /customers/:id/tags），chip 带 × 直删 */}
            <div><span className="text-xs text-gray-400">{t('common.tags')}</span><div className="mt-1 flex flex-wrap gap-1">{(custTags.length ? custTags : (detail.tags || [])).map((t2: TableRowData, i: number) => <Tag key={i} theme="primary" variant="light" closable={custTags.length > 0 && !!t2.tag_id} onClose={() => t2.tag_id && void removeTag(Number(t2.tag_id))}>{t2.tag_name}</Tag>)} {(custTags.length ? custTags : (detail.tags || [])).length === 0 && <span className="text-xs text-gray-300">{t('customers.none')}</span>}</div></div>
            <div><span className="text-xs text-gray-400">{t('common.remark')}</span><p className="text-sm text-gray-600 mt-1">{c.remark || '-'}</p></div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="success" variant="outline" onClick={() => void loadTd()}>{showTd ? t('customers.hideTestDrive') : t('customers.showTestDrive')}</Button>
              <div className="mt-2 space-y-2">
                {showTd && testDrives.length === 0 && <div className="text-gray-400 text-xs">{t('customers.noTestDrive')}</div>}
                {testDrives.map((td, i) => (
                  <div key={i} className="bg-gray-50 rounded-lg p-2 border border-gray-100">
                    <div className="flex items-center justify-between"><span className="text-xs font-medium text-gray-700">{td.model_name || t('customers.testDrive')}</span><span className="text-[10px] text-gray-500">{td.status === 'pending' ? t('customers.tdPending') : td.status === 'completed' ? t('customers.tdCompleted') : td.status === 'cancelled' ? t('customers.tdCancelled') : td.status}</span></div>
                    <p className="text-xs text-gray-400 mt-0.5">{td.scheduled_at ? new Date(td.scheduled_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''} {td.location ? '· ' + td.location : ''}</p>
                  </div>
                ))}
              </div>
            </div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="primary" variant="outline" onClick={() => void loadChat()}>{showChat ? t('customers.hideChat') : t('customers.showChat')}</Button>
              <div className="mt-2 max-h-48 overflow-y-auto bg-gray-50 rounded-lg p-3 text-xs space-y-1">
                {showChat && chat.length === 0 && <div className="text-gray-400">{t('customers.noChat')}</div>}
                {chat.map((m, i) => <div key={i}><span className={`${wcolor(m.sender_type)} font-medium`}>[{who(m.sender_type)}]</span> {m.content}</div>)}
              </div>
            </div>
            {/* P1-9：历史会话时间线——会话列表（GET /customers/:id/conversations）+ 点开单会话消息（GET /conversations/:id/messages） */}
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="primary" variant="outline" onClick={() => void loadConvs()}>{showConvs ? t('customers.hideConvs') : t('customers.showConvs')}</Button>
              {showConvs && (
                <div className="mt-2 space-y-1">
                  {convs.length === 0 && <div className="text-gray-400 text-xs">{t('customers.noConvs')}</div>}
                  {convs.map((cv) => (
                    <div key={cv.id} className="bg-gray-50 rounded-lg border border-gray-100">
                      <div className="flex items-center justify-between px-2 py-1.5 cursor-pointer" onClick={() => void toggleConv(Number(cv.id))}>
                        <span className="text-xs text-gray-600">{t('customers.convMeta', { id: cv.id, channel: cv.channel || 'web', status: cv.status === 'active' ? t('common.active') : cv.status === 'closed' ? t('common.closed') : (cv.status || '-') })}</span>
                        <span className="text-[10px] text-gray-400">{openConv === cv.id ? t('common.collapse') : t('common.expand')}</span>
                      </div>
                      {openConv === cv.id && (
                        <div className="max-h-40 overflow-y-auto px-2 pb-2 text-xs space-y-1">
                          {convMsgs.length === 0 && <div className="text-gray-400">{t('common.noMessages')}</div>}
                          {convMsgs.map((m, i) => <div key={i}><span className={`${wcolor(m.sender_type)} font-medium`}>[{who(m.sender_type)}]</span> {m.content}</div>)}
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </Drawer>

      <Dialog header={t('customers.editTitle')} visible={editVisible} onClose={() => setEditVisible(false)} onConfirm={() => void submitEdit()} confirmBtn={saving ? t('crud.saving') : t('crud.save')} width="680px">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.fullName')}</label><Input value={form.name} onChange={(v) => setField('name', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.phoneShort')}</label><Input value={form.phone} onChange={(v) => setField('phone', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.city')}</label><Input value={form.city} onChange={(v) => setField('city', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.fieldInterest')}</label><Input value={form.interest_model} onChange={(v) => setField('interest_model', v)} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.budgetWan')}</label><InputNumber value={Number(form.budget || 0)} onChange={(v) => setField('budget', v)} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.intentRange')}</label><InputNumber value={Number(form.intent_score || 0)} onChange={(v) => setField('intent_score', v)} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.stage')}</label><Select value={form.journey_stage} onChange={(v) => setField('journey_stage', v)} options={stageOptions} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.fieldOwner')}</label><Select value={Number(form.assigned_user_id || 0)} onChange={(v) => setField('assigned_user_id', v)} options={users} filterable style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.status')}</label><Select value={Number(form.status)} onChange={(v) => setField('status', v)} options={statusEditOptions} style={{ width: '100%' }} /></div>
          <div className="md:col-span-2"><label className="block text-xs text-gray-500 mb-1">{t('common.remark')}</label><Textarea value={form.remark} onChange={(v) => setField('remark', v)} autosize={{ minRows: 2, maxRows: 5 }} /></div>
        </div>
      </Dialog>

      <Dialog header={t('customers.manageTags')} visible={tagVisible} onClose={() => setTagVisible(false)} onConfirm={() => void submitTags()} confirmBtn={saving ? t('crud.saving') : t('crud.save')} width="620px">
        <Select
          value={tagForm}
          onChange={(v) => setTagForm(Array.isArray(v) ? v.map(String) : [])}
          options={tagOptions}
          multiple
          filterable
          clearable
          placeholder={t('customers.tagPlaceholder')}
          style={{ width: '100%' }}
        />
        <p className="text-xs text-gray-400 mt-2">{t('customers.tagOverwriteHint')}</p>
      </Dialog>
      <Dialog header={t('customers.createTitle')} visible={createVisible} onClose={() => setCreateVisible(false)} onConfirm={() => void submitCreate()} confirmBtn={saving ? t('deals.submitting') : t('deals.create')} width="620px">
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.fullName')}</label><Input value={create.name} onChange={(v) => setCreate((s) => ({ ...s, name: v }))} placeholder={t('customers.phVisitorName')} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.mobilePhone')}</label><Input value={create.phone} onChange={(v) => setCreate((s) => ({ ...s, phone: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.city')}</label><Input value={create.city} onChange={(v) => setCreate((s) => ({ ...s, city: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.fieldInterest')}</label><Input value={create.interest_model} onChange={(v) => setCreate((s) => ({ ...s, interest_model: v }))} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.budgetWan')}</label><InputNumber value={Number(create.budget || 0)} onChange={(v) => setCreate((s) => ({ ...s, budget: v }))} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('common.source')}</label><Select value={create.source} onChange={(v) => setCreate((s) => ({ ...s, source: v }))} options={sourceOptions} style={{ width: '100%' }} /></div>
          <div><label className="block text-xs text-gray-500 mb-1">{t('customers.fieldOwner')}</label><Select value={Number(create.assigned_user_id || 0)} onChange={(v) => setCreate((s) => ({ ...s, assigned_user_id: v }))} options={users} filterable style={{ width: '100%' }} /></div>
          <div className="md:col-span-2"><label className="block text-xs text-gray-500 mb-1">{t('common.remark')}</label><Textarea value={create.remark} onChange={(v) => setCreate((s) => ({ ...s, remark: v }))} autosize={{ minRows: 2, maxRows: 4 }} /></div>
        </div>
        <p className="text-xs text-gray-400 mt-2">{t('customers.createHint')}</p>
      </Dialog>
    </div>
  )
}
