// 商机管道看板（商机批 · 前端批1，2026-09-24）：把"这个客户跟到哪一步了"从顾问脑子里的
// 一笔糊涂账，变成一格一格能点开的数字。
//
// 三条页面纪律，都是后端口径的镜像，改任何一条都会让看板和名单对不上：
//  1. **格子字符串不自己拼**。下钻用的 filter 一律取后端 `drill_filter` 字段原样回传——
//     前端写 `"stage:" + s` 拼错一个字母，就是一格永远点开是空的，而且页面看起来完全正常。
//  2. **中文名一律读 config**。阶段名/分组名/流失原因/报价状态都由后端 config 下发，
//     前端再写一份枚举，早晚出现"看板叫商务谈判、下钻标题叫谈判中"。
//  3. **金额只在输入框里是元，中间步骤全是分**。报价是钱，任何一次浮点往返都可能
//     让 0.1+0.2 变成 0.30000000000000004 落进库；合计更是**只读服务端返回值**，
//     前端算的那份不作数（后端按明细重算，两边不一致时以库为准）。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
// 金额换算与原因码文案都在 lib 里（见 ../../lib/money、../../lib/dealReasons）：
// 后台看板和顾问台开单是同一个业务动作的两张脸，两份四舍五入/两套话术迟早对不上。
import { AUTH } from '../../lib/api'
import { fenToYuan, fenToYuanInput, yuanToFen } from '../../lib/money'
import { dealReasonText } from '../../lib/dealReasons'
import type {
  DealBoardResp, DealConfig, DealDetailResp, DealDrillResp, DealQuoteDetail,
  DealQuoteResp, DealRow,
} from '../../types'

type ApiResp<T> = { code: number; message?: string; reason?: string; data?: T }

/** 一个可下钻的格子：filter 直接用后端下发的 drill_filter，label 用后端下发的中文名。 */
type Cell = { filter: string; label: string; count: number; amount_cents: number }

/** 打开商机详情的入口参数（从看板格子或列表行都能触发）。 */
type OpenDeal = { id: number; title: string }

const DAYS_OPTIONS = [7, 30, 90, 365].map((d) => ({ label: `近 ${d} 天`, value: String(d) }))

