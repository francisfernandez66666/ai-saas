// 催缴受理 Tab（D3，2026-09-23）：欠费不再只靠销售人肉盯群。
//
// 依赖接口（仅 super_admin，平台路径无需 X-Tenant-ID）：
//   GET  /api/v1/super/dunning?only_open=&limit=      未结催缴队列 + 两份平台口径
//   POST /api/v1/super/dunning/:id/nudge              人工"立刻再催一次"（不推进档位）
//   POST /api/v1/super/dunning/:id/reset              关闭催缴序列（线下已收款/谈定缓收）
//
// 刻意不提供"解封"按钮：解封是有资金含义的动作，只认可到账那一条路（billing.DunningOnPaid）。
// reset 也只停催缴——否则后台一个按钮就能把欠费户放回货架，与资金红线同构。
// 两个动作都在领域层落审计行（带操作人与 IP），本文件不重复记。
import { useCallback, useEffect, useState } from 'react'
import { Button, Switch, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import { confirmDialog } from '../../lib/confirm'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, DunningRow, SuperDunningQueueResp, TableRowData } from '../../types'

// ApiResp 本文件接口调用的标准信封：code/message 为状态位，data 为业务负载。
type ApiResp<T> = { code: number; message?: string; data?: T }

// 序列状态 → 标签配色（running=在催、exhausted=已走完序列（通常已停用）、resolved=已结）。
// 模块级拿不到 t()，只存「后端值 → I18nKey」，渲染时再 t()。
const STATUS_KEYS: Record<string, { label: I18nKey; theme: 'primary' | 'success' | 'warning' | 'danger' | 'default' }> = {
  running: { label: 'super.dunning.stRunning', theme: 'warning' },
  exhausted: { label: 'super.dunning.stExhausted', theme: 'danger' },
  resolved: { label: 'super.dunning.stResolved', theme: 'success' },
}

/** 格式化时间列（只需到分钟：催缴节奏是天级，秒是噪音） */
function fmtTime(v?: string | null): string {
  return v ? new Date(v).toLocaleString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' }) : '-'
}

