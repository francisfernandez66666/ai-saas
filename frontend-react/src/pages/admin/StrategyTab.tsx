// F5 策略中心：话术模板 CRUD + 策略试运行面板（7 步推理结果）。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, InputNumber, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import { isVisitorName } from '../../lib/visitor'
import type { CellProps, TableRowData } from '../../types'
import { EntityCrud, defaultRowToForm, StatusTag, type FieldSpec, type Option } from './EntityCrud'

// P2-14：模块级常量拿不到 t()，只存「后端值 → I18nKey」映射，翻译推迟到渲染时。
/** 锚点编号（0~6 为后端契约值，不翻译）→ 锚点名 I18nKey。 */
const ANCHOR_NAME_KEYS: Record<number, I18nKey> = {
  0: 'strategy.anchorName0',
  1: 'strategy.anchorName1',
  2: 'strategy.anchorName2',
  3: 'strategy.anchorName3',
  4: 'strategy.anchorName4',
  5: 'strategy.anchorName5',
  6: 'strategy.anchorName6',
}
/** 锚点编号顺序：下拉与评分表都按它遍历，保证与后端编号一致。 */
const ANCHOR_ORDER = [0, 1, 2, 3, 4, 5, 6]
/** 模板状态下拉（1/0/2 为后端值）。 */
const STATUS_KEYS: { key: I18nKey; value: number }[] = [
  { key: 'common.enable', value: 1 },
  { key: 'common.disable', value: 0 },
  { key: 'strategy.statusDraft', value: 2 },
]
/** 情绪下拉（neutral/positive/negative 为后端值）。 */
const EMOTION_KEYS: { key: I18nKey; value: string }[] = [
  { key: 'strategy.emotionNeutral', value: 'neutral' },
  { key: 'strategy.emotionPositive', value: 'positive' },
  { key: 'strategy.emotionNegative', value: 'negative' },
]

/**
 * 把锚点编号转换为锚点名。翻译函数由调用方注入——模块级函数拿不到 hook，
 * 而锚点名在模板表格、试运行结果、评分表三处都要用。
 * 未知编号回退为数字文本（后端新增锚点时前端不至于白屏）。
 */
function anchorLabel(v: number, t: (k: I18nKey) => string) {
  const k = ANCHOR_NAME_KEYS[v]
  return k ? t(k) : String(v ?? '-')
}

// 只服务 defaultRowToForm（该函数只读 key/type，不读 label），故不携带可渲染文案。
const TEMPLATE_ALL_FIELDS: FieldSpec[] = [
  { key: 'id' },
  { key: 'name' },
  { key: 'anchor_type', type: 'number' },
  { key: 'sub_type' },
  { key: 'category' },
  { key: 'trigger_tags', type: 'list' },
  { key: 'required_tags', type: 'list' },
  { key: 'min_intent', type: 'number' },
  { key: 'max_intent', type: 'number' },
  { key: 'applicable_models', type: 'list' },
  { key: 'prompt_template' },
  { key: 'hook_template' },
  { key: 'hook_fields', type: 'list' },
  { key: 'required_features', type: 'list' },
  { key: 'priority', type: 'number' },
  { key: 'status', type: 'number' },
  { key: 'ab_group' },
  { key: 'ab_weight', type: 'number' },
]

// PackTemplateStat 单个策略模板的包效果统计（/admin/packs/stats 下发）：样本量与钩子/留资/评分等比率。
type PackTemplateStat = {
  template_id: string
  sample_count: number
  hook_rate: number
  lead_rate: number
  pending_human_rate?: number
  avg_intent_delta?: number
  avg_eval_score?: number | null
}