/** 商机管道 Tab 主体：看板格子 → 下钻名单 → 建单 / 推进 / 报价。 */
export function DealsTab() {
  const [board, setBoard] = useState<DealBoardResp | null>(null)
  const [days, setDays] = useState('30')
  const [stuckDays, setStuckDays] = useState('7')
  // loading 由触发方置真（筛选/刷新/写后回读），effect 只负责收回——
  // 在 effect 里同步 setState 会多跑一轮级联渲染，lint 按这条记账
  const [loading, setLoading] = useState(true)
  const [cell, setCell] = useState<Cell | null>(null)
  const [createOpen, setCreateOpen] = useState(false)
  const [openDeal, setOpenDeal] = useState<OpenDeal | null>(null)

  // 阶段名/分组名/流失原因/报价状态全部从这份 config 取，前端不另写枚举
  const cfg: DealConfig | null = board?.config ?? null

  const load = useCallback(async () => {
    const j = (await AUTH(`/api/v1/admin/deals/board?days=${days}&stuck_days=${stuckDays}`)) as ApiResp<DealBoardResp> | null
    if (j?.code === 0) setBoard(j.data ?? null)
    else MessagePlugin.error(j?.message || '商机看板加载失败')
    setLoading(false)
  }, [days, stuckDays])

  useEffect(() => { void (async () => { await load() })() }, [load])

  /** 主动刷新：触发方持有 loading。 */
  const reload = () => { setLoading(true); void load() }

  /** 看板 4 个分组格（在途/停滞/赢单/输单），filter 与金额都由后端算好直出。 */
  const groupCells: Cell[] = board ? [
    { filter: 'open', label: cfg?.filter_labels?.open || '在途', count: board.open_count, amount_cents: board.open_amount_cents },
    { filter: 'stuck', label: cfg?.filter_labels?.stuck || '停滞', count: board.stuck_count, amount_cents: board.stuck_amount_cents },
    { filter: 'won', label: cfg?.filter_labels?.won || '赢单', count: board.won_count, amount_cents: board.won_amount_cents },
    { filter: 'lost', label: cfg?.filter_labels?.lost || '输单', count: board.lost_count, amount_cents: 0 },
  ] : []

  /** 看板 6 个阶段格：后端已把 won/lost 排在阶段序列末尾，drill_filter 原样带。 */
  const stageCells: Cell[] = (board?.stages || []).map((s) => ({
    filter: s.drill_filter, label: s.stage_name, count: s.count, amount_cents: s.amount_cents,
  }))

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
        <div className="text-[13px] text-gray-500">
          一个客户同时只能有一张在途商机；<strong>成交或流失后这单就定版</strong>，
          再跟是开新单，不改历史。报价发出去即锁死，要调价就出新版——报价单是客户手里的承诺凭证，
          改已发出的那张等于伪造。
        </div>
        <div className="flex flex-wrap gap-2 items-center">
          <Select value={days} onChange={(v) => { setDays(String(v)); setCell(null) }} options={DAYS_OPTIONS} style={{ width: 120 }} size="small" />
          <span className="text-xs text-gray-500">停滞阈值</span>
          <Select value={stuckDays} onChange={(v) => { setStuckDays(String(v)); setCell(null) }}
            options={[3, 7, 14, 30].map((d) => ({ label: `${d} 天未动`, value: String(d) }))} style={{ width: 130 }} size="small" />
          <Button theme="default" variant="outline" onClick={reload} disabled={loading}>{loading ? '加载中…' : '刷新'}</Button>
          <Button theme="primary" onClick={() => setCreateOpen(true)}>+ 新建商机</Button>
          {board && (
            <span className="text-xs text-gray-500">
              赢单率 <strong>{board.win_rate_pct}%</strong>
              <span className="text-gray-400">（分母只算已终局的 {board.won_count + board.lost_count} 单）</span>
            </span>
          )}
        </div>
        {board?.truncated && (
          <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
            单量超出一次扫描上限，<strong>下面的数字比真实值小</strong>（后端如实标注，不静默少算）。
            请缩小时间窗再看，或走「导出」拿全量。
          </div>
        )}
        {board?.note && <div className="text-xs text-gray-400">{board.note}</div>}
      </div>

      {board && (
        <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
          <div className="text-[13px] text-gray-500">分组（点一格看是哪些客户，名单条数与格子数字必然相同）</div>
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3">
            {groupCells.map((c) => <BoardCell key={c.filter} cell={c} active={cell?.filter === c.filter} onPick={setCell} />)}
          </div>
          <div className="text-[13px] text-gray-500 pt-2">按阶段</div>
          <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
            {stageCells.map((c) => <BoardCell key={c.filter} cell={c} active={cell?.filter === c.filter} onPick={setCell} />)}
          </div>
        </div>
      )}

      {cell
        ? <DealDrill key={`${cell.filter}:${days}:${stuckDays}`} cell={cell} days={Number(days)} stuckDays={Number(stuckDays)}
            onBack={() => setCell(null)} onOpen={(d) => setOpenDeal(d)} />
        : (
          <div className="bg-white rounded-lg shadow-sm p-6 text-[13px] text-gray-400">
            点上面任意一格，这里就展开对应的客户名单。
          </div>
        )}

      {createOpen && cfg && <CreateDealDialog cfg={cfg} onClose={() => setCreateOpen(false)} onDone={() => { setCreateOpen(false); reload() }} onOpen={setOpenDeal} />}
      {openDeal && cfg && (
        <DealDetailDialog key={`${openDeal.id}:v${board?.days ?? 0}`} id={openDeal.id} title={openDeal.title} cfg={cfg}
          onClose={() => setOpenDeal(null)} onChanged={reload} />
      )}
    </div>
  )
}

/** 看板一格：数字 + 金额 + 点击下钻。`role="button"` 是 playwright 认这个格子真挂了点击的锚点。 */
function BoardCell({ cell, active, onPick }: { cell: Cell; active: boolean; onPick: (c: Cell) => void }) {
  return (
    <div role="button" tabIndex={0} data-testid={`deal-cell-${cell.filter}`}
      className={`rounded-lg border p-3 cursor-pointer transition-colors ${active ? 'border-indigo-500 bg-indigo-50' : 'border-gray-200 hover:border-indigo-300'}`}
      onClick={() => onPick(cell)} onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onPick(cell) } }}>
      <div className="text-xs text-gray-500 truncate">{cell.label}</div>
      <div className="text-2xl font-semibold leading-tight" data-testid="deal-cell-count">{cell.count}</div>
      <div className="text-[11px] text-gray-400">{cell.amount_cents ? `¥ ${fenToYuan(cell.amount_cents)}` : '—'}</div>
    </div>
  )
}

