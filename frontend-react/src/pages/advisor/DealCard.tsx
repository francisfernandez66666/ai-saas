// 客户详情·商机卡（商机批 · 前端批3，2026-09-24）：顾问在台面上问的那三个问题——
// 「这个客户跟过几单」「现在谈到哪一格」「要不要再出一版价」，以及开单 / 推进 / 出报价三个动作。
//
// 为什么这张卡只看当前客户、不做看板：管理端问的是整条管道健康度（/admin/deals/board），
// 顾问端问的是下一步做什么。两套口径混在一个组件里，早晚变成"顾问看到的数和管理员对不上"。
//
// 三条纪律（都是后端口径的镜像，见 internal/api/deal_advisor.go）：
//  1. **建单不传 source**。这单是谁带来的由后端按客户身上的活码判定（acquisition/manual），
//     是归因事实，不是填写人的自由发挥；前端传了也没有口子接。
//  2. **金额只在输入框里是元，提交体里全是分**（换算走 lib/money 单点，与后台看板同一份）。
//  3. **终局单照常列出、但不再给推进/报价**：三个月前流失那单被藏起来，顾问就会重复开一张
//     一模一样的单；而给它一个"推进"按钮，点下去必吃 deal_closed。
//
// 弹窗可见性由本组件自持（开哪张单、哪一版报价），数据与写请求留在父组件 pages/Advisor.tsx，
// 与 C2 拆分后的「视图持状态、卡片发意图」分层一致。
import { useState } from 'react'
import { Button, Dialog, Input, Textarea } from 'tdesign-react'
import type { DealConfig, DealRow } from '../../types'
import { fenToYuan, fenToYuanInput, yuanToFen } from '../../lib/money'

// DealCreateForm 开单表单（金额在界面是元，提交时由父组件转分；expected_close_at 留空=不承诺日期）
export type DealCreateForm = { title: string; stage: string; amount_yuan: string; expected_close_at: string }
// DealMoveForm 推进表单（to 必填；won 必带金额、lost 必带原因，判据在后端）
export type DealMoveForm = { to: string; amount_yuan: string; change_amount: boolean; lost_reason: string }
// DealQuoteForm 报价表单（lines 的单价是元；send=true 表示存完直接发出）
export type DealQuoteForm = { lines: { name: string; qty: string; unit_yuan: string }[]; note: string; valid_until: string }
// DealWriteResult 写操作回包：ok=false 时 message 是给顾问看的中文话术；
// dismiss=true 表示父组件已经把状态刷好了（如撞"已有在途单"），弹窗直接关掉别再留一张错表单
export type DealWriteResult = { ok: boolean; message: string; dismiss?: boolean }

// DealCardProps 商机卡入参：deals 为该客户名下全部商机（含终局，后端已按最新优先排）
export type DealCardProps = {
  deals: DealRow[]
  cfg: DealConfig | null
  onCreate: (form: DealCreateForm) => Promise<DealWriteResult>
  onMove: (dealId: number, form: DealMoveForm) => Promise<DealWriteResult>
  onQuote: (dealId: number, form: DealQuoteForm, send: boolean) => Promise<DealWriteResult>
}