/** 格式化比例值为百分比文本。 */
function formatPercent(v: number) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`
}

/** 策略模板效果单元格：展示钩子、留资与评分指标。 */
function PackEffectCell({ row, stats }: { row: CrudRow; stats: Record<string, PackTemplateStat> }) {
  const { t } = useI18n()
  const stat = stats[String(row.id)]
  if (!stat || Number(stat.sample_count || 0) <= 0) return <span className="text-xs text-gray-400">-</span>
  if (Number(stat.sample_count || 0) < 50) return <Tag theme="warning">{t('strategy.accumulating', { n: Number(stat.sample_count || 0) })}</Tag>
  return (
    <div className="text-xs leading-5">
      <div>{t('strategy.effectLine', { hook: formatPercent(stat.hook_rate), lead: formatPercent(stat.lead_rate) })}</div>
      <div className="text-gray-400">{t('strategy.sampleLine', { n: Number(stat.sample_count || 0), intent: (Number(stat.avg_intent_delta || 0) * 100).toFixed(1) })}{typeof stat.avg_eval_score === 'number' ? t('strategy.sampleScore', { score: stat.avg_eval_score.toFixed(0) }) : ''}</div>
    </div>
  )
}

/** 策略模板 Tab：查看和维护话术/锚点模板。 */
export function StrategyTemplateTab() {
  const { t } = useI18n()
  const [packStats, setPackStats] = useState<Record<string, PackTemplateStat>>({})

  // 语言切换时 t 的引用会变（I18nProvider 按 lang 记忆化），下拉随之重算
  const anchorOptions: Option[] = ANCHOR_ORDER.map((v) => ({ label: `${v} ${t(ANCHOR_NAME_KEYS[v])}`, value: v }))
  const statusOptions: Option[] = STATUS_KEYS.map((x) => ({ label: t(x.key), value: x.value }))

  useEffect(() => {
    ;(async () => {
      const j = await AUTH('/api/v1/admin/packs/stats?days=30')
      if (j?.code === 0) {
        const map: Record<string, PackTemplateStat> = {}
        for (const s of (j.data?.list || []) as PackTemplateStat[]) {
          if (s?.template_id) map[String(s.template_id)] = s
        }
        setPackStats(map)
      }
    })()
  }, [])

  return <EntityCrud
    base="/api/v1/strategy/templates"
    title={t('strategy.title')}
    pageSize={20}
    hasStatusToggle={false}
    filters={[
      { key: 'keyword', label: t('strategy.filterKw') },
      { key: 'anchor_type', label: t('strategy.anchorType'), options: anchorOptions },
      { key: 'category', label: t('common.category') },
      { key: 'status', label: t('common.status'), options: statusOptions },
      { key: 'ab_group', label: t('strategy.abGroupFilter') },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 150 },
      { colKey: 'name', title: t('common.name'), width: 160 },
      { colKey: 'anchor_type', title: t('strategy.anchorCol'), width: 110, cell: (p: CellProps) => <Tag theme="primary">{anchorLabel(Number(p.row.anchor_type), t)}</Tag> },
      { colKey: 'category', title: t('common.category'), width: 120 },
      { colKey: 'prompt_template', title: t('strategy.promptScript'), ellipsis: true },
      { colKey: 'ab_group', title: t('strategy.abGroup'), width: 110, cell: (p: CellProps) => (p.row.ab_group ? <Tag theme="warning">{String(p.row.ab_group)} · {Number(p.row.ab_weight || 0)}</Tag> : <span className="text-xs text-gray-400">-</span>) },
      { colKey: 'priority', title: t('strategy.priority'), width: 90 },
      { colKey: 'usage_count', title: t('strategy.usageCount'), width: 100 },
      { colKey: 'pack_effect', title: t('strategy.packEffect'), width: 190, cell: (p: CellProps) => <PackEffectCell row={p.row} stats={packStats} /> },
      { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <StatusTag row={p.row} /> },
    ]}
    createFields={[
      { key: 'id', label: t('strategy.templateId'), required: true, placeholder: t('strategy.templateIdPh') },
      { key: 'name', label: t('strategy.templateName'), required: true },
      { key: 'anchor_type', label: t('strategy.anchorType'), type: 'select', options: anchorOptions, defaultValue: 0 },
      { key: 'category', label: t('common.category'), placeholder: t('strategy.categoryPh') },
      { key: 'sub_type', label: t('strategy.subType') },
      { key: 'min_intent', label: t('strategy.minIntent'), type: 'number', defaultValue: 0 },
      { key: 'max_intent', label: t('strategy.maxIntent'), type: 'number', defaultValue: 1 },
      { key: 'priority', label: t('strategy.priority'), type: 'number', defaultValue: 5 },
      { key: 'status', label: t('common.status'), type: 'select', options: statusOptions, defaultValue: 1 },
      { key: 'ab_group', label: t('strategy.abGroupName'), placeholder: t('strategy.abGroupPh') },
      { key: 'ab_weight', label: t('strategy.abWeight'), type: 'number', defaultValue: 0 },
      { key: 'trigger_tags', label: t('strategy.triggerTags'), type: 'list' },
      { key: 'required_tags', label: t('strategy.requiredTags'), type: 'list' },
      { key: 'applicable_models', label: t('strategy.applicableModels'), type: 'list' },
      { key: 'hook_fields', label: t('strategy.hookFields'), type: 'list' },
      { key: 'required_features', label: t('strategy.requiredFeatures'), type: 'list' },
      { key: 'prompt_template', label: t('strategy.promptScript'), type: 'textarea', required: true },
      { key: 'hook_template', label: t('strategy.hookScript'), type: 'textarea' },
    ]}
    rowToForm={(row) => defaultRowToForm(TEMPLATE_ALL_FIELDS, row)}
  />
}

// TestOutput 策略测试接口的返回：命中的模板、生成的话术与路由/意向等裁决结果。
type TestOutput = {
  selected_anchor: number
  anchor_confidence: number
  stage_downgraded: boolean
  original_anchor?: number
  final_anchor?: number
  template_id: string
  template_name: string
  prompt_text: string
  hook_text: string
  urgency_level: string
  route_result: string
  route_reason: string
  intent_delta: number
  anchor_scores: number[]
  anchor_probs: number[]
}

// CustomerOption 策略测试的模拟客户下拉项（顾问客户列表）。
type CustomerOption = { label: string; value: number }

/** 策略测试 Tab：模拟客户输入并查看策略推荐结果。 */
export function StrategyTestTab() {
  const { t } = useI18n()
  const [customers, setCustomers] = useState<CustomerOption[]>([])
  const [form, setForm] = useState({
    customer_id: 0,
    conversation_id: 0,
    customer_input: '',
    intent_score: 0,
    trust_level: 0,
    price_sensitivity: 0,
    resistance_type: 0,
    current_stage: 0,
    attempts: 1,
    hook_rate: 0,
    high_intent_rounds: 0,
    emotion: 'neutral',
    silent_duration: 0,
  })
  const [output, setOutput] = useState<TestOutput | null>(null)
  const [testing, setTesting] = useState(false)

  // P2-10：v 收敛为 unknown（表单状态按 key 动态写入）
  const set = useCallback((k: string, v: unknown) => setForm((s) => ({ ...s, [k]: v })), [])

  const emotionOptions: Option[] = EMOTION_KEYS.map((x) => ({ label: t(x.key), value: x.value }))

  useEffect(() => {
    ;(async () => {
      const j = await AUTH('/api/v1/customers?page_size=50')
      if (j?.code === 0) {
        setCustomers((j.data?.list || []).map((c: CrudRow) => ({
          label: `${c.id} ${!isVisitorName(String(c.name)) ? c.name : t('common.customer')}`,
          value: Number(c.id),
        })))
        if (j.data?.list?.length) setForm((s) => ({ ...s, customer_id: Number(j.data.list[0].id) }))
      }
    })()
  }, [t])

  const run = async () => {
    if (!form.customer_id) { MessagePlugin.warning(t('strategy.selectCustomerWarn')); return }
    setTesting(true)
    const j = await AUTH('/api/v1/strategy/test', {
      method: 'POST',
      body: {
        customer_id: Number(form.customer_id),
        conversation_id: Number(form.conversation_id) || undefined,
        customer_input: form.customer_input,
        intent_score: Number(form.intent_score),
        trust_level: Number(form.trust_level),
        price_sensitivity: Number(form.price_sensitivity),
        resistance_type: Number(form.resistance_type),
        current_stage: Number(form.current_stage),
        attempts: Number(form.attempts),
        hook_rate: Number(form.hook_rate),
        high_intent_rounds: Number(form.high_intent_rounds),
        emotion: form.emotion,
        silent_duration: Number(form.silent_duration),
      },
      // P1 修复(2026-09-18，AUDIT_VERIFY_2026-09-18)：策略测试走真实 AI 生成回复，
      // 后端 GenerateTextWithUsage 总预算 110s，旧版吃 30s 默认超时——慢链路/降级链
      // 触发时前端先 abort，用户看到"网络异常"而后端其实仍在正常返回。放宽到 120s。
      timeoutMs: 120000,
    })
    setTesting(false)
    if (j?.code === 0) setOutput(j.data)
  }

  const scores = useMemo(() => (output?.anchor_scores || []).map((v, i) => ({
    anchor: anchorLabel(i, t),
    score: v,
    prob: output?.anchor_probs?.[i] ?? 0,
  })), [output, t])

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">{t('strategy.testTitle')}</h3>
        <p className="text-xs text-gray-400 mt-0.5">{t('strategy.testDesc')}</p>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-4 gap-3">
          <Select value={form.customer_id} onChange={(v) => set('customer_id', v)} options={customers} placeholder={t('strategy.selectCustomer')} filterable style={{ width: '100%' }} />
          <InputNumber value={form.conversation_id} onChange={(v) => set('conversation_id', v)} placeholder={t('strategy.conversationIdPh')} style={{ width: '100%' }} />
          <Select value={form.emotion} onChange={(v) => set('emotion', v)} options={emotionOptions} style={{ width: '100%' }} />
          <Button theme="primary" loading={testing} disabled={testing} onClick={run}>{t('strategy.runBtn')}</Button>
          <div className="md:col-span-4"><Textarea value={form.customer_input} onChange={(v) => set('customer_input', v)} autosize={{ minRows: 2, maxRows: 5 }} placeholder={t('strategy.inputPh')} /></div>
          <InputNumber value={form.intent_score} onChange={(v) => set('intent_score', v)} placeholder={t('strategy.intentScore')} style={{ width: '100%' }} />
          <InputNumber value={form.trust_level} onChange={(v) => set('trust_level', v)} placeholder={t('strategy.trustLevel')} style={{ width: '100%' }} />
          <InputNumber value={form.price_sensitivity} onChange={(v) => set('price_sensitivity', v)} placeholder={t('strategy.priceSensitivity')} style={{ width: '100%' }} />
          <InputNumber value={form.current_stage} onChange={(v) => set('current_stage', v)} placeholder={t('strategy.mentalStage')} style={{ width: '100%' }} />
          <InputNumber value={form.attempts} onChange={(v) => set('attempts', v)} placeholder={t('strategy.attempts')} style={{ width: '100%' }} />
          <InputNumber value={form.hook_rate} onChange={(v) => set('hook_rate', v)} placeholder={t('strategy.hookRate')} style={{ width: '100%' }} />
          <InputNumber value={form.resistance_type} onChange={(v) => set('resistance_type', v)} placeholder={t('strategy.resistancePh')} style={{ width: '100%' }} />
          <InputNumber value={form.silent_duration} onChange={(v) => set('silent_duration', v)} placeholder={t('strategy.silentDuration')} style={{ width: '100%' }} />
        </div>
      </div>

      {output && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">{t('strategy.decisionResult')}</h4>
            <div className="grid grid-cols-2 gap-3 text-sm">
              <div><span className="text-xs text-gray-400">{t('strategy.selectedAnchor')}</span><div><Tag theme="primary">{anchorLabel(output.selected_anchor, t)}</Tag></div></div>
              <div><span className="text-xs text-gray-400">{t('strategy.confidence')}</span><div className="font-semibold">{(output.anchor_confidence * 100).toFixed(1)}%</div></div>
              <div><span className="text-xs text-gray-400">{t('strategy.route')}</span><div><Tag theme={output.route_result === 'human' ? 'warning' : 'success'}>{output.route_result}</Tag></div></div>
              <div><span className="text-xs text-gray-400">{t('strategy.urgencyLevel')}</span><div>{output.urgency_level || '-'}</div></div>
              <div><span className="text-xs text-gray-400">{t('strategy.template')}</span><div>{output.template_name || '-'}<span className="text-xs text-gray-400 ml-1">{output.template_id}</span></div></div>
              <div><span className="text-xs text-gray-400">{t('strategy.intentDelta')}</span><div>{(output.intent_delta * 100).toFixed(1)}%</div></div>
            </div>
            <div className="mt-3 text-xs text-gray-500">{output.route_reason || t('strategy.noRouteReason')}</div>
            {output.stage_downgraded && <div className="mt-2"><Tag theme="warning">{t('strategy.stageDowngrade')}</Tag><span className="text-xs text-gray-500 ml-2">{anchorLabel(output.original_anchor ?? 0, t)} → {anchorLabel(output.final_anchor ?? 0, t)}</span></div>}
          </div>
          <div className="bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">{t('strategy.scriptPreview')}</h4>
            <div className="text-xs text-gray-400">{t('strategy.promptScript')}</div>
            <Textarea readOnly value={output.prompt_text || '-'} autosize={{ minRows: 2, maxRows: 6 }} />
            <div className="text-xs text-gray-400 mt-3">{t('strategy.hookScript')}</div>
            <Textarea readOnly value={output.hook_text || '-'} autosize={{ minRows: 2, maxRows: 5 }} />
          </div>
          <div className="md:col-span-2 bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">{t('strategy.anchorScores')}</h4>
            <Table
              rowKey="anchor"
              data={scores as TableRowData[]}
              size="small"
              columns={[
                { colKey: 'anchor', title: t('strategy.anchorType'), width: 160 },
                { colKey: 'score', title: t('strategy.rawScore'), width: 140, cell: (p: CellProps) => Number(p.row.score || 0).toFixed(3) },
                { colKey: 'prob', title: t('strategy.probability'), width: 140, cell: (p: CellProps) => (Number(p.row.prob || 0) * 100).toFixed(1) + '%' },
                { colKey: 'hit', title: t('strategy.hit'), cell: (p: CellProps) => p.row.anchor === anchorLabel(output.selected_anchor, t) ? <Tag theme="success">{t('common.yes')}</Tag> : <span className="text-gray-300">-</span> },
              ]}
            />
          </div>
        </div>
      )}
    </div>
  )
}
