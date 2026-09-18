// F5 策略中心：话术模板 CRUD + 策略试运行面板（7 步推理结果）。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, InputNumber, MessagePlugin, Select, Table, Tag, Textarea } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import type { CellProps, TableRowData } from '../../types'
import { EntityCrud, defaultRowToForm, statusTag, type FieldSpec } from './EntityCrud'

const ANCHOR_OPTS: FieldSpec['options'] = [
  { label: '0 不抛', value: 0 },
  { label: '1 同类/场景', value: 1 },
  { label: '2 拆解', value: 2 },
  { label: '3 对比', value: 3 },
  { label: '4 损失', value: 4 },
  { label: '5 稀缺', value: 5 },
  { label: '6 代价自担', value: 6 },
]
const STATUS_OPTS = [{ label: '启用', value: 1 }, { label: '停用', value: 0 }, { label: '草稿（不进召回）', value: 2 }]
const ANCHOR_NAMES = ['不抛', '同类/场景', '拆解', '对比', '损失', '稀缺', '代价自担']

/** 把锚点编号转换为策略中文名称。 */
function anchorLabel(v: number) {
  return ANCHOR_NAMES[v] ?? String(v ?? '-')
}

const TEMPLATE_ALL_FIELDS: FieldSpec[] = [
  { key: 'id', label: '模板ID' },
  { key: 'name', label: '名称' },
  { key: 'anchor_type', label: '锚类型', type: 'number' },
  { key: 'sub_type', label: '子类型' },
  { key: 'category', label: '分类' },
  { key: 'trigger_tags', label: '触发标签', type: 'list' },
  { key: 'required_tags', label: '必含标签', type: 'list' },
  { key: 'min_intent', label: '最低意向', type: 'number' },
  { key: 'max_intent', label: '最高意向', type: 'number' },
  { key: 'applicable_models', label: '适用车型', type: 'list' },
  { key: 'prompt_template', label: '抛话术' },
  { key: 'hook_template', label: '钩话术' },
  { key: 'hook_fields', label: '钩字段', type: 'list' },
  { key: 'required_features', label: '所需卖点', type: 'list' },
  { key: 'priority', label: '优先级', type: 'number' },
  { key: 'status', label: '状态', type: 'number' },
  { key: 'ab_group', label: '实验组' },
  { key: 'ab_weight', label: '分流权重', type: 'number' },
]

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
  const stat = stats[String(row.id)]
  if (!stat || Number(stat.sample_count || 0) <= 0) return <span className="text-xs text-gray-400">-</span>
  if (Number(stat.sample_count || 0) < 50) return <Tag theme="warning">数据积累中 {Number(stat.sample_count || 0)}</Tag>
  return (
    <div className="text-xs leading-5">
      <div>钩 {formatPercent(stat.hook_rate)} / 资 {formatPercent(stat.lead_rate)}</div>
      <div className="text-gray-400">{Number(stat.sample_count || 0)} 样本 · 意向 {(Number(stat.avg_intent_delta || 0) * 100).toFixed(1)}%{typeof stat.avg_eval_score === 'number' ? ` · 分 ${stat.avg_eval_score.toFixed(0)}` : ''}</div>
    </div>
  )
}

