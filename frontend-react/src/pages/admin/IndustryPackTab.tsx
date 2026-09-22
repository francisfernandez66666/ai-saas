// F6 行业包/租户绑定管理：查看当前绑定、两级行业/企业包绑定、部门包绑定。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, MessagePlugin, Select, Table, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
// P2-15(2026-09-20 批三)：原生弹窗 12 处统一替换为 MessagePlugin/confirmDialog，
// 与管理台其余破坏性操作确认口径一致（原生弹窗样式割裂且阻塞主线程）
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, TableRowData } from '../../types'

type Pack = {
  id: number
  code: string
  name: string
  industry: string
  version: string
  pack_level: string
  parent_code: string
  status: string
  file_size?: number
  updated_at?: string
}

type DeptBinding = {
  department_id: number
  pack_id: number
  pack_code: string
  applied_version: string
}

type Current = {
  bound: boolean
  industry?: Pack | null
  enterprise?: Pack | null
  departments?: DeptBinding[]
}

type DeptNode = {
  id: number
  name: string
  children?: DeptNode[]
}

type PackTemplateStat = {
  template_id: string
  pack_code: string
  pack_version: string
  sample_count: number
  hook_rate: number
  lead_rate: number
  pending_human_rate?: number
  avg_intent_delta?: number
  avg_eval_score?: number | null
}

type PackEffectRow = PackTemplateStat & {
  history_version?: string
  history_sample_count?: number
  history_hook_rate?: number
  history_lead_rate?: number
  hook_delta?: number
  lead_delta?: number
}

// 判优层行动建议卡片（批五 C·L1，只读展示）：与后端 strategy.TemplateSuggestion 对齐
type PackSuggestionView = {
  pack_code: string
  pack_version: string
  template_id: string
  anchor_type: number
  status: string
  sample_count: number
  reward_rate: number
  anchor_median_rate: number
  metric: string
  min_samples: number
  confidence: number
  reason: string
}

// 建议状态 → 徽标文案与 TDesign Tag 主题（走语义主题色，不硬编码色值）
const SUGGESTION_UI: Record<string, { label: string; theme: 'danger' | 'success' | 'warning' | 'default' }> = {
  suggest_review: { label: '建议下线/改稿', theme: 'danger' },
  leading: { label: '主力话术', theme: 'success' },
  keep_watching: { label: '保持观察', theme: 'default' },
  insufficient_samples: { label: '样本积累中', theme: 'warning' },
  no_peer: { label: '无同锚对照', theme: 'default' },
}

/** 把部门树扁平化为级联选择器可用的 options。 */
function flattenDepts(nodes: DeptNode[]): { label: string; value: number }[] {
  const out: { label: string; value: number }[] = []
  const walk = (list: DeptNode[]) => {
    list.forEach((n) => {
      out.push({ label: n.name || `部门#${n.id}`, value: Number(n.id) })
      if (n.children?.length) walk(n.children)
    })
  }
  walk(nodes || [])
  return out
}

/** 生成行业包展示标签，包含包名、版本和层级。 */
function packLabel(p: Pack) {
  return `${p.name} ${p.version}（${p.code}）`
}

/** 把行业包发布状态转换为中文标签。 */
function statusLabel(level: string) {
  return level === 'industry' ? '行业包' : level === 'enterprise' ? '企业包' : level === 'department' ? '部门包' : level || '包'
}

