// 额度与账单提醒卡片（D3，2026-09-23）。
//
// 为什么放工作台而不是塞进「用量」页：这张卡片说的不是"我用了多少"，而是"接下来会发生什么"——
// 额度到档会被降级、到期不续会被停登录。这两件事都需要管理员当天就看到，
// 而「用量」页是事后查数才去的地方。
//
// 三条展示纪律：
//  1. **数字一律用后端算好的**（progress[].pct / day_past / grace_end）。前端一旦自己除，
//     就会出现"卡片说 79%、邮件说已越 80% 档"这种没人能解释的对不上。
//  2. **没有分母就不画进度条**。unlimited=true 的两种形态（不限额、预充值余额）画成 0%
//     等于告诉客户"你还很宽裕"，是反的。
//  3. **提醒口径原样透出**（阈值档、催缴第几天、宽限期到哪天）。这些是平台会对客户执行的动作，
//     藏着不说才是纠纷起点；同时前端绝不按本地时区推算"还剩几天"，一律用后端 day_past。
import { useCallback, useEffect, useState } from 'react'
import { Button, Progress, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { AdminDunningResp, AdminUsageAlertsResp, UsageProgressRow } from '../../types'

/** 留痕列表最多显示几条（更多去「用量」页看，卡片只负责"最近发生过什么"） */
const MAX_ALERT_ROWS = 5

/** 千分位展示；undefined/null 显示 '-'，不能让"没数据"读成"0" */
function fmtNum(v?: number | null): string {
  if (v === undefined || v === null) return '-'
  return v.toLocaleString('zh-CN')
}

/** 日期展示（后端给 RFC3339，卡片只需要到日）；空值返回空串由调用方决定文案 */
function fmtDay(s?: string | null): string {
  if (!s) return ''
  const d = new Date(s)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString('zh-CN')
}

/** 进度条配色：耗尽红、临界橙、其余正常。阈值来源是后端 thresholds，这里只按 pct 分档。 */
function barColor(pct: number, thresholds: number[]): string {
  const top = thresholds.length ? thresholds[thresholds.length - 1] : 100
  if (pct >= 100) return '#dc2626'
  if (pct >= Math.min(top, 95)) return '#ea580c'
  return '#4f46e5'
}

/** 单项指标一行：有分母画条，没分母说绝对量 + 预警线 */
function ProgressLine({ row, thresholds }: { row: UsageProgressRow; thresholds: number[] }) {
  return (
    <div className="border border-gray-100 rounded-lg p-3">
      <div className="flex items-center justify-between gap-2 text-sm">
        <span className="text-gray-600">{row.label}</span>
        <span className="text-gray-800 font-medium">
          {row.unlimited
            ? `剩 ${fmtNum(row.remaining)}`
            : `${fmtNum(row.used)} / ${fmtNum(row.max)}（${row.pct}%）`}
        </span>
      </div>
      {row.unlimited
        ? <p className="text-xs text-gray-400 mt-1">
            {row.warn_below > 0 ? `余额低于 ${fmtNum(row.warn_below)} 时通知管理员` : '余额桶不设限'}
          </p>
        : <div className="mt-2"><Progress color={barColor(row.pct, thresholds)} percentage={Math.min(row.pct, 100)} /></div>}
    </div>
  )
}

/** 额度与账单提醒卡片：三指标进度 + 越档留痕 + 催缴进度。 */
export function QuotaAlertCard() {
  const [usage, setUsage] = useState<AdminUsageAlertsResp | null>(null)
  const [bill, setBill] = useState<AdminDunningResp | null>(null)
  const [loading, setLoading] = useState(false)
  // 两个端点各自记失败：催缴读不到不该把额度进度也一起清空（反之同理）
  const [usageFailed, setUsageFailed] = useState(false)
  const [billFailed, setBillFailed] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    const [u, b] = await Promise.all([
      AUTH('/api/v1/admin/usage/alerts?limit=10'),
      AUTH('/api/v1/admin/billing/dunning'),
    ])
    setUsageFailed(!(u?.code === 0 && u.data))
    setBillFailed(!(b?.code === 0 && b.data))
    setUsage(u?.code === 0 ? (u.data as AdminUsageAlertsResp) : null)
    setBill(b?.code === 0 ? (b.data as AdminDunningResp) : null)
    setLoading(false)
  }, [])

  useEffect(() => { void load() }, [load])

  const dunning = bill?.dunning
  const urgent = !!dunning?.exists && (dunning.suspended || dunning.status === 'running')

  return (
    <div className="bg-white rounded-xl border border-gray-200 p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="text-base font-bold text-gray-800 mb-1">额度与账单提醒</h3>
          <p className="text-xs text-gray-400">
            AI 额度用到哪一档、欠费催缴走到哪一步。数字与节奏都由平台侧统一判定，此卡片只如实转述。
          </p>
        </div>
        <div className="flex items-center gap-2">
          {loading && <span className="text-xs text-gray-400">加载中…</span>}
          <Button theme="default" variant="outline" size="small" loading={loading} disabled={loading} onClick={() => void load()}>刷新</Button>
        </div>
      </div>

      {/* 催缴横幅：这家是不是正在被催，是管理员第一眼要知道的事 */}
      {billFailed && <p className="mt-3 text-sm text-red-500">账单状态读取失败，请点右上「刷新」重试。</p>}
      {!billFailed && urgent && (
        <div className="mt-3 rounded-lg p-3 text-sm" style={{ background: dunning?.suspended ? '#fef2f2' : '#fff7ed', color: dunning?.suspended ? '#b91c1c' : '#9a3412' }}>
          {dunning?.suspended
            ? <>已因欠费停止登录，续费到账后自动恢复。催缴第 <b>{dunning.stage}</b>/{dunning.total_stages} 档。</>
            : <>套餐已到期第 <b>{dunning?.day_past}</b> 天，正在按档催缴（第 <b>{dunning?.stage}</b>/{dunning?.total_stages} 档）。
                {fmtDay(dunning?.grace_end) && <> 宽限期至 {fmtDay(dunning?.grace_end)}。</>}
                {' '}请前往收银台续费：<a href="/billing" className="underline">立即续费</a>。</>}
          {!!bill?.config?.enabled && bill.config.suspend_after_days > 0 && (
            <span className="block text-xs mt-1 opacity-70">
              平台口径：到期后第 {(bill.config.steps || []).join('/')} 天各提醒一次，第 {bill.config.suspend_after_days} 天停止登录（数据完整保留）。
            </span>
          )}
        </div>
      )}

      {usageFailed && <p className="mt-3 text-sm text-red-500">额度数据读取失败，请点右上「刷新」重试。</p>}
      {!usageFailed && (
        <>
          <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
            {(usage?.progress ?? []).map((r) => (
              <ProgressLine key={r.metric} row={r} thresholds={usage?.config?.thresholds ?? []} />
            ))}
            {!usage?.progress?.length && <p className="text-sm text-gray-400">暂无额度数据。</p>}
          </div>

          {/* 通道就绪提示：预警发不出去时，卡片必须说"不会送达"，否则管理员以为有人在盯
              —— 三段都以 config 实存为前提：口径由后端算好，缺段就整段不渲染（宁可少说，不可崩台） */}
          {!!usage?.config && !usage.config.enabled && (
            <p className="mt-3 text-xs text-gray-400">额度越档提醒当前未启用，只在本卡片展示进度。</p>
          )}
          {!!usage?.config && usage.config.enabled && !usage.config.email_ready && (
            <p className="mt-3 text-xs text-orange-600">邮件通道未配置，越档提醒不会送达管理员邮箱。</p>
          )}
          {!!usage?.config && usage.config.enabled && usage.config.email_ready && (
            <p className="mt-3 text-xs text-gray-500">
              提醒口径：用量达 {(usage.config.thresholds || []).join('%/')}% 时通知租户管理员邮箱
              {usage.config.group_ready ? '，额度耗尽同步平台群' : ''}。当前账期 {usage.period_key}。
            </p>
          )}

          <div className="mt-4">
            <h4 className="text-sm font-medium text-gray-700 mb-2">历史提醒（最近 {MAX_ALERT_ROWS} 条）</h4>
            {(usage?.list ?? []).length === 0 && <p className="text-sm text-gray-400">还没有发过额度提醒。</p>}
            <div className="space-y-2">
              {(usage?.list ?? []).slice(0, MAX_ALERT_ROWS).map((a, i) => (
                <div key={`${a.metric}-${a.threshold}-${a.period_key}-${i}`}
                  className="flex flex-wrap items-center justify-between gap-2 bg-gray-50 rounded-lg px-3 py-2 text-xs">
                  <span className="text-gray-600">
                    {a.period_key} · {a.metric === 'token_balance' ? '预充值余额' : a.metric === 'monthly_tokens' ? '月度 token 额度' : '月度调用次数'}
                    <b className="ml-1 text-gray-800">{a.metric === 'token_balance' ? `剩 ${fmtNum(a.remaining)}` : `${a.threshold}% 档`}</b>
                  </span>
                  <span className="flex items-center gap-2">
                    <Tag size="small" theme={a.channels ? 'success' : 'danger'} variant="light">
                      {a.channels ? `已送达 ${a.channels}` : '无可用收件人'}
                    </Tag>
                    <span className="text-gray-400">{fmtDay(a.created_at)}</span>
                  </span>
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}