/** 策略模板 Tab：查看和维护话术/锚点模板。 */
export function StrategyTemplateTab() {
  const [packStats, setPackStats] = useState<Record<string, PackTemplateStat>>({})

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
    title="策略模板"
    pageSize={20}
    hasStatusToggle={false}
    filters={[
      { key: 'keyword', label: '名称/话术' },
      { key: 'anchor_type', label: '锚类型', options: ANCHOR_OPTS },
      { key: 'category', label: '分类' },
      { key: 'status', label: '状态', options: STATUS_OPTS },
      { key: 'ab_group', label: '实验组(E4)' },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 150 },
      { colKey: 'name', title: '名称', width: 160 },
      { colKey: 'anchor_type', title: '锚', width: 110, cell: (p: CellProps) => <Tag theme="primary">{anchorLabel(Number(p.row.anchor_type))}</Tag> },
      { colKey: 'category', title: '分类', width: 120 },
      { colKey: 'prompt_template', title: '抛话术', ellipsis: true },
      { colKey: 'ab_group', title: '实验组', width: 110, cell: (p: CellProps) => (p.row.ab_group ? <Tag theme="warning">{String(p.row.ab_group)} · {Number(p.row.ab_weight || 0)}</Tag> : <span className="text-xs text-gray-400">-</span>) },
      { colKey: 'priority', title: '优先级', width: 90 },
      { colKey: 'usage_count', title: '使用次数', width: 100 },
      { colKey: 'pack_effect', title: '包效果', width: 190, cell: (p: CellProps) => <PackEffectCell row={p.row} stats={packStats} /> },
      { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => statusTag(p.row) },
    ]}
    createFields={[
      { key: 'id', label: '模板ID', required: true, placeholder: '如 tpl_custom_001' },
      { key: 'name', label: '模板名称', required: true },
      { key: 'anchor_type', label: '锚类型', type: 'select', options: ANCHOR_OPTS, defaultValue: 0 },
      { key: 'category', label: '分类', placeholder: '同类锚/对比锚/不抛' },
      { key: 'sub_type', label: '子类型' },
      { key: 'min_intent', label: '最低意向', type: 'number', defaultValue: 0 },
      { key: 'max_intent', label: '最高意向', type: 'number', defaultValue: 1 },
      { key: 'priority', label: '优先级', type: 'number', defaultValue: 5 },
      { key: 'status', label: '状态', type: 'select', options: STATUS_OPTS, defaultValue: 1 },
      { key: 'ab_group', label: '实验组名', placeholder: 'E4：同组 variant 按客户稳定分桶，留空不参与实验' },
      { key: 'ab_weight', label: '分流权重', type: 'number', defaultValue: 0 },
      { key: 'trigger_tags', label: '触发标签', type: 'list' },
      { key: 'required_tags', label: '必含标签', type: 'list' },
      { key: 'applicable_models', label: '适用车型', type: 'list' },
      { key: 'hook_fields', label: '钩采集字段', type: 'list' },
      { key: 'required_features', label: '所需卖点', type: 'list' },
      { key: 'prompt_template', label: '抛话术', type: 'textarea', required: true },
      { key: 'hook_template', label: '钩话术', type: 'textarea' },
    ]}
    rowToForm={(row) => defaultRowToForm(TEMPLATE_ALL_FIELDS, row)}
  />
}

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

type CustomerOption = { label: string; value: number }