// 弹窗内 label / 原生 date 输入的统一样式（与 dialogs.tsx 同一套移动风格，跨文件不便共用常量）
const LABEL_STYLE = { fontSize: 13, color: '#475569' }
const DATE_STYLE = { display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }
const ROW_STYLE = { marginTop: 8, paddingTop: 8, borderTop: '1px solid #f0f0f0' }
// CELL_INPUT 报价明细行里的原生小输入框（数量/单价），与 TDesign Input 同字号同行高
const CELL_INPUT = { width: '100%', padding: '6px 8px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14 }
const MINI_BTN = { background: 'none', border: '1px solid #e2e8f0', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }

// isClosed 商机是否已定版（成交/流失）：判据只有阶段码，后端同样按这两态锁死后续写操作
const isClosed = (stage: string) => stage === 'won' || stage === 'lost'

// DealCard 顾问工作台「商机」卡片：在途/历史单列表 + 开单、推进、出报价三个入口。
export default function DealCard({ deals, cfg, onCreate, onMove, onQuote }: DealCardProps) {
  const [createOpen, setCreateOpen] = useState(false)
  const [moveFor, setMoveFor] = useState<DealRow | null>(null)
  const [quoteFor, setQuoteFor] = useState<DealRow | null>(null)

  const open = deals.find((d) => !isClosed(d.stage))

  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <b style={{ fontSize: 13 }}>商机</b>
        {/* 一个客户同时只准一张在途单：有在途单时不再给"开单"入口，避免让人去撞后端那道拒 */}
        {!open && <button data-testid="deal-open-btn" onClick={() => setCreateOpen(true)} style={{ background: 'none', border: 'none', color: 'var(--pri)', fontSize: 12, cursor: 'pointer' }}>+ 开单</button>}
      </div>
      {deals.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>还没开过单。谈到钱就记一张，别留在聊天记录里。</div>}
      {open && <div data-testid="deal-open-hint" style={{ color: '#475569', marginTop: 6, fontSize: 12 }}>这个客户有一张在途商机，在原单上推进就行。</div>}
      {deals.map((d) => {
        const closed = isClosed(d.stage)
        return (
          <div key={d.id} data-testid={`deal-row-${d.id}`} style={ROW_STYLE}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ flex: 1, fontWeight: 600 }}>{d.title || `商机 #${d.id}`}</span>
              <span data-testid={`deal-stage-${d.id}`} style={{ fontSize: 11, padding: '1px 6px', borderRadius: 10, background: closed ? (d.stage === 'won' ? '#e8f9ef' : '#f1f5f9') : '#eef2ff', color: closed ? (d.stage === 'won' ? '#1f7a45' : '#64748b') : '#4338ca' }}>{d.stage_name}</span>
            </div>
            <div style={{ color: '#a0aec0', fontSize: 12, marginTop: 2 }}>
              ¥ {fenToYuan(d.amount_cents)}
              {/* 停滞天数只对在途单有意义：终局单再显示"停滞 0 天"会被读成"这单还活着" */}
              {!closed && <span> · 停 {d.stalled_days} 天</span>}
              {closed && d.stage === 'lost' && <span> · {d.lost_reason_name || d.lost_reason || '已流失'}</span>}
              {closed && d.stage === 'won' && <span> · {d.won_at || '已成交'}</span>}
              {d.quote_count > 0 && <span> · 报价 {d.quote_count} 版</span>}
            </div>
            {d.open_quote && (
              <div data-testid={`deal-quote-${d.id}`} style={{ fontSize: 12, marginTop: 2, color: '#475569' }}>
                当前报价 v{d.open_quote.version} · {d.open_quote.status_name} · ¥ {fenToYuan(d.open_quote.total_cents)}
                {d.open_quote.valid_until ? ` · 有效至 ${String(d.open_quote.valid_until).slice(0, 10)}` : ''}
              </div>
            )}
            {!closed && cfg && (
              <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                <button data-testid={`deal-move-${d.id}`} onClick={() => setMoveFor(d)} style={MINI_BTN}>推进</button>
                <button data-testid={`deal-newquote-${d.id}`} onClick={() => setQuoteFor(d)} style={MINI_BTN}>{d.open_quote ? '另出一版报价' : '出报价'}</button>
              </div>
            )}
          </div>
        )
      })}

      {createOpen && cfg && <CreateDealDialog cfg={cfg} onClose={() => setCreateOpen(false)} onSubmit={onCreate} />}
      {moveFor && cfg && <MoveDealDialog deal={moveFor} cfg={cfg} onClose={() => setMoveFor(null)} onSubmit={(f) => onMove(moveFor.id, f)} />}
      {quoteFor && cfg && <QuoteDealDialog deal={quoteFor} cfg={cfg} onClose={() => setQuoteFor(null)} onSubmit={(f, send) => onQuote(quoteFor.id, f, send)} />}
    </div>
  )
}

