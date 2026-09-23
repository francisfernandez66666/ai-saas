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
import type { CellProps, DunningRow, SuperDunningQueueResp, TableRowData } from '../../types'

type ApiResp<T> = { code: number; message?: string; data?: T }

// 序列状态 → 标签配色（running=在催、exhausted=已走完序列（通常已停用）、resolved=已结）
const STATUS_META: Record<string, { label: string; theme: 'primary' | 'success' | 'warning' | 'danger' | 'default' }> = {
  running: { label: '在催', theme: 'warning' },
  exhausted: { label: '序列已尽', theme: 'danger' },
  resolved: { label: '已结', theme: 'success' },
}

/** 格式化时间列（只需到分钟：催缴节奏是天级，秒是噪音） */
function fmtTime(v?: string | null): string {
  return v ? new Date(v).toLocaleString('zh-CN', { hour12: false, hour: '2-digit', minute: '2-digit' }) : '-'
}

/** 催缴队列：档位进度、下次通知时间、人工重发与清序列。 */
export function DunningTab() {
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
      `确认立刻给「${r.tenant_name || r.code}」的管理员再发一封催缴邮件？` +
      `将按其当前进度（到期后第 ${r.day_past} 天、第 ${r.stage} 档）发送，不推进档位序列。`, '立刻再催')
    if (!ok) return
    setBusyId(r.tenant_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/dunning/${r.tenant_id}/nudge`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || '催缴已重发'); void load() }
    else MessagePlugin.error(j?.message || '人工催缴失败')
  }

  // 清序列：线下已付款/谈定缓收时用；只停止催缴，绝不解封
  async function doReset(r: DunningRow) {
    const ok = await confirmDialog(
      `确认关闭「${r.tenant_name || r.code}」的催缴序列？此后系统不再自动催缴，` +
      '但**不会解除停用、也不会改动到期时间与额度**——恢复登录只有"订单到账"一条路。', '关闭催缴序列')
    if (!ok) return
    setBusyId(r.tenant_id)
    const j = await AUTH<{ code: number; message?: string }>(`/api/v1/super/dunning/${r.tenant_id}/reset`, { method: 'POST' })
    setBusyId(null)
    if (j?.code === 0) { MessagePlugin.success(j.message || '催缴序列已关闭'); void load() }
    else MessagePlugin.error(j?.message || '催缴序列处理失败')
  }

  const cols = [
    { colKey: 'tenant_name', title: '租户', width: 180, cell: (p: CellProps) => p.row.tenant_name || `#${p.row.tenant_id}` },
    { colKey: 'code', title: '编码', width: 130 },
    { colKey: 'tenant_status', title: '租户状态', width: 100, cell: (p: CellProps) => (
      <Tag theme={p.row.tenant_status === 'suspended' ? 'danger' : p.row.tenant_status === 'expired' ? 'warning' : 'default'} variant="light">
        {p.row.tenant_status}
      </Tag>) },
    { colKey: 'status', title: '序列', width: 100, cell: (p: CellProps) => {
      const m = STATUS_META[p.row.status] || { label: p.row.status, theme: 'default' as const }
      return <Tag theme={m.theme}>{m.label}</Tag>
    } },
    { colKey: 'stage', title: '档位', width: 80, cell: (p: CellProps) => `${p.row.stage}` },
    { colKey: 'day_past', title: '逾期天数', width: 90 },
    { colKey: 'due_at', title: '到期日', width: 130, cell: (p: CellProps) => fmtTime(p.row.due_at) },
    { colKey: 'grace_end', title: '宽限期止', width: 130, cell: (p: CellProps) => fmtTime(p.row.grace_end) },
    { colKey: 'next_notify_at', title: '下次通知', width: 130, cell: (p: CellProps) => fmtTime(p.row.next_notify_at) },
    { colKey: 'last_notified_at', title: '上次通知', width: 130, cell: (p: CellProps) => fmtTime(p.row.last_notified_at) },
    { colKey: 'sent_to', title: '收件人(脱敏)', width: 170, cell: (p: CellProps) => p.row.sent_to || '-' },
    { colKey: 'op', title: '操作', width: 190, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        <Button size="small" theme="primary" variant="outline" loading={busyId === p.row.tenant_id}
          disabled={busyId !== null} onClick={() => void doNudge(p.row as DunningRow)}>立刻再催</Button>
        <Button size="small" variant="outline" disabled={busyId !== null}
          onClick={() => void doReset(p.row as DunningRow)}>关闭序列</Button>
      </div>) },
  ]

  const cfg = data?.config
  const usageCfg = data?.usage_alert_config

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 12, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>催缴受理</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ fontSize: 12, color: '#718096' }}>只看在催</span>
          <Switch size="small" value={onlyOpen} onChange={(v: boolean) => setOnlyOpen(v)} />
          <Button theme="primary" variant="outline" onClick={() => void load()} loading={loading}>刷新</Button>
        </div>
      </div>

      {/* 开关未开时队列必然为空：这句话必须说清，否则运营会读成"没有一家欠费" */}
      {!cfg?.enabled && (
        <p style={{ fontSize: 13, color: '#9a3412', background: '#fff7ed', borderRadius: 8, padding: '8px 12px' }}>
          催缴自动序列当前未启用（平台开关 dunning_enabled）。下方若有行，是历史巡检留下的序列。
        </p>
      )}
      {!!cfg?.enabled && (
        <p style={{ fontSize: 12, color: '#718096', margin: '0 0 12px' }}>
          平台口径：到期后第 {(cfg.steps || []).join('/')} 天各发一次催缴；
          {cfg.suspend_after_days > 0 ? `第 ${cfg.suspend_after_days} 天自动停止登录（数据保留，续费到账自动恢复）。` : '当前设置只催不封。'}
          {' '}额度预警：{usageCfg?.enabled ? `已启用，越 ${((usageCfg?.thresholds || []).join('/'))}% 档通知管理员。` : '未启用。'}
        </p>
      )}

      <Table rowKey="tenant_id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty={onlyOpen ? '暂无在催租户' : '暂无催缴序列'} pagination={{ defaultPageSize: 20 }} />

      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>
        序列由每小时巡检推进，同一档不会重复轰炸；跳档（服务停了几天才恢复）只补发最高档。
        「立刻再催」不改档位，只按当前进度补发一封并留审计；「关闭序列」用于线下已收款的场合，它不解封、不改到期时间。
        收件人一律脱敏存留（最多 3 个），完整地址不入本表。
      </p>
    </div>
  )
}