/** 策略测试 Tab：模拟客户输入并查看策略推荐结果。 */
export function StrategyTestTab() {
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

  const set = useCallback((k: string, v: any) => setForm((s) => ({ ...s, [k]: v })), [])

  useEffect(() => {
    ;(async () => {
      const j = await AUTH('/api/v1/customers?page_size=50')
      if (j?.code === 0) {
        setCustomers((j.data?.list || []).map((c: CrudRow) => ({
          label: `${c.id} ${c.name && !String(c.name).startsWith('访客_') ? c.name : '客户'}`,
          value: Number(c.id),
        })))
        if (j.data?.list?.length) setForm((s) => ({ ...s, customer_id: Number(j.data.list[0].id) }))
      }
    })()
  }, [])

  const run = async () => {
    if (!form.customer_id) { MessagePlugin.warning('请选择测试客户'); return }
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
    anchor: ANCHOR_NAMES[i] || String(i),
    score: v,
    prob: output?.anchor_probs?.[i] ?? 0,
  })), [output])

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">策略试运行</h3>
        <p className="text-xs text-gray-400 mt-0.5">输入客户与模拟消息，观察 7 步推理结果。</p>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-4 gap-3">
          <Select value={form.customer_id} onChange={(v) => set('customer_id', v)} options={customers} placeholder="选择客户" filterable style={{ width: '100%' }} />
          <InputNumber value={form.conversation_id} onChange={(v) => set('conversation_id', v)} placeholder="会话ID(可选)" style={{ width: '100%' }} />
          <Select value={form.emotion} onChange={(v) => set('emotion', v)} options={[{ label: '中性', value: 'neutral' }, { label: '正面', value: 'positive' }, { label: '负面', value: 'negative' }]} style={{ width: '100%' }} />
          <Button theme="primary" loading={testing} disabled={testing} onClick={run}>运行策略</Button>
          <div className="md:col-span-4"><Textarea value={form.customer_input} onChange={(v) => set('customer_input', v)} autosize={{ minRows: 2, maxRows: 5 }} placeholder="客户消息，例如：这车有点贵，和坦克比怎么样？" /></div>
          <InputNumber value={form.intent_score} onChange={(v) => set('intent_score', v)} placeholder="意向分" style={{ width: '100%' }} />
          <InputNumber value={form.trust_level} onChange={(v) => set('trust_level', v)} placeholder="信任度" style={{ width: '100%' }} />
          <InputNumber value={form.price_sensitivity} onChange={(v) => set('price_sensitivity', v)} placeholder="价格敏感" style={{ width: '100%' }} />
          <InputNumber value={form.current_stage} onChange={(v) => set('current_stage', v)} placeholder="心智阶段" style={{ width: '100%' }} />
          <InputNumber value={form.attempts} onChange={(v) => set('attempts', v)} placeholder="抛锚次数" style={{ width: '100%' }} />
          <InputNumber value={form.hook_rate} onChange={(v) => set('hook_rate', v)} placeholder="接钩率" style={{ width: '100%' }} />
          <InputNumber value={form.resistance_type} onChange={(v) => set('resistance_type', v)} placeholder="抗性 0无/1价/2配/3服/4牌" style={{ width: '100%' }} />
          <InputNumber value={form.silent_duration} onChange={(v) => set('silent_duration', v)} placeholder="沉默秒数" style={{ width: '100%' }} />
        </div>
      </div>

      {output && (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
          <div className="bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">决策结果</h4>
            <div className="grid grid-cols-2 gap-3 text-sm">
              <div><span className="text-xs text-gray-400">选中锚</span><div><Tag theme="primary">{anchorLabel(output.selected_anchor)}</Tag></div></div>
              <div><span className="text-xs text-gray-400">置信度</span><div className="font-semibold">{(output.anchor_confidence * 100).toFixed(1)}%</div></div>
              <div><span className="text-xs text-gray-400">路由</span><div><Tag theme={output.route_result === 'human' ? 'warning' : 'success'}>{output.route_result}</Tag></div></div>
              <div><span className="text-xs text-gray-400">紧迫等级</span><div>{output.urgency_level || '-'}</div></div>
              <div><span className="text-xs text-gray-400">模板</span><div>{output.template_name || '-'}<span className="text-xs text-gray-400 ml-1">{output.template_id}</span></div></div>
              <div><span className="text-xs text-gray-400">意向变化</span><div>{(output.intent_delta * 100).toFixed(1)}%</div></div>
            </div>
            <div className="mt-3 text-xs text-gray-500">{output.route_reason || '无路由原因'}</div>
            {output.stage_downgraded && <div className="mt-2"><Tag theme="warning">阶段锁降级</Tag><span className="text-xs text-gray-500 ml-2">{anchorLabel(output.original_anchor ?? 0)} → {anchorLabel(output.final_anchor ?? 0)}</span></div>}
          </div>
          <div className="bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">话术预览</h4>
            <div className="text-xs text-gray-400">抛话术</div>
            <Textarea readOnly value={output.prompt_text || '-'} autosize={{ minRows: 2, maxRows: 6 }} />
            <div className="text-xs text-gray-400 mt-3">钩话术</div>
            <Textarea readOnly value={output.hook_text || '-'} autosize={{ minRows: 2, maxRows: 5 }} />
          </div>
          <div className="md:col-span-2 bg-white rounded-lg shadow-sm p-4">
            <h4 className="text-sm font-bold text-gray-800 mb-3">锚评分</h4>
            <Table
              rowKey="anchor"
              data={scores as TableRowData[]}
              size="small"
              columns={[
                { colKey: 'anchor', title: '锚类型', width: 160 },
                { colKey: 'score', title: '原始分', width: 140, cell: (p: CellProps) => Number(p.row.score || 0).toFixed(3) },
                { colKey: 'prob', title: '概率', width: 140, cell: (p: CellProps) => (Number(p.row.prob || 0) * 100).toFixed(1) + '%' },
                { colKey: 'hit', title: '选中', cell: (p: CellProps) => p.row.anchor === anchorLabel(output.selected_anchor) ? <Tag theme="success">是</Tag> : <span className="text-gray-300">-</span> },
              ]}
            />
          </div>
        </div>
      )}
    </div>
  )
}
