// AI 贡献度看板（D2，PLAN_FIX_2026-09-21 商业化第一缺口）。
//
// 为什么值得单列一张卡片：客户续费时最常问的一句是"AI 到底帮我做了多少生意"。
// 系统原本只能证明"AI 在回复"（模型健康、消息量），证明不了"AI 带来的留资/到店"。
// 这张卡片把后端 /api/v1/stats/ai-contribution 的口径原样呈现，并附上后端口径说明，
// 避免前端自行"美化"成客户想听的数字。
//
// 数据/口径全部来自后端（含 notes），前端只做展示与窗口切换，不重算任何比率——
// 比率一旦在前端复算，就会与后端 notes 的声明脱节，这是"两个口径"类缺陷的常见起点。
//
// D4(2026-09-23)：六个**客户级**数字可点击，交给调用方切到客户名单并带上当前窗口。
// 只这六个可点：占比/切换率/转化率是比率，活跃会话/消息量/待接管的单位不是"客户"，
// 给它们加点击就会出现"卡片写 8、名单 20 行"——那比不能点更糟。
// 后端 /stats/ai-contribution/customers 与本卡片共用同一份归属谓词（analytics.MatchContributionMetric），
// 名单条数与卡片数字同源；响应的 metrics 字段才是可下钻指标的权威清单。
import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { AIContribution, ContributionDrill } from '../../types'

/** 后端 schema.AIContribution 的展示侧镜像：批五 E 契约批起直接复用 types.ts 的共享口径，
 *  不再在本文件维护第二份字段表（两份字段表正是"两个口径"的起点）。 */
export type Contribution = AIContribution

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
  /** 点击客户级数字时的下钻回调（带指标码与当前窗口）；不传即整卡只读。 */
  onDrill?: (drill: ContributionDrill) => void
}

/** 可下钻的指标格子：值为真实数字且调用方挂了 onDrill 才可点。
 *  值为 '-'（后端没给数）时不可点——点开一份空名单会被读成"端点坏了"。
 *  键盘可达（Enter/空格），与原生按钮同语义。 */
function Tile({ metric, value, days, onDrill, className, children }: {
  metric: string
  value?: number
  days: number
  onDrill?: (drill: ContributionDrill) => void
  className: string
  children: ReactNode
}) {
  const clickable = !!onDrill && value !== undefined && value !== null
  const go = () => onDrill?.({ metric, days })
  return (
    <div
      className={`${className}${clickable ? ' cursor-pointer hover:ring-2 hover:ring-indigo-300' : ''}`}
      role={clickable ? 'button' : undefined}
      tabIndex={clickable ? 0 : undefined}
      title={clickable ? '点击查看对应的客户名单' : undefined}
      onClick={clickable ? go : undefined}
      onKeyDown={clickable ? (e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); go() } } : undefined}
    >
      {children}
    </div>
  )
}

/** AI 贡献度卡片：接待量 / 结果归因 / 交互结构 + 口径说明。 */
export function AIContributionCard({ defaultDays = 30, onDrill }: Props) {
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
          {/* 接待量：AI 独立 vs 人工参与（前两个是客户数，可下钻；后两个是比率，不可点） */}
          <div className="mt-4 grid grid-cols-2 md:grid-cols-4 gap-3">
            <Tile metric="ai_served" value={data?.ai_served_customers} days={days} onDrill={onDrill} className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-indigo-600">{num(data?.ai_served_customers)}</b>
              <span className="text-xs text-gray-500">AI 独立接待客户</span>
            </Tile>
            <Tile metric="human_served" value={data?.human_served_customers} days={days} onDrill={onDrill} className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{num(data?.human_served_customers)}</b>
              <span className="text-xs text-gray-500">人工参与客户</span>
            </Tile>
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{pct(data?.ai_serve_share)}</b>
              <span className="text-xs text-gray-500">AI 独立接待占比</span>
            </div>
            <div className="bg-gray-50 rounded-lg p-4">
              <b className="block text-2xl text-gray-800">{pct(data?.handoff_rate)}</b>
              <span className="text-xs text-gray-500">人机切换率</span>
            </div>
          </div>

          {/* 结果归因：留资对照组 + 到店/成交（仅 AI 侧）——四个客户数可下钻，两个转化率不可 */}
          <div className="mt-4 grid grid-cols-2 md:grid-cols-3 lg:grid-cols-6 gap-3">
            <Tile metric="ai_lead" value={data?.ai_leads} days={days} onDrill={onDrill} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_leads)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 留资</p>
            </Tile>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{pct(data?.ai_lead_rate)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 留资转化率</p>
            </div>
            <Tile metric="assisted_lead" value={data?.assisted_leads} days={days} onDrill={onDrill} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.assisted_leads)}</b>
              <p className="text-xs text-gray-500 mt-1">人工留资（对照）</p>
            </Tile>
            <div className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{pct(data?.assisted_lead_rate)}</b>
              <p className="text-xs text-gray-500 mt-1">人工留资转化率</p>
            </div>
            <Tile metric="ai_arrived" value={data?.ai_arrived} days={days} onDrill={onDrill} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_arrived)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 到店 {pct(data?.ai_arrive_rate)}</p>
            </Tile>
            <Tile metric="ai_ordered" value={data?.ai_ordered} days={days} onDrill={onDrill} className="border border-gray-100 rounded-lg p-3 text-center">
              <b className="block text-xl text-gray-800">{num(data?.ai_ordered)}</b>
              <p className="text-xs text-gray-500 mt-1">AI 成交 {pct(data?.ai_order_rate)}</p>
            </Tile>
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

          {!!onDrill && (
            <p className="mt-2 text-xs text-gray-400">
              点上方任一「客户数」可核对背后的客户名单（比例与会话/消息量不是客户数，故不可点）。
            </p>
          )}

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