/** 催缴队列：档位进度、下次通知时间、人工重发与清序列。 */
export function DunningTab() {
  const { t } = useI18n()
  const [data, setData] = useState<SuperDunningQueueResp | null>(null)
  const [rows, setRows] = useState<DunningRow[]>([])
  const [onlyOpen, setOnlyOpen] = useState(true)
  const [loading, setLoading] = useState(false)
  // 正在操作的租户 id：两个按钮都是"对客户开口/停止开口"的动作，连点没有意义
  const [busyId, setBusyId] = useState<number | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    const j = (await AUTH(`/api/v1/super/dunning?only_open=${onlyOpen ? 'true' : 'false'}&limit=100`)) as ApiResp<SuperDunningQueueResp>
    if (j?.code === 0 && j.data) {
      setData(j.data)
      setRows(j.data.list || [])
    }
    setLoading(false)
  }, [onlyOpen])
  useEffect(() => { void load() }, [load])

  // 人工重发：按当前档位再发一封，档位不变（节奏归状态机）
  async function doNudge(r: DunningRow) {
    const ok = await confirmDialog(
      t('super.dunning.nudgeBody', { name: r.tenant_name || r.code, day: r.day_past, stage: r.stage }),
      t('super.dunning.nudgeTitle'))
    if (!ok) return
    setBusyId(r.tenant_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/dunning/${r.tenant_id}/nudge`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.dunning.nudged')); void load() }
    else MessagePlugin.error(j?.message || t('super.dunning.nudgeFail'))
  }

  // 清序列：线下已付款/谈定缓收时用；只停止催缴，绝不解封
  async function doReset(r: DunningRow) {
    const ok = await confirmDialog(
      t('super.dunning.resetBody', { name: r.tenant_name || r.code }),
      t('super.dunning.resetTitle'))
    if (!ok) return
    setBusyId(r.tenant_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/dunning/${r.tenant_id}/reset`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || t('super.dunning.resetOk')); void load() }
    else MessagePlugin.error(j?.message || t('super.dunning.resetFail'))
  }

  const cols = [
    { colKey: 'tenant_name', title: t('common.tenant'), width: 180, cell: (p: CellProps) => p.row.tenant_name || `#${p.row.tenant_id}` },
    { colKey: 'code', title: t('common.code'), width: 130 },
    { colKey: 'tenant_status', title: t('super.dunning.colTenantStatus'), width: 100, cell: (p: CellProps) => (
      <Tag theme={p.row.tenant_status === 'suspended' ? 'danger' : p.row.tenant_status === 'expired' ? 'warning' : 'default'} variant="light">
        {p.row.tenant_status}
      </Tag>) },
    { colKey: 'status', title: t('super.dunning.colSeq'), width: 100, cell: (p: CellProps) => {
      const m = STATUS_KEYS[p.row.status] || { label: p.row.status as I18nKey, theme: 'default' as const }
      return <Tag theme={m.theme}>{t(m.label)}</Tag>
    } },
    { colKey: 'stage', title: t('super.dunning.colStage'), width: 80, cell: (p: CellProps) => `${p.row.stage}` },
    { colKey: 'day_past', title: t('super.dunning.colOverdueDays'), width: 90 },
    { colKey: 'due_at', title: t('super.dunning.colDue'), width: 130, cell: (p: CellProps) => fmtTime(p.row.due_at) },
    { colKey: 'grace_end', title: t('super.dunning.colGrace'), width: 130, cell: (p: CellProps) => fmtTime(p.row.grace_end) },
    { colKey: 'next_notify_at', title: t('super.dunning.colNext'), width: 130, cell: (p: CellProps) => fmtTime(p.row.next_notify_at) },
    { colKey: 'last_notified_at', title: t('super.dunning.colLast'), width: 130, cell: (p: CellProps) => fmtTime(p.row.last_notified_at) },
    { colKey: 'sent_to', title: t('super.dunning.colRecipient'), width: 170, cell: (p: CellProps) => p.row.sent_to || '-' },
    { colKey: 'op', title: t('super.dunning.colOp'), width: 190, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" theme="primary" variant="outline" loading={busyId === p.row.tenant_id}
          disabled={busyId !== null} onClick={() => void doNudge(p.row as DunningRow)}>{t('super.dunning.nudgeTitle')}</Button>
        <Button size="small" variant="outline" disabled={busyId !== null}
          onClick={() => void doReset(p.row as DunningRow)}>{t('super.dunning.resetBtn')}</Button>
      </div>) },
  ]

  const cfg = data?.config
  const usageCfg = data?.usage_alert_config

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>{t('super.dunning.title')}</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ fontSize: 12, color: '#718096' }}>{t('super.dunning.onlyOpen')}</span>
          <Switch size="small" value={onlyOpen} onChange={(v: boolean) => setOnlyOpen(v)} />
          <Button theme="primary" variant="outline" onClick={() => void load()} loading={loading}>{t('super.dunning.refresh')}</Button>
        </div>
      </div>

      {/* 开关未开时队列必然为空：这句话必须说清，否则运营会读成"没有一家欠费" */}
      {!cfg?.enabled && (
        <p style={{ fontSize: 13, color: '#9a3412', background: '#fff7ed', borderRadius: 8, padding: '8px 12px' }}>
          {t('super.dunning.disabledHint')}
        </p>
      )}
      {!!cfg?.enabled && (
        <p style={{ fontSize: 12, color: '#718096', margin: '0 0 12px' }}>
          {t('super.dunning.notePrefix', { steps: (cfg.steps || []).join('/') })}
          {cfg.suspend_after_days > 0 ? t('super.dunning.suspendAt', { days: cfg.suspend_after_days }) : t('super.dunning.onlyNudge')}
          {/* 显式空格：原文案此处是 `{' '}` 文本节点。相邻 JSX 表达式之间不产生空格，
              去掉它会把「只催不封。额度预警：」连成一句（DunningTab.test.tsx 逐字钉了这个空格） */}
          {' '}{t('super.dunning.usagePrefix')}
          {usageCfg?.enabled ? t('super.dunning.usageOn', { thresholds: (usageCfg?.thresholds || []).join('/') }) : t('super.dunning.usageOff')}
        </p>
      )}

      <Table rowKey="tenant_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty={onlyOpen ? t('super.dunning.emptyOpen') : t('super.dunning.emptyAll')} pagination={{ defaultPageSize: 20 }} />

      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        {t('super.dunning.footNote')}
      </p>
    </div>
  )
}
