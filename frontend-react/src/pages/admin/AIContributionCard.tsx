// AI 贡献度看板（D2，PLAN_FIX_2026-09-21 商业化第一缺口）。
//
// 为什么值得单列一张卡片：客户续费时最常问的一句是"AI 到底帮我做了多少生意"。
// 系统原本只能证明"AI 在回复"（模型健康、消息量），证明不了"AI 带来的留资/到店"。
// 这张卡片把后端 /api/v1/stats/ai-contribution 的口径原样呈现，并附上后端口径说明，
// 避免前端自行"美化"成客户想听的数字。
//
// 数据/口径全部来自后端（含 notes），前端只做展示与窗口切换，不重算任何比率——
// 比率一旦在前端复算，就会与后端 notes 的声明脱节，这是"两个口径"类缺陷的常见起点。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'

/** 后端 schema.AIContribution 的展示侧镜像（仅取用到的字段，全部可选以防旧版后端）。 */
export type Contribution = {
  period_days?: number
  since?: string
  until?: string
  new_conversations?: number
  active_conversations?: number
  ai_served_customers?: number
  human_served_customers?: number
  ai_serve_share?: number
  handoff_rate?: number
  ai_leads?: number
  assisted_leads?: number
  ai_lead_rate?: number
  assisted_lead_rate?: number
  ai_arrived?: number
  ai_ordered?: number
  ai_arrive_rate?: number
  ai_order_rate?: number
  ai_messages?: number
  human_messages?: number
  customer_messages?: number
  ai_message_share?: number
  pending_handoff_now?: number
  notes?: string[]
}

/** 可选统计窗口（天）。与后端 (0,365] 约束一致。 */
const WINDOW_OPTIONS = [7, 30, 90] as const

/** 小样本阈值：与服务端口径说明一致（<30 只作趋势参考）。 */
const SMALL_SAMPLE = 30

/** 百分比展示：后端传 0-1 的小数。 */
export function pct(v?: number) {
  return `${(Number(v ?? 0) * 100).toFixed(1)}%`
}

/** 整数展示：缺值显示 '-' 而不是 0，避免"没数据"被读成"业务为零"。 */
function num(v?: number) {
  return v === undefined || v === null ? '-' : String(v)
}

type Props = {
  /** 默认统计窗口天数，默认 30。 */
  defaultDays?: number
}

/** AI 贡献度卡片：接待量 / 结果归因 / 交互结构 + 口径说明。 */
export function AIContributionCard({ defaultDays = 30 }: Props) {
  const [days, setDays] = useState<number>(defaultDays)
  const [data, setData] = useState<Contribution | null>(null)
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)

  const load = useCallback(async (d: number) => {
    setLoading(true)
    setFailed(false)
    const r = await AUTH(`/api/v1/stats/ai-contribution?days=${d}`)
    if (r?.code === 0 && r.data) {
      setData(r.data as Contribution)
    } else {
      // 失败态必须显式可见：静默留空会让"端点挂了"看起来像"业务为零"
      setData(null)
      setFailed(true)
    }
    setLoading(false)
  }, [])

  useEffect(() => { void load(days) }, [load, days])

  const served = (data?.ai_served_customers ?? 0) + (data?.human_served_customers ?? 0)
  const smallSample = served > 0 && served < SMALL_SAMPLE

  const msgs = useMemo(() => ([
    { label: 'AI 消息', value: data?.ai_messages },
    { label: '人工消息', value: data?.human_messages },
    { label: '客户消息', value: data?.customer_messages },
  ]), [data])

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-bold text-gray-800 mb-1">AI 贡献度</h3>
          <p className="text-xs text-gray-400">AI 独立接待与人工参与的对比，以及留资/到店/成交归因。</p>
        </div>
        <div className="flex items-center gap-2">
          {loading && <span className="text-xs text-gray-400">加载中…</span>}
          {WINDOW_OPTIONS.map((d) => (
            <Tag
              key={d}
              theme={d === days ? 'primary' : 'default'}
              variant={d === days ? 'dark' : 'light'}
              style={{ cursor: 'pointer' }}
              onClick={() => setDays(d)}
            >
              近 {d} 天
            </Tag>
          ))}
        </div>
      </div>

      {failed && (
        <p className="text-sm text-red-500 py-6 text-center">AI 贡献度数据加载失败，请点上方窗口重试。</p>
      )}

      {!failed && (
        <>
          {/* 接待量：AI 独立 vs 人工参与 */}
          <div className="mt-4 grid grid-cols-2 md:grid-cols-4 gap-3">
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-indigo-600">{num(data?.ai_served_customers)}</b>
              <span className="text-xs text-gray-500">AI 独立接待客户</span>
            </div>
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{num(data?.human_served_customers)}</b>
              <span className="text-xs text-gray-500">人工参与客户</span>
            </div>
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{pct(data?.ai_serve_share)}</b>
              <span className="text-xs text-gray-500">AI 独立接待占比</span>
            </div>
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{pct(data?.handoff_rate)}</b>
              <span className="text-xs text-gray-500">人机切换率</span>
            </div>
          </div>

          {/* 结果归因：留资对照组 + 到店/成交（仅 AI 侧） */}
          <div className="mt-4 grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_leads)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 留资</p>
            </div>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{pct(data?.ai_lead_rate)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 留资转化率</p>
            </div>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.assisted_leads)}</b>
              <p className="text-xs text-gray-500 mt-1">人工留资（对照）</p>
            </div>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{pct(data?.assisted_lead_rate)}</b>
              <p className="text-xs text-gray-500 mt-1">人工留资转化率</p>
            </div>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_arrived)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 到店 {pct(data?.ai_arrive_rate)}</p>
            </div>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_ordered)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 成交 {pct(data?.ai_order_rate)}</p>
            </div>
          </div>

          {/* 交互结构 + 口径边界 */}
          <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-gray-500">
            <span>活跃会话 <b className="text-gray-800">{num(data?.active_conversations)}</b></span>
            <span>新建会话 <b className="text-gray-800">{num(data?.new_conversations)}</b></span>
            {msgs.map((m) => (
              <span key={m.label}>{m.label} <b className="text-gray-800">{num(m.value)}</b></span>
            ))}
            <span>AI 消息占比 <b className="text-gray-800">{pct(data?.ai_message_share)}</b></span>
            <span>待接管 <b className="text-gray-800">{num(data?.pending_handoff_now)}</b></span>
          </div>

          {smallSample && (
            <p className="mt-3 text-xs text-orange-600">
              接待样本仅 {served} 人（&lt;{SMALL_SAMPLE}），转化率只作趋势参考，不建议直接用于投放或考核结论。
            </p>
          )}

          {/* 口径说明：原样透出后端 notes，前端不做措辞改写 */}
          {!!data?.notes?.length && (
            <details className="mt-4">
              <summary className="text-xs text-gray-500 cursor-pointer">口径说明（{data.notes.length} 条）</summary>
              <ul className="mt-2 space-y-1 pl-5 list-disc">
                {data.notes.map((n, i) => (
                  <li key={i} className="text-xs text-gray-400 leading-relaxed">{n}</li>
                ))}
              </ul>
            </details>
          )}
        </>
      )}
    </div>
  )
}