/** 下钻名单：横幅 total 与看板格子数字逐字相同（两侧共用后端同一份谓词，不是两边各写对一次）。 */
function DealDrill({ cell, days, stuckDays, onBack, onOpen }: {
  cell: Cell; days: number; stuckDays: number; onBack: () => void; onOpen: (d: OpenDeal) => void
}) {
  const [data, setData] = useState<DealDrillResp | null>(null)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)

  // 换格子由父层用 key 整块重挂载（页码自然回第 1 页），此处只跟请求
  useEffect(() => {
    void (async () => {
      const j = (await AUTH(`/api/v1/admin/deals?filter=${encodeURIComponent(cell.filter)}&days=${days}&stuck_days=${stuckDays}&page=${page}&page_size=20`)) as ApiResp<DealDrillResp> | null
      if (j?.code === 0) setData(j.data ?? null)
      else MessagePlugin.error(j?.message || '商机名单读取失败')
      setLoading(false)
    })()
  }, [cell.filter, days, stuckDays, page])

  const goto = (p: number) => { setLoading(true); setPage(p) }

  const cols = DealListColumns(onOpen)

  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[13px]">
          <strong>{cell.label}</strong>
          <span className="text-gray-500"> · 近 {days} 天 · 停滞阈值 {stuckDays} 天 </span>
          {data ? <>共 <strong data-testid="deal-drill-total">{data.total}</strong> 张商机（{data.label}）</> : <span className="text-gray-400">加载中…</span>}
        </div>
        <Button size="small" variant="outline" onClick={onBack}>返回看板</Button>
      </div>
      {data?.truncated && <div className="text-xs text-orange-700">单量超出扫描上限，此处数字偏小（后端如实标注）。</div>}
      {data?.note && <div className="text-xs text-gray-400">{data.note}</div>}
      <Table rowKey="id" data={data?.list || []} columns={cols} size="small" loading={loading} empty="这一格还没有商机" />
      {data && (
        <div className="flex items-center gap-3 text-xs text-gray-500">
          <Button size="small" variant="text" disabled={page <= 1} onClick={() => goto(page - 1)}>上一页</Button>
          <span>第 {page} 页 / 共 {Math.max(1, Math.ceil(data.total / data.page_size))} 页</span>
          <Button size="small" variant="text" disabled={page * data.page_size >= data.total} onClick={() => goto(page + 1)}>下一页</Button>
        </div>
      )}
    </div>
  )
}

/** 商机列表列定义（看板下钻名单与建单后的回读共用一套，避免两处表头漂移）。 */
function DealListColumns(onOpen: (d: OpenDeal) => void) {
  return [
    { colKey: 'customer_name', title: '客户', width: 150, cell: (p: { row: DealRow }) => (
      <div>
        <div className="text-[13px]">{p.row.customer_name || `客户 #${p.row.customer_id}`}</div>
        <div className="text-[11px] text-gray-400">{p.row.customer_phone || p.row.source_code || ''}</div>
      </div>
    ) },
    { colKey: 'title', title: '商机', width: 170, cell: (p: { row: DealRow }) => (
      <Button variant="text" size="small" className="!px-0 text-left" onClick={() => onOpen({ id: p.row.id, title: p.row.title })}>{p.row.title}</Button>
    ) },
    { colKey: 'stage_name', title: '阶段', width: 100, cell: (p: { row: DealRow }) => (
      <Tag variant="light" theme={p.row.stage === 'won' ? 'success' : p.row.stage === 'lost' ? 'danger' : 'primary'}>{p.row.stage_name}</Tag>
    ) },
    { colKey: 'amount_cents', title: '金额(元)', width: 120, cell: (p: { row: DealRow }) => (
      <span className="text-[13px] tabular-nums">{fenToYuan(p.row.amount_cents)}</span>
    ) },
    { colKey: 'stalled_days', title: '停滞', width: 80, cell: (p: { row: DealRow }) => (
      p.row.stage === 'won' || p.row.stage === 'lost' ? <span className="text-[12px] text-gray-400">—</span>
        : <span className={`text-[13px] ${p.row.stalled_days >= 7 ? 'text-orange-600' : ''}`}>{p.row.stalled_days} 天</span>
    ) },
    { colKey: 'quote_count', title: '报价', width: 90, cell: (p: { row: DealRow }) => (
      <span className="text-[13px]">{p.row.quote_count} 版{p.row.open_quote ? <span className="text-gray-400">（{p.row.open_quote.status_name}）</span> : null}</span>
    ) },
    { colKey: 'owner_name', title: '归属', width: 100, cell: (p: { row: DealRow }) => p.row.owner_name || <span className="text-gray-400">未分配</span> },
    { colKey: 'op', title: '操作', width: 90, cell: (p: { row: DealRow }) => (
      <Button size="small" variant="text" onClick={() => onOpen({ id: p.row.id, title: p.row.title })}>详情</Button>
    ) },
  ]
}

/** 建单表单的初始值。金额留空而不是 0：0 是"这单免费"，空是"还没谈到钱"。 */
function emptyDealForm() {
  return { customer_id: '', title: '', stage: 'lead', amount_yuan: '', source: 'manual', expected_close_at: '' }
}

