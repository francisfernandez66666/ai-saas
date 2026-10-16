// F7 流程引擎管理页：定义查看/实例列表/状态进度/手动推进/启动实例。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Dialog, Input, InputNumber, MessagePlugin, Progress, Select, Table, Tabs, Tag } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, FlowEdge, FlowNode, TableRowData } from '../../types'
import type { Cfg } from './shared'

/** 生成流程画布节点样式。 */
const nodeStyle = (c: string): React.CSSProperties => ({ minWidth: 120, padding: '10px 14px', borderRadius: 10, color: '#fff', textAlign: 'center', fontSize: 13, fontWeight: 600, background: c })
const ROUTE_OPTS = [{ label: 'ai', value: 'ai' }, { label: 'human', value: 'human' }, { label: 'fish', value: 'fish' }, { label: 'pending_human', value: 'pending_human' }]
// P2-14：旅程阶段卡的标题/说明是文案，走 I18nKey 在渲染时翻译；
// 颜色是视觉属性不属于文案，留在原处。后端不消费这两列，纯展示。
const JOURNEY_STAGES: { t: I18nKey; d: I18nKey; c: string }[] = [
  { t: 'flow.journeyAiContact', d: 'flow.journeyAiContactD', c: '#6366f1' },
  { t: 'flow.journeyHumanContact', d: 'flow.journeyHumanContactD', c: '#0ea5e9' },
  { t: 'flow.journeyLead', d: 'flow.journeyLeadD', c: '#06b6d4' },
  { t: 'flow.journeyVisit', d: 'flow.journeyVisitD', c: '#22c55e' },
  { t: 'flow.journeyOrder', d: 'flow.journeyOrderD', c: '#f97316' },
  { t: 'flow.journeyDeliver', d: 'flow.journeyDeliverD', c: '#ef4444' },
]
// 策略决策链步骤（展示顺序固定，键序即链序）
const CHAIN_STEP_KEYS: I18nKey[] = [
  'flow.chainIntent', 'flow.chainAnchor', 'flow.chainStageLock',
  'flow.chainTemplate', 'flow.chainFallback', 'flow.chainGenerate',
]

/** 解析流程配置中的 JSON 数组。
 *  P2-10：返回 unknown[]——元素结构由调用方按 FlowNode/string 断言（见下两处 as）。 */
function parseJsonArray(raw: unknown): unknown[] {
  if (Array.isArray(raw)) return raw
  if (typeof raw !== 'string' || !raw.trim()) return []
  try { const v = JSON.parse(raw); return Array.isArray(v) ? v : [] } catch { return [] }
}

/** 生成流程节点中文展示标签。 */
function flowNodeLabel(row: CrudRow, id: unknown) {
  const nodes = parseJsonArray(row.nodes_json) as FlowNode[]
  const node = nodes.find((n) => String(n.id) === String(id))
  return node?.name || node?.id || String(id || '-')
}

/** 根据流程实例状态计算进度条比例。 */
function progressFor(row: CrudRow) {
  const nodes = parseJsonArray(row.nodes_json) as FlowNode[]
  const executed = parseJsonArray(row.executed_nodes) as string[]
  const total = Math.max(nodes.length || 1, executed.length || 1)
  const done = Math.max(executed.length, nodes.findIndex((n) => String(n.id) === String(row.current_node_id)) + 1)
  return Math.min(100, Math.round((done / total) * 100))
}

/** 把流程状态映射为展示主题色。 */
function statusTheme(status: unknown) {
  if (status === 'completed' || status === 'paid' || status === 1) return 'success'
  if (status === 'failed' || status === 'closed' || status === 'archived') return 'danger'
  if (status === 'suspended') return 'warning'
  return 'primary'
}

