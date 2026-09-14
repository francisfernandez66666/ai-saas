// F7 流程引擎管理页：定义查看/实例列表/状态进度/手动推进/启动实例。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Dialog, Input, InputNumber, MessagePlugin, Progress, Select, Table, Tabs, Tag } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import type { CellProps, FlowEdge, FlowNode, TableRowData } from '../../types'
import type { Cfg } from './shared'

/** 生成流程画布节点样式。 */
const nodeStyle = (c: string): React.CSSProperties => ({ minWidth: 120, padding: '10px 14px', borderRadius: 10, color: '#fff', textAlign: 'center', fontSize: 13, fontWeight: 600, background: c })
const ROUTE_OPTS = [{ label: 'ai', value: 'ai' }, { label: 'human', value: 'human' }, { label: 'fish', value: 'fish' }, { label: 'pending_human', value: 'pending_human' }]

/** 解析流程配置中的 JSON 数组。 */
function parseJsonArray(raw: unknown): any[] {
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
  const [defs, setDefs] = useState<CrudRow[]>([])
  const [instances, setInstances] = useState<CrudRow[]>([])
  const [customers, setCustomers] = useState<{ label: string; value: number }[]>([])
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
    if (c?.code === 0) setCustomers((c.data?.list || []).map((x: CrudRow) => ({ label: `${x.id} ${x.name || '客户'}`, value: Number(x.id) })))
    setLoading(false)
  }, [start.flow_code])

  useEffect(() => { void load() }, [load])

  useEffect(() => {
    if (!start.customer_id) { setConversations([]); return }
    ;(async () => {
      const j = await AUTH(`/api/v1/conversations?customer_id=${start.customer_id}&page_size=50`)
      if (j?.code === 0) setConversations((j.data?.list || []).map((x: CrudRow) => ({ label: `会话 ${x.id}`, value: Number(x.id) })))
    })()
  }, [start.customer_id])

  const defMap = useMemo(() => new Map(defs.map((d) => [Number(d.id), d])), [defs])
  const enrichedInstances = useMemo(() => instances.map((row) => {
    const def = defMap.get(Number(row.flow_def_id)) || {}
    const state = parseJsonArray(row.state_json)[0] || {}
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
    if (!start.flow_code) { MessagePlugin.warning('请选择流程'); return }
    const j = await AUTH('/api/v1/flows/start', {
      method: 'POST',
      body: { flow_code: start.flow_code, customer_id: Number(start.customer_id) || undefined, conversation_id: Number(start.conversation_id) || undefined },
    })
    if (j?.code === 0) { MessagePlugin.success(j.message || '流程已启动'); await load() }
  }

  const doAdvance = async () => {
    if (!advRow) return
    const j = await AUTH('/api/v1/flows/advance', { method: 'POST', body: { instance_id: Number(advRow.id), route } })
    if (j?.code === 0) { MessagePlugin.success(j.message || '流程已推进'); setAdvRow(null); await load() }
  }

  return (
    <div className="space-y-4">
      <Tabs defaultValue="instances" placement="top">
        <Tabs.TabPanel value="instances" label="流程实例">
          <div className="mt-3 bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center gap-3">
            <Button theme="primary" variant="outline" loading={loading} disabled={loading} onClick={() => void load()}>刷新实例</Button>
            <span className="text-xs text-gray-400">共 {instances.length} 条最近实例</span>
          </div>
          <div className="mt-4 bg-white rounded-lg shadow-sm p-4 overflow-x-auto">
            <Table
              rowKey="id"
              loading={loading}
              data={enrichedInstances as TableRowData[]}
              size="small"
              empty="暂无流程实例"
              pagination={{ defaultPageSize: 10 }}
              columns={[
                { colKey: 'id', title: '实例', width: 90 },
                { colKey: 'flow_code', title: '流程', width: 150 },
                { colKey: 'customer_id', title: '客户', width: 90 },
                { colKey: 'conversation_id', title: '会话', width: 90 },
                { colKey: 'current_node_id', title: '当前节点', width: 160, cell: (p: CellProps) => flowNodeLabel(p.row, p.row.current_node_id) },
                { colKey: 'status', title: '状态', width: 110, cell: (p: CellProps) => <Tag theme={statusTheme(p.row.status)}>{String(p.row.status || '-')}</Tag> },
                { colKey: 'progress', title: '进度', width: 180, cell: (p: CellProps) => <Progress percentage={progressFor(p.row)} /> },
                { colKey: 'started_at', title: '开始时间', width: 180 },
                {
                  colKey: 'actions', title: '操作', width: 160, fixed: 'right', cell: (p: CellProps) => (
                    <div className="flex gap-2">
                      <Button size="small" variant="text" disabled={p.row.status !== 'running'} onClick={() => { setAdvRow(p.row); setRoute('ai') }}>推进</Button>
                      <Button size="small" variant="text" onClick={async () => { const j = await AUTH(`/api/v1/flows/instances/${p.row.id}`); if (j?.code === 0) { setDetail({ ...j.data, nodes_json: defMap.get(Number(j.data.flow_def_id))?.nodes_json || '' }); } }}>详情</Button>
                    </div>
                  ),
                },
              ]}
            />
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="definitions" label="流程定义">
          <div className="mt-3 bg-white rounded-lg shadow-sm p-4">
            <Table
              rowKey="id"
              loading={loading}
              data={defs as TableRowData[]}
              size="small"
              empty="暂无流程定义"
              pagination={{ defaultPageSize: 10 }}
              columns={[
                { colKey: 'code', title: '编码', width: 180 },
                { colKey: 'name', title: '名称', width: 180 },
                { colKey: 'version', title: '版本', width: 110 },
                { colKey: 'start_node_id', title: '起点', width: 150 },
                { colKey: 'nodes', title: '节点数', width: 90, cell: (p: CellProps) => parseJsonArray(p.row.nodes_json).length },
                { colKey: 'edges', title: '连线数', width: 90, cell: (p: CellProps) => parseJsonArray(p.row.edges_json).length },
                { colKey: 'is_default', title: '默认', width: 90, cell: (p: CellProps) => p.row.is_default ? <Tag theme="success">是</Tag> : '-' },
                { colKey: 'status', title: '状态', width: 100, cell: (p: CellProps) => <Tag theme={Number(p.row.status) === 1 ? 'success' : 'default'}>{Number(p.row.status) === 1 ? '启用' : '停用'}</Tag> },
                { colKey: 'actions', title: '操作', width: 100, fixed: 'right', cell: (p: CellProps) => <Button size="small" variant="text" onClick={() => void openDetail(p.row)}>查看</Button> },
              ]}
            />
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="ops" label="启动流程">
          <div className="mt-3 bg-white rounded-lg shadow-sm p-5 grid grid-cols-1 md:grid-cols-4 gap-3">
            <div><div className="text-xs text-gray-500 mb-1">流程</div><Select value={start.flow_code} onChange={(v) => setStart((s) => ({ ...s, flow_code: String(v) }))} options={defs.map((d) => ({ label: `${d.code} ${d.name}`, value: String(d.code) }))} filterable style={{ width: '100%' }} /></div>
            <div><div className="text-xs text-gray-500 mb-1">客户ID</div><InputNumber value={start.customer_id} onChange={(v) => setStart((s) => ({ ...s, customer_id: Number(v || 0), conversation_id: 0 }))} style={{ width: '100%' }} /></div>
            <div><div className="text-xs text-gray-500 mb-1">会话ID（可选）</div><Input value={String(start.conversation_id || '')} onChange={(v) => setStart((s) => ({ ...s, conversation_id: Number(v || 0) }))} placeholder="可选" style={{ width: '100%' }} /></div>
            <div className="flex items-end"><Button theme="primary" onClick={() => void startFlow()}>启动</Button></div>
            <div className="md:col-span-4 text-xs text-gray-400">客户选择参考：{customers.slice(0, 8).map((c) => c.label).join(' / ')}；输入客户 ID 后可选择其会话。</div>
          </div>
        </Tabs.TabPanel>

        <Tabs.TabPanel value="overview" label="流程图">
          <div className="mt-3 space-y-6">
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <div className="flex items-center gap-2 mb-5"><span className="text-lg">🗺️</span><div><h3 className="text-base font-bold text-gray-800">客户旅程流程图</h3><p className="text-xs text-gray-400 mt-0.5">6 个主流程阶段 + 1 个独立战败分支</p></div></div>
              <div className="overflow-x-auto pb-4"><div className="flex items-center min-w-max">
                {[
                  { t: 'AI建联', d: 'AI自动首次触达', c: '#6366f1' },
                  { t: '人工建联', d: '顾问接手跟进', c: '#0ea5e9' },
                  { t: '已留资', d: '客户留下联系方式', c: '#06b6d4' },
                  { t: '已到店', d: '客户到店看车', c: '#22c55e' },
                  { t: '已下单', d: '客户支付定金', c: '#f97316' },
                  { t: '已交车', d: '完成交付，成交闭环', c: '#ef4444' },
                ].map((s, i, arr) => (<><div key={s.t} className="flex flex-col items-center"><div style={nodeStyle(s.c)}><div className="font-bold text-sm">{s.t}</div><div className="text-[10px] opacity-80 mt-0.5">{s.d}</div></div><span className="text-[10px] text-gray-400 mt-1">Stage {i + 1}</span></div>{i < arr.length - 1 && <span className="text-gray-300 text-xl px-2">→</span>}</>))}
              </div></div>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <div className="flex items-center gap-2 mb-4"><span className="text-lg">⚙️</span><div><h3 className="text-base font-bold text-gray-800">策略决策链</h3><p className="text-xs text-gray-400 mt-0.5">意图分析 → 锚点选择 → 阶段锁 → 模板匹配 → 降级 → 回复生成</p></div></div>
              <div className="flex flex-wrap gap-2 text-xs">
                {['意图分析', '锚点选择', '阶段锁', '模板匹配', '降级判断', '回复生成'].map((s, i, arr) => (<><span key={s} className="px-3 py-1.5 bg-indigo-50 text-indigo-700 rounded-lg">{s}</span>{i < arr.length - 1 && <span className="text-gray-300 self-center">→</span>}</>))}
              </div>
            </div>
            <div className="bg-white rounded-xl border border-gray-200 p-6">
              <h3 className="text-base font-bold text-gray-800 mb-3">当前配置参数摘要</h3>
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

      <Dialog header={detail?.name ? `${detail.name} 详情` : '详情'} visible={!!detail} onClose={() => setDetail(null)} width="760px" footer={<Button onClick={() => setDetail(null)}>关闭</Button>}>
        {detail && (
          <div className="space-y-3 text-sm">
            {detail.id && <div><b>ID：</b>{String(detail.id)}</div>}
            {detail.current_node_id !== undefined && <div><b>当前节点：</b>{flowNodeLabel(detail, detail.current_node_id)}</div>}
            {detail.status !== undefined && <div><b>状态：</b>{String(detail.status)}</div>}
            {detail.nodes_json !== undefined && <div><b>节点：</b>{parseJsonArray(detail.nodes_json).map((n: FlowNode) => <Tag key={n.id} className="mr-1 mb-1">{n.name || n.id}</Tag>)}</div>}
            {detail.edges_json !== undefined && <div className="text-xs text-gray-500">{JSON.stringify(parseJsonArray(detail.edges_json).map((e: FlowEdge) => `${e.from}->${e.to}${e.condition ? `(${e.condition})` : ''}`), null, 2)}</div>}
            <pre className="text-xs whitespace-pre-wrap max-h-80 overflow-auto">{JSON.stringify(detail, null, 2)}</pre>
          </div>
        )}
      </Dialog>

      <Dialog header="手动推进流程" visible={!!advRow} onClose={() => setAdvRow(null)} onConfirm={doAdvance} confirmBtn="推进">
        <div className="space-y-3">
          <p className="text-sm text-gray-600">实例 #{advRow?.id} 当前节点：{advRow ? flowNodeLabel(advRow, advRow.current_node_id) : '-'}</p>
          <Select value={route} onChange={(v) => setRoute(String(v))} options={ROUTE_OPTS} style={{ width: '100%' }} />
        </div>
      </Dialog>
    </div>
  )
}