/** 新建商机弹窗：一个客户一在途单，撞了后端回 deal_already_open，此处把用户带去原单。 */
function CreateDealDialog({ cfg, onClose, onDone, onOpen }: {
  cfg: DealConfig; onClose: () => void; onDone: () => void; onOpen: (d: OpenDeal) => void
}) {
  const [form, setForm] = useState(emptyDealForm())
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // 建单只给在途阶段：终局两态是"推进"的结果，不是开单的起点
  const openStages = cfg.stages.filter((s) => s !== 'won' && s !== 'lost')

  async function submit() {
    const cid = Number(form.customer_id.trim())
    if (!Number.isInteger(cid) || cid <= 0) { setErr('请填写正确的客户 ID（正整数）'); return }
    const amount = form.amount_yuan.trim() === '' ? 0 : yuanToFen(form.amount_yuan)
    if (amount === null) { setErr('金额格式不对：最多两位小数，别带单位'); return }
    if (amount > cfg.max_amount_cents) { setErr(`金额超出允许上限（¥ ${fenToYuan(cfg.max_amount_cents)}）`); return }
    setBusy(true)
    setErr('')
    const j = (await AUTH('/api/v1/admin/deals', {
      method: 'POST',
      body: {
        customer_id: cid, title: form.title.trim(), stage: form.stage,
        amount_cents: amount, source: form.source, expected_close_at: form.expected_close_at,
      },
    })) as ApiResp<{ deal: DealRow }> | null
    setBusy(false)
    if (j?.code !== 0) {
      const text = dealReasonText(j?.reason, j?.message || '建单失败')
      // 已有在途单不该让用户再撞一次：直接把原单开出来
      if (j?.reason === 'deal_already_open') { MessagePlugin.warning(text); onDone(); return }
      setErr(text)
      return
    }
    MessagePlugin.success('商机已创建')
    const d = j.data?.deal
    onDone()
    if (d) onOpen({ id: d.id, title: d.title })
  }

  return (
    <Dialog header="新建商机" visible onClose={onClose} onConfirm={() => void submit()}
      confirmBtn={busy ? '提交中…' : '创建'} width="560px">
      <div className="space-y-3">
        <div>
          <label className="block text-xs text-gray-500 mb-1">客户 ID（必填，一个客户同时只能有一张在途单）</label>
          <Input value={form.customer_id} onChange={(v) => setForm({ ...form, customer_id: String(v) })} placeholder="如 1024" />
        </div>
        <div>
          <label className="block text-xs text-gray-500 mb-1">商机标题（如「极石 01 四驱版 · 置换」）</label>
          <Input value={form.title} onChange={(v) => setForm({ ...form, title: String(v) })} maxlength={80} placeholder="给复盘时看的名字" />
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">起始阶段</label>
            <Select value={form.stage} onChange={(v) => setForm({ ...form, stage: String(v) })} style={{ width: '100%' }}
              options={openStages.map((s) => ({ label: cfg.stage_names[s] || s, value: s }))} />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">金额（元，可留空）</label>
            <Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder="还没谈到钱就空着" />
          </div>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">来源</label>
            <Select value={form.source} onChange={(v) => setForm({ ...form, source: String(v) })} style={{ width: '100%' }}
              options={cfg.sources.map((s) => ({ label: s, value: s }))} />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">预计成交日（可选）</label>
            <input type="date" aria-label="预计成交日" value={form.expected_close_at} className="w-full px-3 py-2 border rounded-lg text-sm"
              onChange={(e) => setForm({ ...form, expected_close_at: e.target.value })} />
          </div>
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
        <p className="text-xs text-gray-400">
          成交与流失只能在单子上「推进」得出：标成交必须带金额，标流失必须选原因——不然月底这张表说不清钱从哪来、单为什么丢。
        </p>
      </div>
    </Dialog>
  )
}

/**
 * 商机详情：主档 + 推进 + 报价版本链。
 *
 * 每次写操作后都<strong>整块回读</strong>（后端所有写接口返回的是落库后的真实形态）——
 * 拿提交时的表单去更新界面，就会出现"金额明明被服务端按明细重算了，界面还显示我输入的那份"。
 */
