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
//
// i18n（P2-14）：与管理端 DealsTab 字节相同的文案直接复用 deals.* 键，本卡专有条目落在 dealCard.*；
// 阶段名/流失原因名/报价状态名全部是后端下发的中文数据（cfg.stage_names 等），不属于界面文案。
import { useState } from 'react'
import { Button, Dialog, Input, Textarea } from 'tdesign-react'
import type { DealConfig, DealRow } from '../../types'
import { useI18n } from '../../i18n/useI18n'
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
  const { t } = useI18n()
  const [createOpen, setCreateOpen] = useState(false)
  const [moveFor, setMoveFor] = useState<DealRow | null>(null)
  const [quoteFor, setQuoteFor] = useState<DealRow | null>(null)

  const open = deals.find((d) => !isClosed(d.stage))

  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <b style={{ fontSize: 13 }}>{t('dealCard.title')}</b>
        {/* 一个客户同时只准一张在途单：有在途单时不再给"开单"入口，避免让人去撞后端那道拒 */}
        {!open && <button data-testid="deal-open-btn" onClick={() => setCreateOpen(true)} style={{ background: 'none', border: 'none', color: 'var(--pri)', fontSize: 12, cursor: 'pointer' }}>{t('dealCard.openBtn')}</button>}
      </div>
      {deals.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>{t('dealCard.empty')}</div>}
      {open && <div data-testid="deal-open-hint" style={{ color: '#475569', marginTop: 6, fontSize: 12 }}>{t('dealCard.openHint')}</div>}
      {deals.map((d) => {
        const closed = isClosed(d.stage)
        return (
          <div key={d.id} data-testid={`deal-row-${d.id}`} style={ROW_STYLE}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 6 }}>
              <span style={{ flex: 1, fontWeight: 600 }}>{d.title || t('dealCard.dealId', { id: d.id })}</span>
              <span data-testid={`deal-stage-${d.id}`} style={{ fontSize: 11, padding: '1px 6px', borderRadius: 10, background: closed ? (d.stage === 'won' ? '#e8f9ef' : '#f1f5f9') : '#eef2ff', color: closed ? (d.stage === 'won' ? '#1f7a45' : '#64748b') : '#4338ca' }}>{d.stage_name}</span>
            </div>
            <div style={{ color: '#a0aec0', fontSize: 12, marginTop: 2 }}>
              ¥ {fenToYuan(d.amount_cents)}
              {/* 停滞天数只对在途单有意义：终局单再显示"停滞 0 天"会被读成"这单还活着" */}
              {!closed && <span>{t('dealCard.stalled', { days: d.stalled_days })}</span>}
              {closed && d.stage === 'lost' && <span> · {d.lost_reason_name || d.lost_reason || t('dealCard.lostFallback')}</span>}
              {closed && d.stage === 'won' && <span> · {d.won_at || t('dealCard.wonFallback')}</span>}
              {d.quote_count > 0 && <span>{t('dealCard.quoteCount', { count: d.quote_count })}</span>}
            </div>
            {d.open_quote && (
              <div data-testid={`deal-quote-${d.id}`} style={{ fontSize: 12, marginTop: 2, color: '#475569' }}>
                {t('dealCard.currentQuote', { version: d.open_quote.version, status: d.open_quote.status_name, amount: fenToYuan(d.open_quote.total_cents) })}
                {d.open_quote.valid_until ? t('dealCard.quoteValidUntil', { date: String(d.open_quote.valid_until).slice(0, 10) }) : ''}
              </div>
            )}
            {!closed && cfg && (
              <div style={{ display: 'flex', gap: 6, marginTop: 6 }}>
                <button data-testid={`deal-move-${d.id}`} onClick={() => setMoveFor(d)} style={MINI_BTN}>{t('deals.advance')}</button>
                <button data-testid={`deal-newquote-${d.id}`} onClick={() => setQuoteFor(d)} style={MINI_BTN}>{d.open_quote ? t('dealCard.newVersionBtn') : t('dealCard.quoteBtn')}</button>
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
  const { t } = useI18n()
  const openStages = cfg.stages.filter((s) => !isClosed(s))
  const [form, setForm] = useState<DealCreateForm>({ title: '', stage: openStages[0] || '', amount_yuan: '', expected_close_at: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  async function submit() {
    const title = form.title.trim()
    if (!title) { setErr(t('dealCard.titleRequired')); return }
    // 金额留空 = 还没谈到钱（0）；填了就必须是能精确转分的形态，别让 0.29 变成 28.999…
    const amount = form.amount_yuan.trim() === '' ? 0 : yuanToFen(form.amount_yuan)
    if (amount === null) { setErr(t('deals.errAmountFmt')); return }
    if (amount > cfg.max_amount_cents) { setErr(t('deals.errAmountOver', { max: fenToYuan(cfg.max_amount_cents) })); return }
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
    <Dialog header={t('dealCard.createTitle')} visible onClose={onClose} onConfirm={() => void submit()} confirmBtn={busy ? t('dealCard.submitting') : t('dealCard.create')}>
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>{t('dealCard.lblTitleStar')}<Input value={form.title} onChange={(v) => setForm({ ...form, title: String(v) })} placeholder={t('dealCard.phDealTitle')} /></label>
        <label style={LABEL_STYLE}>{t('deals.lblStartStage')}
          <select value={form.stage} onChange={(e) => setForm({ ...form, stage: e.target.value })} style={DATE_STYLE}>
            {openStages.map((s) => <option key={s} value={s}>{cfg.stage_names[s] || s}</option>)}
          </select>
        </label>
        <label style={LABEL_STYLE}>{t('deals.lblAmount')}<Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder={t('deals.phAmount')} /></label>
        <label style={LABEL_STYLE}>{t('deals.lblExpectedClose')}
          <input type="date" aria-label={t('dealCard.ariaExpectedClose')} value={form.expected_close_at} style={DATE_STYLE} onChange={(e) => setForm({ ...form, expected_close_at: e.target.value })} />
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
  const { t } = useI18n()
  const [form, setForm] = useState<DealMoveForm>({ to: '', amount_yuan: '', change_amount: false, lost_reason: '' })
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const lostReasons = Object.entries(cfg.lost_reasons)

  async function submit() {
    if (!form.to) { setErr(t('deals.pickStageFirst')); return }
    // 这里只判"输入的元能不能精确转成分"，真正的换算由父层在提交体里做一次——
    // 两边各转一次迟早出现卡片显示 29 分、请求体 28 分的对不上。
    if (form.to === 'won') {
      const v = yuanToFen(form.amount_yuan)
      if (v === null || v <= 0) { setErr(t('dealCard.wonNeedAmount')); return }
    }
    if (form.to === 'lost' && !form.lost_reason) { setErr(t('dealCard.lostNeedReasonLong')); return }
    if (form.to !== 'won' && form.change_amount) {
      const v = yuanToFen(form.amount_yuan)
      if (v === null || v < 0) { setErr(t('dealCard.amountFmtShort')); return }
    }
    setBusy(true)
    const res = await onSubmit(form)
    setBusy(false)
    if (!res.ok) { setErr(res.message); return }
    onClose()
  }

  return (
    <Dialog header={t('dealCard.moveTitle', { title: deal.title || t('dealCard.dealId', { id: deal.id }) })} visible onClose={onClose} onConfirm={() => void submit()} confirmBtn={busy ? t('dealCard.submitting') : t('crud.save')}>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ fontSize: 12, color: '#a0aec0' }}>{t('dealCard.currentStage', { stage: deal.stage_name, amount: fenToYuan(deal.amount_cents) })}</div>
        <label style={LABEL_STYLE}>{t('deals.lblMoveTo')}
          <select value={form.to} onChange={(e) => setForm({ ...form, to: e.target.value, amount_yuan: '', lost_reason: '' })} style={DATE_STYLE}>
            <option value="">{t('dealCard.phPickStageEllipsis')}</option>
            {cfg.stages.filter((s) => s !== deal.stage).map((s) => <option key={s} value={s}>{cfg.stage_names[s] || s}</option>)}
          </select>
        </label>
        {form.to === 'won' && <label style={LABEL_STYLE}>{t('dealCard.lblWonAmountStar')}<Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder={t('dealCard.phWonAmount')} /></label>}
        {form.to === 'lost' && <label style={LABEL_STYLE}>{t('dealCard.lblLostReasonStar')}
          <select value={form.lost_reason} onChange={(e) => setForm({ ...form, lost_reason: e.target.value })} style={DATE_STYLE}>
            <option value="">{t('dealCard.phPickReason')}</option>
            {lostReasons.map(([k, label]) => <option key={k} value={k}>{label}</option>)}
          </select>
        </label>}
        {form.to && !isClosed(form.to) && (
          <label style={{ ...LABEL_STYLE, display: 'flex', alignItems: 'center', gap: 6 }}>
            <input type="checkbox" checked={form.change_amount} onChange={(e) => setForm({ ...form, change_amount: e.target.checked })} />
            {t('deals.chkChangeAmount')}
          </label>
        )}
        {form.change_amount && !isClosed(form.to) && <Input value={form.amount_yuan} onChange={(v) => setForm({ ...form, amount_yuan: String(v) })} placeholder={t('dealCard.phNewAmount', { amount: fenToYuanInput(deal.amount_cents) })} />}
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
  const { t } = useI18n()
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
      if (!name) return fail(t('dealCard.quoteItemRequired'))
      const qty = Number(qtyRaw)
      if (!Number.isInteger(qty) || qty <= 0) return fail(t('dealCard.quoteQtyInt', { name }))
      const unit = yuanToFen(unitRaw)
      if (unit === null || unit < 0) return fail(t('dealCard.quoteUnitFmt', { name }))
      built.push({ name, qty: qtyRaw, unit_yuan: unitRaw })
    }
    if (built.length === 0) return fail(t('dealCard.quoteNoLines'))
    if (built.length > cfg.max_quote_lines) return fail(t('dealCard.quoteTooManyLines', { max: cfg.max_quote_lines }))
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
    <Dialog header={t('dealCard.quoteTitle', { title: deal.title || t('dealCard.dealId', { id: deal.id }) })} visible onClose={onClose} width="520px"
      footer={
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', width: '100%' }}>
          <span style={{ fontSize: 12, color: '#475569' }}>{t('dealCard.quoteTotal', { amount: fenToYuan(totalFen) })}</span>
          <div style={{ display: 'flex', gap: 8 }}>
            <Button theme="default" variant="outline" onClick={onClose}>{t('deals.cancel')}</Button>
            <Button theme="default" loading={busy} onClick={() => void submit(false)}>{t('deals.saveDraft')}</Button>
            <Button theme="primary" loading={busy} onClick={() => void submit(true)}>{t('deals.saveAndSend')}</Button>
          </div>
        </div>
      }>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ fontSize: 12, color: '#a0aec0' }}>{t('dealCard.quoteLockA')}<strong>{t('dealCard.quoteLockStrong')}</strong>{t('dealCard.quoteLockB')}</div>
        {lines.map((l, i) => (
          <div key={i} style={{ display: 'grid', gridTemplateColumns: '1fr 60px 90px 28px', gap: 6 }} data-testid={`quote-line-${i}`}>
            <Input value={l.name} placeholder={t('deals.phQuoteItem')} onChange={(v) => setLines(lines.map((x, k) => (k === i ? { ...x, name: String(v) } : x)))} />
            {/* 数量/单价用原生 input：TDesign 的 Input 会把 aria-label 挂到外层 div 上，
                屏幕阅读器读到的就是那个 div（没有值），这里没必要为了组件统一牺牲可读性 */}
            <input type="text" inputMode="numeric" aria-label={t('dealCard.lblQty')} placeholder={t('dealCard.lblQty')} value={l.qty} style={CELL_INPUT}
              onChange={(e) => setLines(lines.map((x, k) => (k === i ? { ...x, qty: e.target.value } : x)))} />
            <input type="text" inputMode="decimal" aria-label={t('dealCard.lblUnit')} placeholder={t('dealCard.phUnit')} value={l.unit_yuan} style={CELL_INPUT}
              onChange={(e) => setLines(lines.map((x, k) => (k === i ? { ...x, unit_yuan: e.target.value } : x)))} />
            <button aria-label={t('dealCard.lblRemoveRow')} disabled={lines.length <= 1} onClick={() => setLines(lines.length <= 1 ? lines : lines.filter((_, k) => k !== i))} style={{ ...MINI_BTN, color: '#9b2c2c' }}>{t('deals.removeRow')}</button>
          </div>
        ))}
        <button data-testid="quote-add-line" disabled={lines.length >= cfg.max_quote_lines} onClick={() => setLines([...lines, { name: '', qty: '1', unit_yuan: '' }])} style={{ ...MINI_BTN, alignSelf: 'flex-start' }}>{t('deals.addRow')}</button>
        <label style={LABEL_STYLE}>{t('deals.lblValidUntil')}
          <input type="date" aria-label={t('dealCard.ariaQuoteValidUntil')} value={validUntil} style={DATE_STYLE} onChange={(e) => setValidUntil(e.target.value)} />
        </label>
        <label style={LABEL_STYLE}>{t('deals.lblNote')}<Textarea value={note} onChange={(v) => setNote(String(v))} placeholder={t('deals.phNote')} autosize={{ minRows: 2 }} /></label>
        {err && <div style={{ color: '#c53030', fontSize: 12 }} data-testid="deal-form-err">{err}</div>}
      </div>
    </Dialog>
  )
}
