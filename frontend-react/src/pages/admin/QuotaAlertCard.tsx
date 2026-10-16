// 额度与账单提醒卡片（D3，2026-09-23）。
//
// 为什么放工作台而不是塞进「用量」页：这张卡片说的不是"我用了多少"，而是"接下来会发生什么"——
// 额度到档会被降级、到期不续会被停登录。这两件事都需要管理员当天就看到，
// 而「用量」页是事后查数才去的地方。
//
// 三条展示纪律：
//  1. 数字一律用后端算好的（progress[].pct / day_past / grace_end）。前端一旦自己除，
//     就会出现"卡片说 79%、邮件说已越 80% 档"这种没人能解释的对不上。
//  2. 没有分母就不画进度条。unlimited=true 的两种形态（不限额、预充值余额）画成 0%
//     等于告诉客户"你还很宽裕"，是反的。
//  3. 提醒口径原样透出（阈值档、催缴第几天、宽限期到哪天）。这些是平台会对客户执行的动作，
//     藏着不说才是纠纷起点；同时前端绝不按本地时区推算"还剩几天"，一律用后端 day_past。
import { useCallback, useEffect, useState } from 'react'
import { Button, Progress, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
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

/** 指标→文案键：后端 metric 码映射到 i18n 键（数据契约，未知码回退原样）。 */
const METRIC_LABEL: Record<string, I18nKey> = {
  token_balance: 'admin.quota.metricBalance',
  monthly_tokens: 'admin.quota.metricMonthlyTokens',
  monthly_calls: 'admin.quota.metricMonthlyCalls',
}

/** 单项指标一行：有分母画条，没分母说绝对量 + 预警线 */
function ProgressLine({ row, thresholds }: { row: UsageProgressRow; thresholds: number[] }) {
  const { t } = useI18n()
  return (
    <div className="border border-gray-100 rounded-lg p-3">
      <div className="flex items-center justify-between gap-2 text-sm">
        <span className="text-gray-600">{row.label}</span>
        <span className="text-gray-800 font-medium">
          {row.unlimited
            ? t('admin.quota.remaining', { n: fmtNum(row.remaining) })
            : `${fmtNum(row.used)} / ${fmtNum(row.max)}（${row.pct}%）`}
        </span>
      </div>
      {row.unlimited
        ? <p className="text-xs text-gray-400 mt-1">
            {row.warn_below > 0 ? t('admin.quota.warnBelow', { n: fmtNum(row.warn_below) }) : t('admin.quota.unlimitedNote')}
          </p>
        : <div className="mt-2"><Progress color={barColor(row.pct, thresholds)} percentage={Math.min(row.pct, 100)} /></div>}
    </div>
  )
}

/** 额度与账单提醒卡片：三指标进度 + 越档留痕 + 催缴进度。 */
export function QuotaAlertCard() {
  const { t } = useI18n()
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
          <h3 className="text-base font-bold text-gray-800 mb-1">{t('admin.quota.title')}</h3>
          <p className="text-xs text-gray-400">{t('admin.quota.desc')}</p>
        </div>
        <div className="flex items-center gap-2">
          {loading && <span className="text-xs text-gray-400">{t('common.loading')}</span>}
          <Button theme="default" variant="outline" size="small" loading={loading} disabled={loading} onClick={() => void load()}>{t('admin.quota.refresh')}</Button>
        </div>
      </div>

      {/* 催缴横幅：这家是不是正在被催，是管理员第一眼要知道的事 */}
      {billFailed && <p className="mt-3 text-sm text-red-500">{t('admin.quota.billFailed')}</p>}
      {!billFailed && urgent && (
        <div className="mt-3 rounded-lg p-3 text-sm" style={{ background: dunning?.suspended ? '#fef2f2' : '#fff7ed', color: dunning?.suspended ? '#b91c1c' : '#9a3412' }}>
          {dunning?.suspended
            ? t('admin.quota.suspendedMsg', { stage: dunning.stage, total: dunning.total_stages })
            : <>{t('admin.quota.expireMsg', { day: dunning?.day_past, stage: dunning?.stage, total: dunning?.total_stages })}
                {fmtDay(dunning?.grace_end) && <>{t('admin.quota.graceMsg', { grace: fmtDay(dunning?.grace_end) })}</>}
                {' '}{t('admin.quota.gotoPay')}<a href="/billing" className="underline">{t('admin.quota.payNow')}</a>。</>}
          {!!bill?.config?.enabled && bill.config.suspend_after_days > 0 && (
            <span className="block text-xs mt-1 opacity-70">
              {t('admin.quota.platformNote', { steps: (bill.config.steps || []).join('/'), suspend: bill.config.suspend_after_days })}
            </span>
          )}
        </div>
      )}

      {usageFailed && <p className="mt-3 text-sm text-red-500">{t('admin.quota.usageFailed')}</p>}
      {!usageFailed && (
        <>
          <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
            {(usage?.progress ?? []).map((r) => (
              <ProgressLine key={r.metric} row={r} thresholds={usage?.config?.thresholds ?? []} />
            ))}
            {!usage?.progress?.length && <p className="text-sm text-gray-400">{t('admin.quota.noUsage')}</p>}
          </div>

          {/* 通道就绪提示：预警发不出去时，卡片必须说"不会送达"，否则管理员以为有人在盯
              —— 三段都以 config 实存为前提：口径由后端算好，缺段就整段不渲染（宁可少说，不可崩台） */}
          {!!usage?.config && !usage.config.enabled && (
            <p className="mt-3 text-xs text-gray-400">{t('admin.quota.alertOff')}</p>
          )}
          {!!usage?.config && usage.config.enabled && !usage.config.email_ready && (
            <p className="mt-3 text-xs text-orange-600">{t('admin.quota.emailNotReady')}</p>
          )}
          {!!usage?.config && usage.config.enabled && usage.config.email_ready && (
            <p className="mt-3 text-xs text-gray-500">
              {t('admin.quota.alertOn', { thresholds: (usage.config.thresholds || []).join('%/') })}
              {usage.config.group_ready ? t('admin.quota.groupSync') : ''}
              {t('admin.quota.period', { period: usage.period_key })}
            </p>
          )}

          <div className="mt-4">
            <h4 className="text-sm font-medium text-gray-700 mb-2">{t('admin.quota.historyTitle', { n: MAX_ALERT_ROWS })}</h4>
            {(usage?.list ?? []).length === 0 && <p className="text-sm text-gray-400">{t('admin.quota.noAlerts')}</p>}
            <div className="space-y-2">
              {(usage?.list ?? []).slice(0, MAX_ALERT_ROWS).map((a, i) => (
                <div key={`${a.metric}-${a.threshold}-${a.period_key}-${i}`}
                  className="flex flex-wrap items-center justify-between gap-2 bg-gray-50 rounded-lg px-3 py-2 text-xs">
                  <span className="text-gray-600">
                    {a.period_key} · {t((METRIC_LABEL[a.metric] ?? a.metric) as I18nKey)}
                    <b className="ml-1 text-gray-800">{a.metric === 'token_balance' ? t('admin.quota.remaining', { n: fmtNum(a.remaining) }) : t('admin.quota.tierPct', { n: a.threshold })}</b>
                  </span>
                  <span className="flex items-center gap-2">
                    <Tag size="small" theme={a.channels ? 'success' : 'danger'} variant="light">
                      {a.channels ? t('admin.quota.delivered', { channels: a.channels }) : t('admin.quota.noRecipient')}
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