function DealDetailDialog({ id, title, cfg, onClose, onChanged }: {
  id: number; title: string; cfg: DealConfig; onClose: () => void; onChanged: () => void
}) {
  const [detail, setDetail] = useState<DealDetailResp | null>(null)
  const [loading, setLoading] = useState(true)
  const [err, setErr] = useState('')
  const [quoteView, setQuoteView] = useState<DealQuoteDetail | null>(null)
  // editId 非空 = 改一张已有草稿；空 = 在当前商机下另出一版
  const [quoteEdit, setQuoteEdit] = useState<{ editId?: number } | null>(null)

  const reload = useCallback(async () => {
    const j = (await AUTH(`/api/v1/admin/deals/${id}`)) as ApiResp<DealDetailResp> | null
    if (j?.code === 0) setDetail(j.data ?? null)
    else setErr(dealReasonText(j?.reason, j?.message || '商机详情读取失败'))
    setLoading(false)
  }, [id])

  useEffect(() => { void (async () => { setLoading(true); setErr(''); await reload() })() }, [reload])

  const deal = detail?.deal ?? null
  const closed = !!deal && (deal.stage === 'won' || deal.stage === 'lost')

  /** 统一的动作提交：成功回读整块、失败按稳定原因码给话术。 */
  async function act(path: string, init: Parameters<typeof AUTH>[1], okText: string): Promise<boolean> {
    setErr('')
    const j = (await AUTH(path, init)) as ApiResp<unknown> | null
    if (j?.code !== 0) { setErr(dealReasonText(j?.reason, j?.message || '操作未完成')); return false }
    MessagePlugin.success(okText)
    await reload()
    onChanged()
    return true
  }

  return (
    <Dialog header={`商机详情 · ${deal?.title || title}`} visible onClose={onClose} footer={
      <div className="flex justify-end"><Button theme="default" variant="outline" onClick={onClose}>关闭</Button></div>
    } width="860px">
      {loading && <div className="py-10 text-center text-[13px] text-gray-400">加载中…</div>}
      {!loading && !deal && <div className="py-10 text-center text-[13px] text-red-600">{err || '商机不存在或无权查看'}</div>}
      {deal && (
        <div className="space-y-4">
          {err && <div className="text-[13px] text-red-600 bg-red-50 border border-red-200 rounded px-3 py-2">{err}</div>}
          <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-[13px]">
            <Field label="客户" value={deal.customer_name || `#${deal.customer_id}`} />
            <Field label="归属顾问" value={deal.owner_name || '未分配'} />
            <Field label="当前阶段" value={`${deal.stage_name}${closed ? '（已定版）' : ` · 停滞 ${deal.stalled_days} 天`}`} />
            <Field label="金额" value={`¥ ${fenToYuan(deal.amount_cents)}`} />
            <Field label="来源" value={deal.source || '—'} />
            <Field label="预计成交" value={deal.expected_close_at || '未定'} />
            <Field label="建档" value={deal.created_at} />
            <Field label={deal.stage === 'lost' ? '流失原因' : '成交时间'} value={deal.stage === 'lost' ? (deal.lost_reason_name || deal.lost_reason) : (deal.won_at || '—')} />
          </div>

          {!closed && <MovePanel deal={deal} cfg={cfg} onAct={act} />}

          <div>
            <div className="text-[13px] text-gray-600 mb-2 flex items-center justify-between">
              <span>报价版本链（{deal.quote_count} 版，<strong>已发出的锁死</strong>，调价只能出新版）</span>
              <Button size="small" theme="primary" variant="outline" onClick={() => setQuoteEdit({})}>
                {deal.open_quote ? '另出一版' : '新建报价'}
              </Button>
            </div>
            <Table rowKey="id" size="small" data={detail?.quotes || []} empty="还没有报价单" columns={[
              { colKey: 'version', title: '版本', width: 70, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => `v${p.row.version}` },
              { colKey: 'status_name', title: '状态', width: 100, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => (
                <Tag variant="light" theme={p.row.status === 'accepted' ? 'success' : p.row.status === 'sent' ? 'primary' : p.row.status === 'void' ? 'danger' : 'default'}>
                  {p.row.status_name || cfg.quote_statuses[p.row.status] || p.row.status}
                </Tag>
              ) },
              { colKey: 'total_cents', title: '合计(元)', width: 120, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => (
                <span className="tabular-nums">{fenToYuan(p.row.total_cents)}</span>
              ) },
              { colKey: 'valid_until', title: '有效期至', width: 130, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => p.row.valid_until || '不限' },
              { colKey: 'sent_at', title: '发出时间', width: 160, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => p.row.sent_at || '—' },
              { colKey: 'op', title: '操作', width: 150, cell: (p: { row: NonNullable<DealDetailResp['quotes']>[number] }) => (
                <div className="flex gap-1">
                  <Button size="small" variant="text" onClick={() => void openQuote(p.row.id)}>看明细</Button>
                  {p.row.status === 'draft' && (
                    <>
                      <Button size="small" variant="text" onClick={() => setQuoteEdit({ editId: p.row.id })}>编辑</Button>
                      <Button size="small" variant="text" onClick={() => void act(`/api/v1/admin/quotes/${p.row.id}/send`, { method: 'POST' }, '报价已发出并锁死')}>发出</Button>
                    </>
                  )}
                  {p.row.status === 'sent' && (
                    <>
                      <Button size="small" variant="text" onClick={() => void act(`/api/v1/admin/quotes/${p.row.id}/accept`, { method: 'POST' }, '已记为接受（记得再把商机推到成交）')}>客户已接受</Button>
                      <Button size="small" variant="text" theme="warning" onClick={() => void act(`/api/v1/admin/quotes/${p.row.id}/decline`, { method: 'POST' }, '已记为拒绝')}>拒绝</Button>
                      <Button size="small" variant="text" theme="danger" onClick={() => void act(`/api/v1/admin/quotes/${p.row.id}/void`, { method: 'POST' }, '该版已作废')}>作废</Button>
                    </>
                  )}
                </div>
              ) },
            ]} />
            <div className="text-xs text-gray-400 mt-1">
              「客户已接受」<strong>不等于成交</strong>：接受只说明对方认了这张单子，
              钱真正落定要在上面把商机推进到「成交」并填成交金额——两步分开，防止点了接受就当收入入账。
            </div>
          </div>
        </div>
      )}
      {quoteView && <QuoteViewDialog quote={quoteView} cfg={cfg} onClose={() => setQuoteView(null)} />}
      {quoteEdit && deal && (
        <QuoteEditDialog dealId={deal.id} quoteId={quoteEdit.editId} cfg={cfg}
          onClose={() => setQuoteEdit(null)}
          onDone={() => { setQuoteEdit(null); void reload(); onChanged() }} />
      )}
    </Dialog>
  )

  /** 打开一版报价的明细（明细行只在详情接口给，列表不下发正文）。 */
  async function openQuote(qid: number) {
    const j = (await AUTH(`/api/v1/admin/quotes/${qid}`)) as ApiResp<DealQuoteResp> | null
    if (j?.code === 0 && j.data?.quote) setQuoteView(j.data.quote)
    else MessagePlugin.error(j?.message || '报价明细读取失败')
  }
}