/** 把比例值格式化为百分比字符串。 */
function formatPercent(v: number) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`
}

/** 拆分语义化版本号数字段。 */
function parseVersionParts(v: string): number[] {
  return String(v || '').split('.').map((x) => {
    const n = Number(x)
    return Number.isFinite(n) ? n : 0
  })
}

/** 比较两个行业包版本号大小。 */
function compareVersion(a: string, b: string): number {
  const pa = parseVersionParts(a)
  const pb = parseVersionParts(b)
  const len = Math.max(pa.length, pb.length)
  for (let i = 0; i < len; i++) {
    const x = pa[i] || 0
    const y = pb[i] || 0
    if (x !== y) return x - y
  }
  return String(a || '').localeCompare(String(b || ''))
}

// IndustryPackTab 后台「行业包」管理页：行业包列表、内容项维护与上下架。
export default function IndustryPackTab() {
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [current, setCurrent] = useState<Current | null>(null)
  const [industryPacks, setIndustryPacks] = useState<Pack[]>([])
  const [enterprisePacks, setEnterprisePacks] = useState<Pack[]>([])
  const [departmentPacks, setDepartmentPacks] = useState<Pack[]>([])
  const [depts, setDepts] = useState<{ label: string; value: number }[]>([])
  const [packStats, setPackStats] = useState<PackTemplateStat[]>([])
  const [packSuggestions, setPackSuggestions] = useState<PackSuggestionView[]>([])
  const [packCode, setPackCode] = useState('')
  const [packVersion, setPackVersion] = useState('')
  const [industryPackId, setIndustryPackId] = useState<number | ''>('')
  const [enterprisePackId, setEnterprisePackId] = useState<number | ''>('')
  const [deptId, setDeptId] = useState<number | ''>('')
  const [deptPackId, setDeptPackId] = useState<number | ''>('')

  const loadCurrent = useCallback(async () => {
    const j = await AUTH('/api/v1/admin/packs/current')
    if (j?.code === 0) {
      const data = j.data || { bound: false }
      setCurrent(data)
      if (data.industry?.id) setIndustryPackId(Number(data.industry.id))
    }
  }, [])

  const loadIndustries = useCallback(async () => {
    const j = await AUTH('/api/v1/admin/packs?level=industry')
    if (j?.code === 0) setIndustryPacks(j.data || [])
  }, [])

  const loadEnterprises = useCallback(async (industryCode: string) => {
    if (!industryCode) { setEnterprisePacks([]); return }
    const j = await AUTH(`/api/v1/admin/packs?level=enterprise&parent_code=${encodeURIComponent(industryCode)}`)
    if (j?.code === 0) setEnterprisePacks(j.data || [])
  }, [])

  const loadDeptPacks = useCallback(async (enterpriseCode: string) => {
    if (!enterpriseCode) { setDepartmentPacks([]); return }
    const j = await AUTH(`/api/v1/admin/packs?level=department&parent_code=${encodeURIComponent(enterpriseCode)}`)
    if (j?.code === 0) setDepartmentPacks(j.data || [])
  }, [])

  const loadDepts = useCallback(async () => {
    const j = await AUTH('/api/v1/org/departments/tree')
    if (j?.code === 0) setDepts(flattenDepts(j.data || []))
  }, [])

  const loadAll = useCallback(async () => {
    setLoading(true)
    await Promise.all([loadCurrent(), loadIndustries(), loadDepts()])
    setLoading(false)
  }, [loadCurrent, loadDepts, loadIndustries])

  useEffect(() => { void loadAll() }, [loadAll])

  useEffect(() => {
    const selected = industryPacks.find((p) => p.id === Number(industryPackId))
    void loadEnterprises(selected?.code || '')
    if (!current?.enterprise || selected?.code !== current?.industry?.code) setEnterprisePackId('')
  }, [current, industryPackId, industryPacks, loadEnterprises])

  useEffect(() => {
    void loadDeptPacks(current?.enterprise?.code || '')
  }, [current, loadDeptPacks])

  useEffect(() => {
    const code = current?.enterprise?.code || current?.industry?.code || ''
    const version = current?.enterprise?.version || current?.industry?.version || ''
    setPackCode(code)
    setPackVersion(version)
    if (!code) {
      setPackStats([])
      setPackSuggestions([])
      return
    }
    ;(async () => {
      const q = new URLSearchParams({ days: '90', pack_code: code })
      const j = await AUTH(`/api/v1/admin/packs/stats?${q.toString()}`)
      if (j?.code === 0) {
        setPackStats((j.data?.list || []) as PackTemplateStat[])
        setPackSuggestions((j.data?.suggestions || []) as PackSuggestionView[])
      }
    })()
  }, [current])

  const selectedIndustry = useMemo(() => industryPacks.find((p) => p.id === Number(industryPackId)), [industryPackId, industryPacks])
  const selectedEnterprise = useMemo(() => enterprisePacks.find((p) => p.id === Number(enterprisePackId)), [enterprisePackId, enterprisePacks])

  const currentRows = useMemo(() => packStats.filter((r) => !packVersion || r.pack_version === packVersion), [packStats, packVersion])
  const historyRows = useMemo(() => packStats.filter((r) => packVersion ? r.pack_version !== packVersion : true), [packStats, packVersion])
  const historyVersion = useMemo(() => {
    const versions = Array.from(new Set(historyRows.map((r) => r.pack_version).filter(Boolean)))
    return versions.sort(compareVersion).reverse()[0] || ''
  }, [historyRows])
  const displayRows = useMemo(() => {
    const rows = (currentRows.length ? currentRows : packStats).map((r) => {
      const history = historyVersion ? historyRows.find((h) => h.template_id === r.template_id && h.pack_version === historyVersion) : undefined
      return {
        ...r,
        history_version: history?.pack_version || '',
        history_sample_count: history?.sample_count || 0,
        history_hook_rate: history?.hook_rate,
        history_lead_rate: history?.lead_rate,
        hook_delta: history ? Number(r.hook_rate || 0) - Number(history.hook_rate || 0) : undefined,
        lead_delta: history ? Number(r.lead_rate || 0) - Number(history.lead_rate || 0) : undefined,
      }
    })
    return rows.sort((a, b) => Number(b.sample_count || 0) - Number(a.sample_count || 0) || String(a.template_id || '').localeCompare(String(b.template_id || '')))
  }, [currentRows, historyRows, historyVersion, packStats])

  const packSummary = useMemo(() => {
    const total = displayRows.reduce((s, r) => s + Number(r.sample_count || 0), 0)
    if (!total) return { total: 0, hookRate: 0, leadRate: 0, pendingRate: 0, score: 0 }
    const scored = displayRows.filter((r) => typeof r.avg_eval_score === 'number')
    const scoreTotal = scored.reduce((s, r) => s + Number(r.sample_count || 0), 0)
    return {
      total,
      hookRate: displayRows.reduce((s, r) => s + Number(r.hook_rate || 0) * Number(r.sample_count || 0), 0) / total,
      leadRate: displayRows.reduce((s, r) => s + Number(r.lead_rate || 0) * Number(r.sample_count || 0), 0) / total,
      pendingRate: displayRows.reduce((s, r) => s + Number(r.pending_human_rate || 0) * Number(r.sample_count || 0), 0) / total,
      score: scoreTotal ? scored.reduce((s, r) => s + Number(r.avg_eval_score || 0) * Number(r.sample_count || 0), 0) / scoreTotal : 0,
    }
  }, [displayRows])

  const bind = async () => {
    if (!industryPackId) { MessagePlugin.warning('请选择行业包'); return }
    setSaving(true)
    const body: Record<string, any> = { industry_pack_id: Number(industryPackId) }
    if (enterprisePackId) body.enterprise_pack_id = Number(enterprisePackId)
    const j = await AUTH('/api/v1/admin/packs/bind', { method: 'POST', body })
    setSaving(false)
    if (j?.code === 0) {
      await Promise.all([loadCurrent(), loadIndustries(), loadDepts()])
      MessagePlugin.success(j.message || '已绑定')
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const unbind = async () => {
    if (!(await confirmDialog('该操作会清除已物化的包内容。', '确认解绑当前租户的行业包和企业包？'))) return
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/unbind', { method: 'POST' })
    setSaving(false)
    if (j?.code === 0) {
      setEnterprisePackId('')
      setDeptPackId('')
      await loadAll()
      MessagePlugin.success(j.message || '已解绑')
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const bindDept = async () => {
    if (!deptId || !deptPackId) { MessagePlugin.warning('请选择部门和部门包'); return }
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/bind-dept', {
      method: 'POST',
      body: { department_id: Number(deptId), pack_id: Number(deptPackId) },
    })
    setSaving(false)
    if (j?.code === 0) {
      setDeptPackId('')
      await loadCurrent()
      MessagePlugin.success(j.message || '部门包已绑定')
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const unbindDept = async (departmentId: number) => {
    if (!(await confirmDialog('解绑后该部门成员将回落租户级包。', '确认解绑该部门包？'))) return
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/unbind-dept', {
      method: 'POST',
      body: { department_id: departmentId },
    })
    setSaving(false)
    if (j?.code === 0) {
      await loadCurrent()
      MessagePlugin.success(j.message || '已解绑')
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">行业包绑定</h3>
            <p className="text-xs text-gray-400 mt-0.5">当前租户按「行业包 → 企业包 → 部门包」三层继承；先绑定行业/企业包，再绑定部门包。</p>
          </div>
          <div className="flex gap-2">
            <Button theme="default" variant="outline" loading={loading} disabled={loading} onClick={() => void loadAll()}>刷新</Button>
            <Button theme="primary" loading={saving} disabled={saving || !industryPackId} onClick={() => void bind()}>绑定</Button>
            <Button theme="danger" variant="outline" loading={saving} disabled={saving || !current?.bound} onClick={() => void unbind()}>解绑租户包</Button>
          </div>
        </div>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-gray-500 mb-1">行业包 *</div>
            <Select
              value={industryPackId}
              onChange={(v) => setIndustryPackId(v as number)}
              options={industryPacks.map((p) => ({ label: packLabel(p), value: p.id }))}
              filterable
              placeholder="选择行业包"
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">企业包（可选）</div>
            <Select
              value={enterprisePackId}
              onChange={(v) => setEnterprisePackId(v as number)}
              options={[{ label: '不绑定企业包', value: '' }, ...enterprisePacks.map((p) => ({ label: packLabel(p), value: p.id }))]}
              filterable
              disabled={!selectedIndustry}
              placeholder="选择企业包"
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">当前绑定</div>
            <div className="border border-gray-200 rounded-md p-3 min-h-[58px] bg-gray-50 text-sm">
              {current?.bound ? (
                <div>
                  <div><span className="text-gray-500">行业：</span>{current.industry?.name || '-'} <Tag theme="primary">{current.industry?.version}</Tag></div>
                  <div><span className="text-gray-500">企业：</span>{current.enterprise?.name || '未绑定'} {current.enterprise ? <Tag theme="success">{current.enterprise.version}</Tag> : null}</div>
                </div>
              ) : <span className="text-gray-400">未绑定</span>}
            </div>
          </div>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">部门包绑定</h3>
        <p className="text-xs text-gray-400 mt-0.5">仅当企业包存在且部门包挂靠该企业包时可绑定。</p>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-gray-500 mb-1">部门</div>
            <Select value={deptId} onChange={(v) => setDeptId(v as number)} options={depts} filterable placeholder="选择部门" style={{ width: '100%' }} />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">部门包</div>
            <Select
              value={deptPackId}
              onChange={(v) => setDeptPackId(v as number)}
              options={departmentPacks.map((p) => ({ label: packLabel(p), value: p.id }))}
              filterable
              disabled={!current?.enterprise}
              placeholder="选择部门包"
              style={{ width: '100%' }}
            />
          </div>
          <div className="flex items-end">
            <Button theme="primary" loading={saving} disabled={saving || !current?.enterprise || !deptId || !deptPackId} onClick={() => void bindDept()}>绑定部门包</Button>
          </div>
        </div>
        <div className="mt-4">
          <Table
            rowKey="department_id"
            loading={loading}
            data={(current?.departments || []) as TableRowData[]}
            size="small"
            empty="暂无部门包绑定"
            columns={[
              { colKey: 'department_id', title: '部门ID', width: 120 },
              { colKey: 'dept_name', title: '部门', width: 180, cell: (p: CellProps) => depts.find((d) => d.value === Number(p.row.department_id))?.label || `部门#${p.row.department_id}` },
              { colKey: 'pack_code', title: '包编码', width: 180 },
              { colKey: 'applied_version', title: '版本', width: 120, cell: (p: CellProps) => <Tag>{p.row.applied_version}</Tag> },
              { colKey: 'pack_level', title: '类型', width: 100, cell: () => <Tag theme="success">{statusLabel('department')}</Tag> },
              { colKey: 'actions', title: '操作', width: 120, fixed: 'right', cell: (p: CellProps) => <Button size="small" variant="text" theme="danger" onClick={() => void unbindDept(Number(p.row.department_id))}>解绑</Button> },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">包效果归因</h3>
            <p className="text-xs text-gray-400 mt-0.5">按当前生效包统计近 30 天 AI 回复的接钩、留资、待人工与意向变化；样本不足 50 条仅作趋势参考。</p>
          </div>
          {packCode ? <div className="text-xs text-gray-500">{packCode}{packVersion ? ` · ${packVersion}` : ''}</div> : null}
        </div>
        {packSummary.total > 0 && (
          <div className="mt-4 grid grid-cols-2 md:grid-cols-4 gap-3">
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">样本</div><div className="text-lg font-bold text-gray-800">{packSummary.total}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">接钩率</div><div className="text-lg font-bold text-blue-600">{formatPercent(packSummary.hookRate)}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">留资率</div><div className="text-lg font-bold text-emerald-600">{formatPercent(packSummary.leadRate)}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">待人工率</div><div className="text-lg font-bold text-amber-600">{formatPercent(packSummary.pendingRate)}</div></div>
          </div>
        )}
        {packSuggestions.length > 0 && (
          <div className="mt-4 rounded-md border border-gray-200 p-3">
            <div className="text-xs font-bold text-gray-700 mb-2">判优建议（仅建议，人工确认后才生效）</div>
            <div className="space-y-1.5">
              {packSuggestions.slice(0, 8).map((s) => {
                const ui = SUGGESTION_UI[s.status] || { label: s.status, theme: 'default' as const }
                return (
                  <div key={`${s.pack_code}-${s.pack_version}-${s.template_id}`} className="flex flex-wrap items-center gap-2 text-xs">
                    <Tag theme={ui.theme}>{ui.label}</Tag>
                    <span className="text-gray-700 font-medium">{s.template_id}</span>
                    <span className="text-gray-500">锚 {s.anchor_type} · {s.metric} {formatPercent(s.reward_rate)}（同锚中位 {formatPercent(s.anchor_median_rate)} · n={s.sample_count}）</span>
                    <span className="text-gray-400">{s.reason}</span>
                  </div>
                )
              })}
            </div>
          </div>
        )}
        <div className="mt-4 overflow-x-auto">
          <Table
            rowKey="template_id"
            data={displayRows as TableRowData[]}
            size="small"
            empty={packCode ? '暂无归因样本，数据积累中' : '绑定行业包后开始归因'}
            pagination={{ defaultPageSize: 10 }}
            columns={[
              { colKey: 'template_id', title: '模板', width: 220 },
              { colKey: 'sample_count', title: '样本', width: 100, cell: (p: CellProps) => Number(p.row.sample_count || 0) < 50 ? <Tag theme="warning">积累中 {p.row.sample_count}</Tag> : p.row.sample_count },
              { colKey: 'hook_rate', title: '接钩率', width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.hook_rate || 0)) },
              { colKey: 'lead_rate', title: '留资率', width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.lead_rate || 0)) },
              { colKey: 'pending_human_rate', title: '待人工', width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.pending_human_rate || 0)) },
              { colKey: 'avg_intent_delta', title: '意向变化', width: 120, cell: (p: CellProps) => `${(Number(p.row.avg_intent_delta || 0) * 100).toFixed(1)}%` },
              { colKey: 'avg_eval_score', title: '质量分', width: 100, cell: (p: CellProps) => typeof p.row.avg_eval_score === 'number' ? p.row.avg_eval_score.toFixed(0) : '-' },
              {
                colKey: 'history_compare',
                title: `历史对比${historyVersion ? ` ${historyVersion}` : ''}`,
                width: 190,
                cell: (p: CellProps) => {
                  if (!p.row.history_version) return <span className="text-xs text-gray-400">-</span>
                  return (
                    <div className="text-xs leading-5">
                      <div>钩 {formatPercent(Number(p.row.history_hook_rate || 0))} · Δ {Number(p.row.hook_delta || 0) >= 0 ? '+' : ''}{(Number(p.row.hook_delta || 0) * 100).toFixed(1)}%</div>
                      <div>资 {formatPercent(Number(p.row.history_lead_rate || 0))} · Δ {Number(p.row.lead_delta || 0) >= 0 ? '+' : ''}{(Number(p.row.lead_delta || 0) * 100).toFixed(1)}%</div>
                    </div>
                  )
                },
              },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">可选包目录</h3>
        <div className="mt-3 overflow-x-auto">
          <Table
            rowKey="id"
            loading={loading}
            data={[...industryPacks, ...enterprisePacks, ...departmentPacks] as TableRowData[]}
            size="small"
            empty="暂无已上架包"
            pagination={{ defaultPageSize: 10 }}
            columns={[
              { colKey: 'code', title: '编码', width: 150 },
              { colKey: 'name', title: '名称', width: 220 },
              { colKey: 'pack_level', title: '层级', width: 120, cell: (p: CellProps) => <Tag theme={p.row.pack_level === 'industry' ? 'primary' : p.row.pack_level === 'enterprise' ? 'success' : 'default'}>{statusLabel(String(p.row.pack_level))}</Tag> },
              { colKey: 'parent_code', title: '父包', width: 140 },
              { colKey: 'version', title: '版本', width: 120 },
              { colKey: 'industry', title: '行业', width: 120 },
              { colKey: 'status', title: '状态', width: 100, cell: (p: CellProps) => <Tag theme={p.row.status === 'active' ? 'success' : 'default'}>{p.row.status}</Tag> },
            ]}
          />
        </div>
      </div>
    </div>
  )
}