/** 流程引擎 Tab：查看流程定义、节点和实例状态。 */
export function FlowEngineTab({ configs }: { configs: Cfg[] }) {
  const { t } = useI18n()
  const [defs, setDefs] = useState<CrudRow[]>([])
  const [instances, setInstances] = useState<CrudRow[]>([])
  const [customers, setCustomers] = useState<{ label: string; value: number }[]>([])
  // 按客户拉会话下拉数据源（接口已接、状态已写；下拉选择器尚未接入 UI，
  // 故 conversations 目前只写不读——是未接线的半成品，不是死代码，勿删。
  // 接线时把 <Select options={conversations}> 挂到流程启动表单即可。）
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const [conversations, setConversations] = useState<{ label: string; value: number }[]>([])
  const [loading, setLoading] = useState(false)
  const [detail, setDetail] = useState<CrudRow | null>(null)
  const [advRow, setAdvRow] = useState<CrudRow | null>(null)
  const [route, setRoute] = useState('ai')
  const [start, setStart] = useState({ flow_code: '', customer_id: 0, conversation_id: 0 })

  const load = useCallback(async () => {
    setLoading(true)
    const [d, i, c] = await Promise.all([
      AUTH('/api/v1/flows?page_size=100'),
      AUTH('/api/v1/flows/instances'),
      AUTH('/api/v1/customers?page_size=50'),
    ])
    if (d?.code === 0) {
      setDefs(d.data?.list || [])
      if (!start.flow_code && (d.data?.list || []).length) setStart((s) => ({ ...s, flow_code: d.data.list[0].code || '' }))
    }
    if (i?.code === 0) setInstances(i.data || [])
    if (c?.code === 0) setCustomers((c.data?.list || []).map((x: CrudRow) => ({ label: `${x.id} ${x.name || t('common.customer')}`, value: Number(x.id) })))
    setLoading(false)
  }, [start.flow_code, t])

  useEffect(() => { void load() }, [load])

  useEffect(() => {
    if (!start.customer_id) { setConversations([]); return }
    ;(async () => {
      const j = await AUTH(`/api/v1/conversations?customer_id=${start.customer_id}&page_size=50`)
      if (j?.code === 0) setConversations((j.data?.list || []).map((x: CrudRow) => ({ label: t('flow.convLabel', { id: Number(x.id) }), value: Number(x.id) })))
    })()
  }, [start.customer_id, t])

  const defMap = useMemo(() => new Map(defs.map((d) => [Number(d.id), d])), [defs])
  const enrichedInstances = useMemo(() => instances.map((row) => {
    const def = defMap.get(Number(row.flow_def_id)) || {}
    const state = (parseJsonArray(row.state_json)[0] || {}) as Record<string, unknown>
    const raw = typeof row.state_json === 'string' ? (() => { try { return JSON.parse(row.state_json) } catch { return {} } })() : {}
    return {
      ...row,
      flow_code: def.code || row.flow_def_id,
      flow_name: def.name || '-',
      nodes_json: def.nodes_json || '',
      executed_nodes: raw?.executed_nodes || state?.executed_nodes || [],
      context: raw?.context || {},
    }
  }), [defMap, instances])

  const openDetail = async (row: CrudRow) => {
    const j = await AUTH(`/api/v1/flows/${row.id}`)
    if (j?.code === 0) {
      setDetail(j.data)
    }
  }

  const startFlow = async () => {
    if (!start.flow_code) { MessagePlugin.warning(t('flow.selectFlowWarn')); return }
    const j = await AUTH('/api/v1/flows/start', {
      method: 'POST',
      body: { flow_code: start.flow_code, customer_id: Number(start.customer_id) || undefined, conversation_id: Number(start.conversation_id) || undefined },
    })
    if (j?.code === 0) { MessagePlugin.success(j.message || t('flow.startedOk')); await load() }
  }

  const doAdvance = async () => {
    if (!advRow) return
    const j = await AUTH('/api/v1/flows/advance', { method: 'POST', body: { instance_id: Number(advRow.id), route } })
    if (j?.code === 0) { MessagePlugin.success(j.message || t('flow.advancedOk')); setAdvRow(null); await load() }
  }

  return (
    <div className="space-y-4">
      <Tabs defaultValue="instances" placement="top">
        <Tabs.TabPanel value="instances" label={t('flow.tabInstances')}>
          <div className="mt-3 bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center gap-3">
            <Button theme="primary" variant="outline" loading={loading} disabled={loading} onClick={() => void load()}>{t('flow.reload')}</Button>
            <span className="text-xs text-gray-400">{t('flow.instanceCount', { n: instances.length })}</span>
          </div>
          <div className="mt-4 bg-white rounded-lg shadow-sm p-4 overflow-x-auto">
            <Table
              rowKey="id"
              loading={loading}
              data={enrichedInstances as TableRowData[]}
              size="small"
              empty={t('flow.instancesEmpty')}
              pagination={{ defaultPageSize: 10 }}
              columns={[
                { colKey: 'id', title: t('flow.colInstance'), width: 90 },
                { colKey: 'flow_code', title: t('flow.colFlow'), width: 150 },
                { colKey: 'customer_id', title: t('common.customer'), width: 90 },
                { colKey: 'conversation_id', title: t('flow.colConversation'), width: 90 },
                { colKey: 'current_node_id', title: t('flow.colCurrentNode'), width: 160, cell: (p: CellProps) => flowNodeLabel(p.row, p.row.current_node_id) },
                { colKey: 'status', title: t('common.status'), width: 110, cell: (p: CellProps) => <Tag theme={statusTheme(p.row.status)}>{String(p.row.status || '-')}</Tag> },
                { colKey: 'progress', title: t('flow.colProgress'), width: 180, cell: (p: CellProps) => <Progress percentage={progressFor(p.row)} /> },
                { colKey: 'started_at', title: t('flow.colStartedAt'), width: 180 },
                {
                  colKey: 'actions', title: t('crud.actions'), width: 160, fixed: 'right', cell: (p: CellProps) => (
                    <div className="flex gap-2">
                      <Button size="small" variant="text" disabled={p.row.status !== 'running'} onClick={() => { setAdvRow(p.row); setRoute('ai') }}>{t('flow.advance')}</Button>
                      <Button size="small" variant="text" onClick={async () => { const j = await AUTH(`/api/v1/flows/instances/${p.row.id}`); if (j?.code === 0) { setDetail({ ...j.data, nodes_json: defMap.get(Number(j.data.flow_def_id))?.nodes_json || '' }); } }}>{t('common.detail')}</Button>
                    </div>
                  ),
                },
              ]}
            />
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="definitions" label={t('flow.tabDefinitions')}>
          <div className="mt-3 bg-white rounded-lg shadow-sm p-4">
            <Table
              rowKey="id"
              loading={loading}
              data={defs as TableRowData[]}
              size="small"
              empty={t('flow.defsEmpty')}
              pagination={{ defaultPageSize: 10 }}
              columns={[
                { colKey: 'code', title: t('common.code'), width: 180 },
                { colKey: 'name', title: t('common.name'), width: 180 },
                { colKey: 'version', title: t('common.version'), width: 110 },
                { colKey: 'start_node_id', title: t('flow.colStartNode'), width: 150 },
                { colKey: 'nodes', title: t('flow.colNodeCount'), width: 90, cell: (p: CellProps) => parseJsonArray(p.row.nodes_json).length },
                { colKey: 'edges', title: t('flow.colEdgeCount'), width: 90, cell: (p: CellProps) => parseJsonArray(p.row.edges_json).length },
                { colKey: 'is_default', title: t('flow.colDefault'), width: 90, cell: (p: CellProps) => p.row.is_default ? <Tag theme="success">{t('common.yes')}</Tag> : '-' },
                { colKey: 'status', title: t('common.status'), width: 100, cell: (p: CellProps) => <Tag theme={Number(p.row.status) === 1 ? 'success' : 'default'}>{Number(p.row.status) === 1 ? t('common.enable') : t('common.disable')}</Tag> },
                { colKey: 'actions', title: t('crud.actions'), width: 100, fixed: 'right', cell: (p: CellProps) => <Button size="small" variant="text" onClick={() => void openDetail(p.row)}>{t('flow.view')}</Button> },
              ]}
            />
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="ops" label={t('flow.tabOps')}>
          <div className="mt-3 bg-white rounded-lg shadow-sm p-5 grid grid-cols-1 md:grid-cols-4 gap-3">
            <div><div className="text-xs text-gray-500 mb-1">{t('flow.colFlow')}</div><Select value={start.flow_code} onChange={(v) => setStart((s) => ({ ...s, flow_code: String(v) }))} options={defs.map((d) => ({ label: `${d.code} ${d.name}`, value: String(d.code) }))} filterable style={{ width: '100%' }} /></div>
            <div><div className="text-xs text-gray-500 mb-1">{t('flow.colCustomerId')}</div><InputNumber value={start.customer_id} onChange={(v) => setStart((s) => ({ ...s, customer_id: Number(v || 0), conversation_id: 0 }))} style={{ width: '100%' }} /></div>
            <div><div className="text-xs text-gray-500 mb-1">{t('flow.convIdOptional')}</div><Input value={String(start.conversation_id || '')} onChange={(v) => setStart((s) => ({ ...s, conversation_id: Number(v || 0) }))} placeholder={t('common.optional')} style={{ width: '100%' }} /></div>
            <div className="flex items-end"><Button theme="primary" onClick={() => void startFlow()}>{t('flow.startFlow')}</Button></div>
            <div className="md:col-span-4 text-xs text-gray-400">{t('flow.opsCustomerHint', { list: customers.slice(0, 8).map((c) => c.label).join(' / ') })}</div>
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="overview" label={t('flow.tabOverview')}>
          <div className="mt-3 space-y-6">
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <div className="flex items-center gap-2 mb-5"><span className="text-lg">🗺️</span><div><h3 className="text-base font-bold text-gray-800">{t('flow.journeyTitle')}</h3><p className="text-xs text-gray-400 mt-0.5">{t('flow.journeyDesc')}</p></div></div>
              <div className="overflow-x-auto pb-4"><div className="flex items-center min-w-max">
                {JOURNEY_STAGES.map((s, i, arr) => (<><div key={s.t} className="flex flex-col items-center"><div style={nodeStyle(s.c)}><div className="font-bold text-sm">{t(s.t)}</div><div className="text-[10px] opacity-80 mt-0.5">{t(s.d)}</div></div><span className="text-[10px] text-gray-400 mt-1">{t('flow.stageLabel', { n: i + 1 })}</span></div>{i < arr.length - 1 && <span className="text-gray-300 text-xl px-2">→</span>}</>))}
              </div></div>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <div className="flex items-center gap-2 mb-4"><span className="text-lg">⚙️</span><div><h3 className="text-base font-bold text-gray-800">{t('flow.chainTitle')}</h3><p className="text-xs text-gray-400 mt-0.5">{CHAIN_STEP_KEYS.map((k) => t(k)).join(' → ')}</p></div></div>
              <div className="flex flex-wrap gap-2 text-xs">
                {CHAIN_STEP_KEYS.map((k, i, arr) => (<><span key={k} className="px-3 py-1.5 bg-indigo-50 text-indigo-700 rounded-lg">{t(k)}</span>{i < arr.length - 1 && <span className="text-gray-300 self-center">→</span>}</>))}
              </div>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <h3 className="text-base font-bold text-gray-800 mb-3">{t('flow.cfgSummary')}</h3>
              <div className="flex flex-wrap gap-2">
                {['tau', 'merge_window', 'stage_lock', 'aggressiveness_default', 'max_delay', 'min_delay', 'delay_range', 'model_priority', 'mock_mode', 'reply_probability'].map((k) => {
                  const cfg = configs.find((c) => c.key === k)
                  if (!cfg) return null
                  return <div key={k} className="px-3 py-1.5 bg-gray-50 border border-gray-200 rounded-lg text-xs"><span className="text-gray-500 font-mono">{cfg.key}</span> <span className="text-gray-800">= {cfg.value.length > 40 ? cfg.value.slice(0, 37) + '...' : cfg.value}</span></div>
                })}
              </div>
            </div>
          </div>
        </Tabs.TabPanel>
      </Tabs>

      <Dialog header={detail?.name ? t('flow.detailTitle', { name: String(detail.name) }) : t('flow.detailFallback')} visible={!!detail} onClose={() => setDetail(null)} width="760px" footer={<Button onClick={() => setDetail(null)}>{t('common.close')}</Button>}>
        {detail && (
          <div className="space-y-3 text-sm">
            {detail.id && <div><b>{t('flow.fieldId')}</b>{String(detail.id)}</div>}
            {detail.current_node_id !== undefined && <div><b>{t('flow.fieldCurrentNode')}</b>{flowNodeLabel(detail, detail.current_node_id)}</div>}
            {detail.status !== undefined && <div><b>{t('flow.fieldStatus')}</b>{String(detail.status)}</div>}
            {detail.nodes_json !== undefined && <div><b>{t('flow.fieldNodes')}</b>{(parseJsonArray(detail.nodes_json) as FlowNode[]).map((n) => <Tag key={n.id} className="mr-1 mb-1">{n.name || n.id}</Tag>)}</div>}
            {detail.edges_json !== undefined && <div className="text-xs text-gray-500">{JSON.stringify((parseJsonArray(detail.edges_json) as FlowEdge[]).map((e) => `${e.from}->${e.to}${e.condition ? `(${e.condition})` : ''}`), null, 2)}</div>}
            <pre className="text-xs whitespace-pre-wrap max-h-80 overflow-auto">{JSON.stringify(detail, null, 2)}</pre>
          </div>
        )}
      </Dialog>

      <Dialog header={t('flow.advanceTitle')} visible={!!advRow} onClose={() => setAdvRow(null)} onConfirm={doAdvance} confirmBtn={t('flow.advance')}>
        <div className="space-y-3">
          <p className="text-sm text-gray-600">{t('flow.advanceLine', { id: String(advRow?.id ?? ''), node: advRow ? flowNodeLabel(advRow, advRow.current_node_id) : '-' })}</p>
          <Select value={route} onChange={(v) => setRoute(String(v))} options={ROUTE_OPTS} style={{ width: '100%' }} />
        </div>
      </Dialog>
    </div>
  )
}