/** 一格只读字段。 */
function Field({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="text-[11px] text-gray-400">{label}</div>
      <div className="text-[13px] break-all">{value || '—'}</div>
    </div>
  )
}

/**
 * 推进面板：往哪个阶段走、带不带金额、流失选原因。
 *
 * 阶段下拉<strong>只列后端允许的目标</strong>——但"允许"的判据在后端（往深走、终局不可逆），
 * 前端只负责把当前阶段之后的候选排出来，真正被拒时按 stage_backward / deal_closed 给话术。
 */
function MovePanel({ deal, cfg, onAct }: {
  deal: DealRow; cfg: DealConfig; onAct: (path: string, init: Parameters<typeof AUTH>[1], okText: string) => Promise<boolean>
}) {
  const [to, setTo] = useState('')
  const [amountYuan, setAmountYuan] = useState('')
  const [changeAmount, setChangeAmount] = useState(false)
  const [lostReason, setLostReason] = useState('')
  const [busy, setBusy] = useState(false)

  const candidates = cfg.stages.filter((s) => s !== deal.stage)

  async function move() {
    if (!to) { MessagePlugin.warning('先选一个目标阶段'); return }
    let amount = 0
    if (to === 'won') {
      const v = yuanToFen(amountYuan)
      if (v === null || v <= 0) { MessagePlugin.error('标记成交必须填成交金额（元，最多两位小数，且大于 0）'); return }
      amount = v
    }
    if (to === 'lost' && !lostReason) { MessagePlugin.error('标记流失必须选一个原因'); return }
    setBusy(true)
    const okText = to === 'won' ? '已记为成交' : to === 'lost' ? '已记为流失' : `已推进到「${cfg.stage_names[to] || to}」`
    const ok = await onAct(`/api/v1/admin/deals/${deal.id}/move`, {
      method: 'POST',
      body: { to, amount_cents: amount, change_amount: changeAmount || to === 'won', lost_reason: lostReason },
    }, okText)
    setBusy(false)
    if (ok) { setTo(''); setAmountYuan(''); setLostReason(''); setChangeAmount(false) }
  }

  return (
    <div className="border border-gray-200 rounded-lg p-3 space-y-3">
      <div className="text-[13px] text-gray-600">推进这张单（往回退不允许，重开请先定版再建新单）</div>
      <div className="flex flex-wrap gap-2 items-end">
        <div style={{ minWidth: 160 }}>
          <label className="block text-xs text-gray-500 mb-1">推进到</label>
          <Select value={to} onChange={(v) => setTo(String(v ?? ''))} style={{ width: '100%' }} placeholder="选择目标阶段"
            options={candidates.map((s) => ({ label: cfg.stage_names[s] || s, value: s }))} />
        </div>
        {to === 'won' && (
          <div style={{ minWidth: 180 }}>
            <label className="block text-xs text-gray-500 mb-1">成交金额（元）</label>
            <Input value={amountYuan} onChange={(v) => setAmountYuan(String(v))} placeholder="必填" />
          </div>
        )}
        {to === 'lost' && (
          <div style={{ minWidth: 180 }}>
            <label className="block text-xs text-gray-500 mb-1">流失原因</label>
            <Select value={lostReason} onChange={(v) => setLostReason(String(v ?? ''))} style={{ width: '100%' }} placeholder="必选"
              options={Object.entries(cfg.lost_reasons).map(([k, label]) => ({ label: String(label), value: k }))} />
          </div>
        )}
        {to && to !== 'won' && to !== 'lost' && (
          <label className="text-xs text-gray-500 flex items-center gap-1 pb-2">
            <input type="checkbox" checked={changeAmount} onChange={(e) => setChangeAmount(e.target.checked)} />
            顺带改金额
          </label>
        )}
        {to && to !== 'won' && changeAmount && (
          <div style={{ minWidth: 160 }}>
            <label className="block text-xs text-gray-500 mb-1">新金额（元）</label>
            <Input value={amountYuan} onChange={(v) => setAmountYuan(String(v))} />
          </div>
        )}
        <Button theme="primary" loading={busy} onClick={() => void move()}>推进</Button>
      </div>
    </div>
  )
}