// CreateDealDialog 开一张新商机：标题 + 起始阶段 + 金额（元）+ 预计成交日。
// 起始阶段只列在途阶段——成交/流失是"推进"的结果，不是开单的起点；表单里没有来源字段（见文件头纪律 1）。
function CreateDealDialog({ cfg, onClose, onSubmit }: { cfg: DealConfig; onClose: () => void; onSubmit: (f: DealCreateForm) => Promise<DealWriteResult> }) {
  const openStages = cfg.stages.filter((s) => !isClosed(s))
  const [form, setForm] = useState<DealCreateForm>({ title: '', stage: openStages[0] || '', amount_yuan: '', expected_close_at: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  async function submit() {
    const title = form.title.trim()
    if (!title) { setErr('给这单起个名字，复盘时要看得懂'); return }
    // 金额留空 = 还没谈到钱（0）；填了就必须是能精确转分的形态，别让 0.29 变成 28.999…
    const amount = form.amount_yuan.trim() === '' ? 0 : yuanToFen(form.amount_yuan)
    if (amount === null) { setErr('金额格式不对：最多两位小数，别带单位'); return }
    if (amount > cfg.max_amount_cents) { setErr(`金额超出允许上限（¥ ${fenToYuan(cfg.max_amount_cents)}）`); return }
    setBusy(true)
    const res = await onSubmit({ ...form, title, amount_yuan: form.amount_yuan.trim() })
    setBusy(false)
    if (!res.ok) {
      // dismiss=父组件已经把这单该做的做了（如撞"已有在途单"，它直接展开了原单），
      // 此时留着一张填过半截的表单只会让人再提交一次
      if (res.dismiss) onClose()
      else setErr(res.message)
      return
    }
    onClose()
  }

  return (
    <Dialog header="开一张商机" visible onClose={onClose} onConfirm={() => void submit()} confirmBtn={busy ? '提交中…' : '创建'}>
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>标题 *<Input value={form.title} onChange={(v) => setForm({ ...form, title: String(v) })} placeholder="如 极石 01 四驱版 · 置换" /></label>
        <label style={LABEL_STYLE}>起始阶段
          <select value={form.stage} onChange={(e) => setForm({ ...form, stage: e.target.value })} style={DATE_STYLE}>
            {openStages.map((s) => <option key={s} value={s}>{cfg.stage_names[s] || s}</option>)}
          </select>
        </label>
        <label style={LABEL_STYLE}>金额（元，可留空）<Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder="还没谈到钱就空着" /></label>
        <label style={LABEL_STYLE}>预计成交日（可选）
          <input type="date" aria-label="预计成交日" value={form.expected_close_at} style={DATE_STYLE} onChange={(e) => setForm({ ...form, expected_close_at: e.target.value })} />
        </label>
        {err && <div style={{ color: '#c53030', fontSize: 12 }} data-testid="deal-form-err">{err}</div>}
      </div>
    </Dialog>
  )
}

// MoveDealDialog 推进一张商机：目标阶段 + （成交）金额 / （流失）原因。
// 下拉列全部其它阶段，但"往回退"的判据在后端（stage_backward）——前端不另算一套顺序，
// 否则阶段表一改，页面就会给出后端不认的"合法"选项。
function MoveDealDialog({ deal, cfg, onClose, onSubmit }: { deal: DealRow; cfg: DealConfig; onClose: () => void; onSubmit: (f: DealMoveForm) => Promise<DealWriteResult> }) {
  const [form, setForm] = useState<DealMoveForm>({ to: '', amount_yuan: '', change_amount: false, lost_reason: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const lostReasons = Object.entries(cfg.lost_reasons)

  async function submit() {
    if (!form.to) { setErr('先选一个目标阶段'); return }
    // 这里只判"输入的元能不能精确转成分"，真正的换算由父层在提交体里做一次——
    // 两边各转一次迟早出现卡片显示 29 分、请求体 28 分的对不上。
    if (form.to === 'won') {
      const v = yuanToFen(form.amount_yuan)
      if (v === null || v <= 0) { setErr('标记成交必须填成交金额（元，两位小数以内，且大于 0）'); return }
    }
    if (form.to === 'lost' && !form.lost_reason) { setErr('标记流失必须选一个原因，否则复盘说不清这单怎么没的'); return }
    if (form.to !== 'won' && form.change_amount) {
      const v = yuanToFen(form.amount_yuan)
      if (v === null || v < 0) { setErr('金额格式不对：最多两位小数'); return }
    }
    setBusy(true)
    const res = await onSubmit(form)
    setBusy(false)
    if (!res.ok) { setErr(res.message); return }
    onClose()
  }

  return (
    <Dialog header={`推进 · ${deal.title || `商机 #${deal.id}`}`} visible onClose={onClose} onConfirm={() => void submit()} confirmBtn={busy ? '提交中…' : '保存'}>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ fontSize: 12, color: '#a0aec0' }}>当前：{deal.stage_name} · ¥ {fenToYuan(deal.amount_cents)}</div>
        <label style={LABEL_STYLE}>推进到
          <select value={form.to} onChange={(e) => setForm({ ...form, to: e.target.value, amount_yuan: '', lost_reason: '' })} style={DATE_STYLE}>
            <option value="">选择目标阶段…</option>
            {cfg.stages.filter((s) => s !== deal.stage).map((s) => <option key={s} value={s}>{cfg.stage_names[s] || s}</option>)}
          </select>
        </label>
        {form.to === 'won' && <label style={LABEL_STYLE}>成交金额（元）*<Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder="钱真正落定的数" /></label>}
        {form.to === 'lost' && <label style={LABEL_STYLE}>流失原因 *
          <select value={form.lost_reason} onChange={(e) => setForm({ ...form, lost_reason: e.target.value })} style={DATE_STYLE}>
            <option value="">选择原因…</option>
            {lostReasons.map(([k, label]) => <option key={k} value={k}>{label}</option>)}
          </select>
        </label>}
        {form.to && !isClosed(form.to) && (
          <label style={{ ...LABEL_STYLE, display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={form.change_amount} onChange={(e) => setForm({ ...form, change_amount: e.target.checked })} />
            顺带改金额
          </label>
        )}
        {form.change_amount && !isClosed(form.to) && <Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder={`新金额（元），当前 ${fenToYuanInput(deal.amount_cents)}`} />}
        {err && <div style={{ color: '#c53030', fontSize: 12 }} data-testid="deal-form-err">{err}</div>}
      </div>
    </Dialog>
  )
}

// QuoteDealDialog 出一版报价：明细行（元输入）+ 有效期 + 存草稿/存并发出两个出口。
// 合计只是输入过程中的参考值——落库后以服务端按明细重算的那份为准（这里不做二次求和展示给最终态）。
function QuoteDealDialog({ deal, cfg, onClose, onSubmit }: {
  deal: DealRow; cfg: DealConfig; onClose: () => void
  onSubmit: (f: DealQuoteForm, send: boolean) => Promise<DealWriteResult>
}) {
  const [lines, setLines] = useState<{ name: string; qty: string; unit_yuan: string }[]>([{ name: '', qty: '1', unit_yuan: '' }])
  const [note, setNote] = useState('')
  const [validUntil, setValidUntil] = useState('')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  // 实时合计：每行先各自换成分再整数相加，全程不做浮点求和
  const totalFen = lines.reduce((sum, l) => {
    const unit = yuanToFen(l.unit_yuan) ?? 0
    const qty = Number(l.qty) || 0
    return sum + unit * qty
  }, 0)

  // 校验结果用「err 非空即失败」的单一形状（本仓 tsconfig 未开 strictNullChecks，判别式联合收不窄）
  type Built = { err: string; form: DealQuoteForm }
  const fail = (err: string): Built => ({ err, form: { lines: [], note: '', valid_until: '' } })

  function build(): Built {
    const built: { name: string; qty: string; unit_yuan: string }[] = []
    for (const l of lines) {
      const name = l.name.trim()
      const qtyRaw = l.qty.trim()
      const unitRaw = l.unit_yuan.trim()
      // 空行的判据是"名字和单价都没写"：新增行的数量默认就是 1，
      // 拿它当"这一行有内容"的证据，直接点保存的人会吃一句"每一行都要有项目名称"
      if (!name && unitRaw === '') continue
      if (!name) return fail('每一行都要有项目名称')
      const qty = Number(qtyRaw)
      if (!Number.isInteger(qty) || qty <= 0) return fail(`「${name}」数量得是正整数`)
      const unit = yuanToFen(unitRaw)
      if (unit === null || unit < 0) return fail(`「${name}」单价格式不对（元，最多两位小数）`)
      built.push({ name, qty: qtyRaw, unit_yuan: unitRaw })
    }
    if (built.length === 0) return fail('报价至少要有明细行')
    if (built.length > cfg.max_quote_lines) return fail(`明细行数超出上限 ${cfg.max_quote_lines} 行`)
    return { err: '', form: { lines: built, note, valid_until: validUntil } }
  }

  async function submit(send: boolean) {
    const b = build()
    if (b.err !== '') { setErr(b.err); return }
    setBusy(true)
    const res = await onSubmit(b.form, send)
    setBusy(false)
    if (!res.ok) { setErr(res.message); return }
    onClose()
  }

  return (
    <Dialog header={`报价 · ${deal.title || `商机 #${deal.id}`}`} visible onClose={onClose} width="520px"
      footer={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%' }}>
          <span style={{ fontSize: 12, color: '#475569' }}>合计 ¥ {fenToYuan(totalFen)}</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <Button theme="default" variant="outline" onClick={onClose}>取消</Button>
            <Button theme="default" loading={busy} onClick={() => void submit(false)}>存草稿</Button>
            <Button theme="primary" loading={busy} onClick={() => void submit(true)}>保存并发出</Button>
          </div>
        </div>
      }>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ fontSize: 12, color: '#a0aec0' }}>明细行<strong>一旦发出就锁死</strong>：要调价就在同一商机下再出一版，旧版自动标为「已被新版取代」。</div>
        {lines.map((l, i) => (
          <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 60px 90px 28px', gap: 6 }} data-testid={`quote-line-${i}`}>
            <Input value={l.name} placeholder="如 极石 01 四驱版" onChange={(v) => setLines(lines.map((x, k) => (k === i ? { ...x, name: String(v) } : x)))} />
            {/* 数量/单价用原生 input：TDesign 的 Input 会把 aria-label 挂到外层 div 上，
                屏幕阅读器读到的就是那个 div（没有值），这里没必要为了组件统一牺牲可读性 */}
            <input type="text" inputMode="numeric" aria-label="数量" placeholder="数量" value={l.qty} style={CELL_INPUT}
              onChange={(e) => setLines(lines.map((x, k) => (k === i ? { ...x, qty: e.target.value } : x)))} />
            <input type="text" inputMode="decimal" aria-label="单价（元）" placeholder="单价" value={l.unit_yuan} style={CELL_INPUT}
              onChange={(e) => setLines(lines.map((x, k) => (k === i ? { ...x, unit_yuan: e.target.value } : x)))} />
            <button aria-label="删除该行" disabled={lines.length <= 1} onClick={() => setLines(lines.length <= 1 ? lines : lines.filter((_, k) => k !== i))} style={{ ...MINI_BTN, color: '#9b2c2c' }}>删</button>
          </div>
        ))}
        <button data-testid="quote-add-line" disabled={lines.length >= cfg.max_quote_lines} onClick={() => setLines([...lines, { name: '', qty: '1', unit_yuan: '' }])} style={{ ...MINI_BTN, alignSelf: 'flex-start' }}>+ 加一行</button>
        <label style={LABEL_STYLE}>有效期至（可选）
          <input type="date" aria-label="报价有效期" value={validUntil} style={DATE_STYLE} onChange={(e) => setValidUntil(e.target.value)} />
        </label>
        <label style={LABEL_STYLE}>备注 / 条款<Textarea value={note} onChange={(v) => setNote(String(v))} placeholder="如 含三年质保，不含上牌" autosize={{ minRows: 2 }} /></label>
        {err && <div style={{ color: '#c53030', fontSize: 12 }} data-testid="deal-form-err">{err}</div>}
      </div>
    </Dialog>
  )
}
