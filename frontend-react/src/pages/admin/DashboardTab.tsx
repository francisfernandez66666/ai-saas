// 后台工作台（F1/F9）：经营概览、顾问漏斗、模型健康、意向分布、快捷入口、最新线索。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Progress, Table, Tag } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH, getToken } from '../../lib/api'
import type { CellProps, TableRowData } from '../../types'

type Overview = {
  total_customers: number
  new_customers_today: number
  active_conversations: number
  conversion_rate: number
  avg_intent_score: number
  human_transfer_rate: number
}

type StatItem = { label: string; value: number; color?: string }

const COLOR_MAP: Record<string, string> = {
  blue: '#4f46e5',
  green: '#16a34a',
  purple: '#7c3aed',
  orange: '#ea580c',
  red: '#dc2626',
}

const STAGE_CN: Record<string, string> = {
  ai_connected: 'AI建联',
  human_connected: '人工建联',
  lead_captured: '已留资',
  arrived: '已到店',
  ordered: '已下单',
  delivered: '已交车',
  lost: '已战败',
}

/** 把数值按百分比展示。 */
function pct(v: number) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`
}

/** 格式化模型冷却剩余秒数。 */
function formatCooldown(sec: number) {
  if (!sec) return '-'
  if (sec < 60) return `${sec}s`
  return `${Math.ceil(sec / 60)}min`
}

/** 后台工作台：聚合核心指标、模型状态和近期运营事件。 */
export function DashboardTab() {
  const [overview, setOverview] = useState<Overview | null>(null)
  const [advisorStats, setAdvisorStats] = useState<StatItem[]>([])
  const [models, setModels] = useState<CrudRow[]>([])
  const [recent, setRecent] = useState<TableRowData[]>([])
  const [loading, setLoading] = useState(false)
  const entries = [
    { title: '顾问工作台', desc: '客户会话、跟进、AI接管', icon: '🧑‍💼', href: '/advisor' },
    { title: '收银台', desc: '三桶余额、套餐充值、发票', icon: '💰', href: '/billing' },
    { title: '组织架构', desc: '部门树与成员管理', icon: '🏢', href: '/org' },
    { title: '客户对话页', desc: 'C端访客聊天入口', icon: '💬', href: '/client' },
    { title: '定价方案', desc: '套餐与 AI 商业包', icon: '🧾', href: '/pricing' },
    { title: '移动端工作台', desc: '/app 一站式入口', icon: '📱', href: '/app' },
  ]

  const load = useCallback(async () => {
    setLoading(true)
    const [o, s, m] = await Promise.all([
      AUTH('/api/v1/stats/overview'),
      AUTH('/api/v1/advisor/stats'),
      AUTH('/api/v1/admin/models'),
    ])
    if (o?.code === 0) setOverview(o.data || null)
    if (s?.code === 0) setAdvisorStats(s.data || [])
    if (m?.code === 0) setModels(m.data?.models || [])
    const [r, i] = await Promise.all([
      fetch('/api/v1/advisor/customers?status=&page=1&page_size=8&assigned=all', { headers: { Authorization: 'Bearer ' + getToken() } }).then((x) => x.json()).catch(() => null),
      AUTH('/api/v1/customers?page_size=100'),
    ])
    if (r?.code === 0) setRecent(r.data?.list || [])
    if (i?.code === 0) setRecent((prev) => prev.length ? prev : (i.data?.list || []).slice(0, 8))
    setLoading(false)
  }, [])

  useEffect(() => { void load() }, [load])

  const intent = useMemo(() => {
    const list = (recent as CrudRow[])
    const low = list.filter((c) => Number(c.intent_score || 0) < 0.4).length
    const mid = list.filter((c) => Number(c.intent_score || 0) >= 0.4 && Number(c.intent_score || 0) < 0.8).length
    const high = list.filter((c) => Number(c.intent_score || 0) >= 0.8).length
    const total = Math.max(list.length, 1)
    return { low, mid, high, total: list.length, lowPct: Math.round((low / total) * 100), midPct: Math.round((mid / total) * 100), highPct: Math.round((high / total) * 100) }
  }, [recent])

  return (
    <div className="space-y-6">
      <div className="bg-white rounded-xl border border-gray-200 p-5">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800 mb-1">今日概览</h3>
            <p className="text-xs text-gray-400 mb-4">租户经营指标、顾问漏斗、模型健康与意向分布。</p>
          </div>
          <Button theme="default" variant="outline" loading={loading} disabled={loading} onClick={() => void load()}>刷新</Button>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.total_customers ?? '-'}</b><span className="text-xs text-gray-500">有效客户</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.new_customers_today ?? '-'}</b><span className="text-xs text-gray-500">今日新增</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.active_conversations ?? '-'}</b><span className="text-xs text-gray-500">活跃会话</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{pct(overview?.conversion_rate || 0)}</b><span className="text-xs text-gray-500">转化率</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{Number(overview?.avg_intent_score || 0).toFixed(2)}</b><span className="text-xs text-gray-500">平均意向</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{pct(overview?.human_transfer_rate || 0)}</b><span className="text-xs text-gray-500">转人工率</span></div>
        </div>
        <div className="mt-4 grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
          {advisorStats.map((s, i) => (
            <div key={i} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl" style={{ color: COLOR_MAP[s.color] || '#1f2937' }}>{s.value}</b>
              <p className="text-xs text-gray-500 mt-1">{s.label}</p>
            </div>
          ))}
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <div className="bg-white rounded-xl border border-gray-200 p-5">
          <h3 className="text-base font-bold text-gray-800 mb-4">意向分布</h3>
          <div className="space-y-3">
            <div><div className="flex justify-between text-sm mb-1"><span>高意向 ≥80</span><span>{intent.high} 人</span></div><Progress color="#16a34a" percentage={intent.highPct} /></div>
            <div><div className="flex justify-between text-sm mb-1"><span>中意向 40-79</span><span>{intent.mid} 人</span></div><Progress color="#ea580c" percentage={intent.midPct} /></div>
            <div><div className="flex justify-between text-sm mb-1"><span>低意向 &lt;40</span><span>{intent.low} 人</span></div><Progress percentage={intent.lowPct} /></div>
          </div>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-5">
          <h3 className="text-base font-bold text-gray-800 mb-4">模型健康</h3>
          <Table
            rowKey="model_name"
            loading={loading}
            data={models as TableRowData[]}
            size="small"
            empty="暂无模型状态"
            columns={[
              { colKey: 'display_name', title: '模型', width: 220, ellipsis: true },
              { colKey: 'provider', title: '供应商', width: 110 },
              { colKey: 'available', title: '状态', width: 100, cell: (p: CellProps) => <Tag theme={p.row.available ? 'success' : 'danger'}>{p.row.available ? '可用' : '冷却'}</Tag> },
              { colKey: 'consecutive_fails', title: '失败', width: 80 },
              { colKey: 'cooldown_left_sec', title: '冷却', width: 90, cell: (p: CellProps) => formatCooldown(Number(p.row.cooldown_left_sec || 0)) },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 p-5">
        <h3 className="text-base font-bold text-gray-800 mb-4">快捷入口</h3>
        <div className="grid grid-cols-2 md:grid-cols-3 gap-3">
          {entries.map((e) => (
            <a key={e.title} href={e.href} className="flex items-center gap-3 bg-gray-50 hover:bg-gray-100 rounded-lg p-4 border border-gray-100 no-underline">
              <span style={{ fontSize: 22 }}>{e.icon}</span>
              <span>
                <b className="block text-sm text-gray-800">{e.title}</b>
                <span className="block text-xs text-gray-400 mt-0.5">{e.desc}</span>
              </span>
            </a>
          ))}
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 p-5">
        <h3 className="text-base font-bold text-gray-800 mb-4">最新客户线索</h3>
        <div className="space-y-2">
          {recent.length === 0 && <p className="text-sm text-gray-400 py-4 text-center">暂无客户</p>}
          {recent.map((l: CrudRow) => (
            <div key={l.id} className="flex items-center justify-between bg-gray-50 rounded-lg px-4 py-3">
              <div className="flex items-center gap-3">
                <div className="w-8 h-8 bg-white rounded-full border border-gray-200 flex items-center justify-center text-xs font-medium text-gray-500">{((l.name || '客')[0] || '客')}</div>
                <div>
                  <span className="text-sm font-medium text-gray-800">{l.name && !l.name.startsWith('访客_') ? l.name : '客户'}</span>
                  <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-gray-100 text-gray-500">{STAGE_CN[l.journey_stage] || l.journey_stage || '-'}</span>
                  <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-indigo-50 text-indigo-600">意向 {Number(l.intent_score || 0).toFixed(2)}</span>
                </div>
              </div>
              <span className="text-xs text-gray-400 truncate max-w-[45%]">{l.last_message || '暂无消息'}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