/** 报价明细只读视图（合计读服务端算的那份，前端不做二次求和）。 */
function QuoteViewDialog({ quote, cfg, onClose }: { quote: DealQuoteDetail; cfg: DealConfig; onClose: () => void }) {
  return (
    <Dialog header={`报价单 v${quote.version} · ${cfg.quote_statuses[quote.status] || quote.status_name || quote.status}`}
      visible onClose={onClose} footer={<div className="flex justify-end"><Button theme="default" variant="outline" onClick={onClose}>关闭</Button></div>} width="640px">
      <div className="space-y-3">
        <Table rowKey="name" size="small" data={quote.lines || []} empty="这张报价没有明细行" columns={[
          { colKey: 'name', title: '项目', width: 220 },
          { colKey: 'qty', title: '数量', width: 80, cell: (p: { row: DealQuoteDetail['lines'][number] }) => p.row.qty },
          { colKey: 'unit_cents', title: '单价(元)', width: 120, cell: (p: { row: DealQuoteDetail['lines'][number] }) => <span className="tabular-nums">{fenToYuan(p.row.unit_cents)}</span> },
          { colKey: 'total_cents', title: '小计(元)', width: 120, cell: (p: { row: DealQuoteDetail['lines'][number] }) => <span className="tabular-nums">{fenToYuan(p.row.total_cents)}</span> },
        ]} />
        <div className="flex items-center justify-between text-[13px]">
          <span className="text-gray-500">有效期至 {quote.valid_until || '不限'} · 发出 {quote.sent_at || '未发出'}</span>
          <span>合计 <strong className="tabular-nums">¥ {fenToYuan(quote.total_cents)}</strong>（服务端按明细算）</span>
        </div>
        {quote.note && <div className="text-[13px] text-gray-600 bg-gray-50 rounded p-2 whitespace-pre-wrap">{quote.note}</div>}
      </div>
    </Dialog>
  )
}

/** 报价编辑弹窗的入参：editId 非空=改已有草稿，空=在当前商机下新建一版。 */
type QuoteEditArgs = { dealId: number; quoteId?: number; cfg: DealConfig; onClose: () => void; onDone: () => void }

