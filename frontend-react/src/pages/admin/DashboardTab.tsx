// 后台工作台（F1/F9）：经营概览、AI 贡献度、顾问漏斗、模型健康、意向分布、快捷入口、最新线索。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Progress, Table, Tag } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { isVisitorName } from '../../lib/visitor'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, ContributionDrill, TableRowData } from '../../types'
import { AIContributionCard } from './AIContributionCard'
import { QuotaAlertCard } from './QuotaAlertCard'

// Overview 工作台经营概览接口的数据：客户存量/新增、活跃会话与转化/转人工等核心比率。
type Overview = {
  total_customers: number
  new_customers_today: number
  active_conversations: number
  conversion_rate: number
  avg_intent_score: number
  human_transfer_rate: number
}

// StatItem 工作台指标卡的一项：展示名、数值与可选主题色（色值仅用于呈现，不参与计算）。
type StatItem = { label: string; value: number; color?: string }

const COLOR_MAP: Record<string, string> = {
  blue: '#4f46e5',
  green: '#16a34a',
  purple: '#7c3aed',
  orange: '#ea580c',
  red: '#dc2626',
}

// 旅程阶段码 → 展示键。阶段码是后端契约；中文名与客户列表同一套（customers.stage*），不另造词
const STAGE_KEYS: Record<string, I18nKey> = {
  ai_connected: 'customers.stageAiConnected',
  human_connected: 'customers.stageHumanConnected',
  lead_captured: 'customers.stageLeadCaptured',
  arrived: 'customers.stageArrived',
  ordered: 'customers.stageOrdered',
  delivered: 'customers.stageDelivered',
  lost: 'customers.stageLost',
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

/** 后台工作台：聚合核心指标、模型状态和近期运营事件。
 *  onDrill 由 Admin 持有（下钻目标「客户线索」是同级 Tab，工作台自己不做跨 Tab 跳转），
 *  工作台只负责把「指标 + 窗口」原样递出去。 */
export function DashboardTab({ onDrill }: { onDrill?: (drill: ContributionDrill) => void }) {
  const { t } = useI18n()
  const [overview, setOverview] = useState<Overview | null>(null)
  const [advisorStats, setAdvisorStats] = useState<StatItem[]>([])
  const [models, setModels] = useState<CrudRow[]>([])
  const [recent, setRecent] = useState<TableRowData[]>([])
  const [loading, setLoading] = useState(false)
  // 快捷入口：标题/描述是界面文案，必须在渲染期经 t() 取，故定义在组件体内而非模块常量
  const entries = [
    { title: t('dashboard.entryAdvisor'), desc: t('dashboard.entryAdvisorDesc'), icon: '🧑‍💼', href: '/advisor' },
    { title: t('dashboard.entryBilling'), desc: t('dashboard.entryBillingDesc'), icon: '💰', href: '/billing' },
    { title: t('dashboard.entryOrg'), desc: t('dashboard.entryOrgDesc'), icon: '🏢', href: '/org' },
    { title: t('dashboard.entryClient'), desc: t('dashboard.entryClientDesc'), icon: '💬', href: '/client' },
    { title: t('dashboard.entryPricing'), desc: t('dashboard.entryPricingDesc'), icon: '🧾', href: '/pricing' },
    { title: t('dashboard.entryApp'), desc: t('dashboard.entryAppDesc'), icon: '📱', href: '/app' },
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
      // P2-6(2026-09-19 批三)：原裸 fetch 只带 Authorization——super 代管(X-Tenant-ID≠登录租户)
      // 下 TenantConsistency 400 且 .catch(()=>null) 静默；改走 AUTH 统一头/超时/401 登出
      AUTH('/api/v1/advisor/customers?status=&page=1&page_size=8&assigned=all'),
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
            <h3 className="text-base font-bold text-gray-800 mb-1">{t('dashboard.todayOverview')}</h3>
            <p className="text-xs text-gray-400 mb-4">{t('dashboard.subtitle')}</p>
          </div>
          <Button theme="default" variant="outline" loading={loading} disabled={loading} onClick={() => void load()}>{t('crud.refresh')}</Button>
        </div>
        <div className="grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.total_customers ?? '-'}</b><span className="text-xs text-gray-500">{t('dashboard.statCustomers')}</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.new_customers_today ?? '-'}</b><span className="text-xs text-gray-500">{t('dashboard.statNewToday')}</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{overview?.active_conversations ?? '-'}</b><span className="text-xs text-gray-500">{t('dashboard.statActiveConv')}</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{pct(overview?.conversion_rate || 0)}</b><span className="text-xs text-gray-500">{t('dashboard.statConversion')}</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{Number(overview?.avg_intent_score || 0).toFixed(2)}</b><span className="text-xs text-gray-500">{t('dashboard.statAvgIntent')}</span></div>
          <div className="bg-gray-50 rounded-lg p-4"><b className="block text-2xl text-gray-800">{pct(overview?.human_transfer_rate || 0)}</b><span className="text-xs text-gray-500">{t('dashboard.statHumanRate')}</span></div>
        </div>
        <div className="mt-4 grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
          {advisorStats.map((s, i) => (
            <div key={i} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl" style={{ color: COLOR_MAP[s.color ?? ''] || '#1f2937' }}>{s.value}</b>
              <p className="text-xs text-gray-500 mt-1">{s.label}</p>
            </div>
          ))}
        </div>
      </div>

      <AIContributionCard onDrill={onDrill} />

      <QuotaAlertCard />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
        <div className="bg-white rounded-xl border border-gray-200 p-5">
          <h3 className="text-base font-bold text-gray-800 mb-4">{t('dashboard.intentDist')}</h3>
          <div className="space-y-3">
            <div><div className="flex justify-between text-sm mb-1"><span>{t('dashboard.intentHigh')}</span><span>{t('dashboard.personCount', { count: intent.high })}</span></div><Progress color="#16a34a" percentage={intent.highPct} /></div>
            <div><div className="flex justify-between text-sm mb-1"><span>{t('dashboard.intentMid')}</span><span>{t('dashboard.personCount', { count: intent.mid })}</span></div><Progress color="#ea580c" percentage={intent.midPct} /></div>
            <div><div className="flex justify-between text-sm mb-1"><span>{t('dashboard.intentLow')}</span><span>{t('dashboard.personCount', { count: intent.low })}</span></div><Progress percentage={intent.lowPct} /></div>
          </div>
        </div>
        <div className="bg-white rounded-xl border border-gray-200 p-5">
          <h3 className="text-base font-bold text-gray-800 mb-4">{t('dashboard.modelHealth')}</h3>
          <Table
            rowKey="model_name"
            loading={loading}
            data={models as TableRowData[]}
            size="small"
            empty={t('dashboard.noModels')}
            columns={[
              { colKey: 'display_name', title: t('dashboard.colModel'), width: 220, ellipsis: true },
              { colKey: 'provider', title: t('dashboard.colProvider'), width: 110 },
              { colKey: 'available', title: t('common.status'), width: 100, cell: (p: CellProps) => <Tag theme={p.row.available ? 'success' : 'danger'}>{p.row.available ? t('dashboard.available') : t('dashboard.cooldown')}</Tag> },
              { colKey: 'consecutive_fails', title: t('dashboard.colFails'), width: 80 },
              { colKey: 'cooldown_left_sec', title: t('dashboard.colCooldown'), width: 90, cell: (p: CellProps) => formatCooldown(Number(p.row.cooldown_left_sec || 0)) },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 p-5">
        <h3 className="text-base font-bold text-gray-800 mb-4">{t('dashboard.quickEntries')}</h3>
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
        <h3 className="text-base font-bold text-gray-800 mb-4">{t('dashboard.recentLeads')}</h3>
        <div className="space-y-2">
          {recent.length === 0 && <p className="text-sm text-gray-400 py-4 text-center">{t('dashboard.noCustomers')}</p>}
          {recent.map((l: CrudRow) => (
            <div key={l.id} className="flex items-center justify-between bg-gray-50 rounded-lg px-4 py-3">
              <div className="flex items-center gap-3">
                <div className="w-8 h-8 bg-white rounded-full border border-gray-200 flex items-center justify-center text-xs font-medium text-gray-500">{((l.name || t('dashboard.guestChar'))[0] || t('dashboard.guestChar'))}</div>
                <div>
                  <span className="text-sm font-medium text-gray-800">{!isVisitorName(l.name) ? l.name : t('common.customer')}</span>
                  <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-gray-100 text-gray-500">{STAGE_KEYS[l.journey_stage] ? t(STAGE_KEYS[l.journey_stage]) : l.journey_stage || '-'}</span>
                  <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-indigo-50 text-indigo-600">{t('dashboard.intentScore', { score: Number(l.intent_score || 0).toFixed(2) })}</span>
                </div>
              </div>
              <span className="text-xs text-gray-400 truncate max-w-[45%]">{l.last_message || t('common.noMessages')}</span>
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