/** 报价单编辑器：明细行增删 + 有效期 + 「存草稿 / 存并发出」两个出口。 */
function QuoteEditDialog({ dealId, quoteId, cfg, onClose, onDone }: QuoteEditArgs) {
  const [lines, setLines] = useState<{ name: string; qty: string; unit_yuan: string }[]>([
    { name: '', qty: '1', unit_yuan: '' },
  ])
  const [note, setNote] = useState('')
  const [validUntil, setValidUntil] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // 编辑已有草稿时先回读一次明细：列表接口不下发 lines，拿列表行去填表会得到一张空单
  useEffect(() => {
    if (!quoteId) return
    void (async () => {
      const j = (await AUTH(`/api/v1/admin/quotes/${quoteId}`)) as ApiResp<DealQuoteResp> | null
      if (j?.code === 0 && j.data?.quote) {
        const q = j.data.quote
        setLines((q.lines || []).map((l) => ({ name: l.name, qty: String(l.qty), unit_yuan: fenToYuanInput(l.unit_cents) })))
        setNote(q.note || '')
        setValidUntil((q.valid_until || '').slice(0, 10))
      } else MessagePlugin.error(j?.message || '草稿读取失败')
    })()
  }, [quoteId])

  // 实时合计用分累加：每行先各自换算成分，再整数相加，不做浮点求和
  const totalFen = lines.reduce((sum, l) => {
    const unit = yuanToFen(l.unit_yuan) ?? 0
    const qty = Number(l.qty) || 0
    return sum + unit * qty
  }, 0)

  // 校验结果用「err 非空即失败」的单一形状，不用判别式联合：本仓 tsconfig 未开 strictNullChecks，
  // 布尔字面量当判别位收不窄（`if (!built.ok) built.err` 直接报类型错），两支合一支反而更好读。
  type Built = { err: string; body: Record<string, unknown> }
  const fail = (err: string): Built => ({ err, body: {} })

  function buildBody(send: boolean): Built {
    const built: { name: string; qty: number; unit_cents: number }[] = []
    for (const l of lines) {
      const name = l.name.trim()
      const qty = Number(l.qty)
      const unit = yuanToFen(l.unit_yuan)
      if (!name && (l.qty.trim() === '' || l.unit_yuan.trim() === '')) continue // 空行忽略
      if (!name) return fail('每一行都要有项目名称')
      if (!Number.isInteger(qty) || qty <= 0) return fail(`「${name}」数量得是正整数`)
      if (unit === null || unit < 0) return fail(`「${name}」单价格式不对（元，最多两位小数）`)
      built.push({ name, qty, unit_cents: unit })
    }
    if (built.length === 0) return fail('报价至少要有明细行')
    if (built.length > cfg.max_quote_lines) return fail(`明细行数超出上限 ${cfg.max_quote_lines} 行`)
    return {
      err: '',
      // set_valid 显式声明"要不要写有效期"：留空=不限，而不是"这次不改动它"
      body: { lines: built, note, send, valid_until: validUntil, set_valid: validUntil !== '' },
    }
  }

  async function submit(send: boolean) {
    setErr('')
    const built = buildBody(send)
    if (built.err !== '') { setErr(built.err); return }
    setBusy(true)
    const path = quoteId ? `/api/v1/admin/quotes/${quoteId}` : `/api/v1/admin/deals/${dealId}/quotes`
    const j = (await AUTH(path, { method: quoteId ? 'PUT' : 'POST', body: built.body })) as ApiResp<DealQuoteResp> | null
    setBusy(false)
    if (j?.code !== 0) { setErr(dealReasonText(j?.reason, j?.message || '报价保存失败')); return }
    MessagePlugin.success(send ? '报价已发出并锁死' : '草稿已保存')
    onDone()
  }

  return (
    <Dialog header={quoteId ? '编辑报价草稿' : '新建报价'} visible onClose={onClose} width="720px"
      footer={
        <div className="flex items-center justify-between w-full">
          <span className="text-[13px] text-gray-500">合计 <strong className="tabular-nums">¥ {fenToYuan(totalFen)}</strong>（保存后以服务端按明细重算的为准）</span>
          <div className="flex gap-2">
            <Button theme="default" variant="outline" onClick={onClose}>取消</Button>
            <Button theme="default" loading={busy} onClick={() => void submit(false)}>存草稿</Button>
            <Button theme="primary" loading={busy} onClick={() => void submit(true)}>保存并发出</Button>
          </div>
        </div>
      }>
      <div className="space-y-3">
        <div className="text-[13px] text-gray-500">
          明细行<strong>一旦发出就锁死</strong>：要改价请在此商机下另出一版，旧版会自动标为「已被新版取代」。
        </div>
        <div className="space-y-2">
          <div className="grid gap-2 text-xs text-gray-500" style={{ gridTemplateColumns: '1fr 90px 130px 60px' }}>
            <span>项目</span><span>数量</span><span>单价（元）</span><span></span>
          </div>
          {lines.map((l, i) => (
            <div key={i} className="grid gap-2" style={{ gridTemplateColumns: '1fr 90px 130px 60px' }}>
              <Input value={l.name} placeholder="如 极石 01 四驱版"
                onChange={(v) => setLines(lines.map((x, k) => (k === i ? { ...x, name: String(v) } : x)))} />
              <Input value={l.qty} onChange={(v) => setLines(lines.map((x, k) => (k === i ? { ...x, qty: String(v) } : x)))} />
              <Input value={l.unit_yuan} onChange={(v) => setLines(lines.map((x, k) => (k === i ? { ...x, unit_yuan: String(v) } : x)))} />
              <Button size="small" variant="text" theme="danger" disabled={lines.length <= 1}
                onClick={() => setLines(lines.length <= 1 ? lines : lines.filter((_, k) => k !== i))}>删</Button>
            </div>
          ))}
          <Button size="small" variant="outline" disabled={lines.length >= cfg.max_quote_lines}
            onClick={() => setLines([...lines, { name: '', qty: '1', unit_yuan: '' }])}>+ 加一行</Button>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">有效期至（可选）</label>
            <input type="date" aria-label="报价有效期" value={validUntil} className="w-full px-3 py-2 border rounded-lg text-sm"
              onChange={(e) => setValidUntil(e.target.value)} />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">备注 / 条款</label>
            <Textarea value={note} onChange={(v) => setNote(String(v))} rows={1} placeholder="如 含三年质保，不含上牌" />
          </div>
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
      </div>
    </Dialog>
  )
}
