// 获客活码 Tab（获客批 · 批次4，2026-09-23）：建码、启停、出二维码、看漏斗、点开就是那些人。
//
// 页面只做四件事——列码、建码、停/启、下钻。刻意**没有删除**：短码回收等于把新物料的流量
// 并进旧活动的账本，所以后端只给"停用"（对外即不存在、历史数字照读），前端也就不提供删除钮。
// 链接一律用后端下发的 `link` 字段：三级基址优先级（租户热配 > 白标域名 > 请求 Host）只有
// 后端算得准，让前端拼一遍就等于把"印错海报"的风险挪到看不见实现的地方。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, MessagePlugin, Select, Table, Tag } from 'tdesign-react'
import { apiFetch, AUTH } from '../../lib/api'
import type { AcqCodeListResp, AcqCodeRow, AcqConfig, AcqDrillResp, AcqFunnel } from '../../types'

type ApiResp<T> = { code: number; message?: string; data?: T }
type Drill = { id: number; name: string; metric: string; days: number }

const DAYS_OPTIONS = [7, 30, 90, 365].map((d) => ({ label: `近 ${d} 天`, value: String(d) }))
const STATUS_OPTIONS = [
  { label: '全部状态', value: '' },
  { label: '启用中', value: 'active' },
  { label: '已停用', value: 'disabled' },
]

/** 归一漏斗数字取值（指标码由后端 config.metrics 下发，这里只按 keyof 取值，不放宽成 any）。 */
function funnelValue(row: AcqCodeRow, metric: string): number {
  return Number(row.funnel[metric as keyof AcqFunnel] ?? 0)
}

/** 获客活码列表：窗口/状态筛选 + 建码弹窗 + 漏斗数字下钻 + 二维码出图。 */
export function AcquisitionTab() {
  const [rows, setRows] = useState<AcqCodeRow[]>([])
  const [cfg, setCfg] = useState<AcqConfig | null>(null)
  const [days, setDays] = useState('30')
  const [status, setStatus] = useState('')
  // loading 由"谁触发的"置真（筛选/刷新/写操作），effect 里只负责收回：
  // 在 effect 里同步 setState 会多跑一轮级联渲染，lint 也按这条记警告
  const [loading, setLoading] = useState(true)
  const [form, setForm] = useState({ name: '', channel: '', remark: '' })
  // 弹窗开关单独持有，**不能用"名字非空"推断**：点「新建活码」时表单恰恰是空的，
  // 拿内容当可见位会让弹窗永远打不开（首跑即被单测抓住）。
  const [createOpen, setCreateOpen] = useState(false)
  const [creating, setCreating] = useState(false)
  const [drill, setDrill] = useState<Drill | null>(null)
  const [qr, setQr] = useState<AcqCodeRow | null>(null)

  const load = useCallback(async () => {
    const j = (await AUTH(`/api/v1/admin/acquisition/codes?days=${days}&status=${status}`)) as ApiResp<AcqCodeListResp> | null
    if (j?.code === 0) {
      setRows(j.data?.list || [])
      setCfg(j.data?.config || null)
    } else MessagePlugin.error(j?.message || '活码列表加载失败')
    setLoading(false)
  }, [days, status])

  // 效果体里直接调 load() 会被 lint 判成"同步 setState"（load 内有 setState），
  // 包一层 IIFE 让状态更新明确发生在 await 之后，读起来也更清楚地表明这是异步取数。
  useEffect(() => { void (async () => { await load() })() }, [load])

  /** 主动刷新：loading 在这里置真（触发方持有），load 只负责收回。 */
  const reload = () => { setLoading(true); void load() }

  // 建码：后端拒绝回稳定原因码（name_required/channel_unknown/…），文案可改、码不改
  async function create() {
    setCreating(true)
    const j = (await AUTH('/api/v1/admin/acquisition/codes', { method: 'POST', body: { ...form, name: form.name.trim() } })) as ApiResp<AcqCodeRow> | null
    setCreating(false)
    if (j?.code !== 0) { MessagePlugin.error(j?.message || '建码失败'); return }
    MessagePlugin.success(`已创建活码 ${j.data?.code ?? ''}`)
    setCreateOpen(false)
    setForm({ name: '', channel: '', remark: '' })
    reload()
  }

  // 启停：停用是"对外即不存在"，历史数字仍在列表可读
  async function toggle(row: AcqCodeRow) {
    const active = row.status !== 'active'
    const j = (await AUTH(`/api/v1/admin/acquisition/codes/${row.id}/status`, { method: 'POST', body: { active } })) as ApiResp<{ status: string }> | null
    if (j?.code !== 0) MessagePlugin.error(j?.message || '状态更新失败')
    else MessagePlugin.success(active ? '已启用' : '已停用（对外不再认这个码，历史数字保留）')
    reload()
  }

  async function copyLink(link: string) {
    try {
      await navigator.clipboard.writeText(link)
      MessagePlugin.success('落地链接已复制')
    } catch {
      MessagePlugin.warning('浏览器拒绝了剪贴板，请手动选中链接复制')
    }
  }

  const cols = [
    { colKey: 'name', title: '用途', width: 170, cell: (p: { row: AcqCodeRow }) => (
      <div>
        <div className="text-[13px]">{p.row.name || '未命名'}</div>
        <div className="text-[11px] text-gray-400">{p.row.code}</div>
      </div>
    ) },
    { colKey: 'channel', title: '渠道', width: 100, cell: (p: { row: AcqCodeRow }) => <Tag variant="light">{p.row.channel || '未设'}</Tag> },
    { colKey: 'status', title: '状态', width: 90, cell: (p: { row: AcqCodeRow }) => (
      p.row.status === 'active' ? <Tag theme="success">启用中</Tag> : <Tag theme="default">已停用</Tag>
    ) },
    { colKey: 'scans', title: '扫码', width: 80, cell: (p: { row: AcqCodeRow }) => (
      // 单位是"次"不是"人"，点开就会造出"数字 8、名单 12 条"的自相矛盾，故只展示不给点击
      <span title="事件级计数，单位与名单不同，不可下钻" className="text-[13px] text-gray-500">{p.row.scans}</span>
    ) },
    { colKey: 'funnel', title: `漏斗（近 ${days} 天，点开看名单）`, width: 300, cell: (p: { row: AcqCodeRow }) => (
      <div className="flex flex-wrap gap-1">
        {(cfg?.metrics || []).map((m) => (
          <div key={m} role="button"
            className="px-2 py-1 rounded border border-gray-200 text-[12px] cursor-pointer hover:border-indigo-400"
            onClick={() => setDrill({ id: p.row.id, name: p.row.name, metric: m, days: Number(days) })}>
            {cfg?.labels?.[m] || m} <strong>{funnelValue(p.row, m)}</strong>
          </div>
        ))}
      </div>
    ) },
    { colKey: 'link', title: '落地链接', width: 220, cell: (p: { row: AcqCodeRow }) => (
      <div className="flex items-center gap-1">
        <a href={p.row.link} target="_blank" rel="noreferrer" className="text-[12px] text-indigo-600 truncate max-w-[160px]" title={p.row.link}>{p.row.link}</a>
        <Button size="small" variant="text" onClick={() => void copyLink(p.row.link)}>复制</Button>
      </div>
    ) },
    { colKey: 'op', title: '操作', width: 150, cell: (p: { row: AcqCodeRow }) => (
      <div className="flex gap-1">
        <Button size="small" variant="text" onClick={() => setQr(p.row)}>二维码</Button>
        <Button size="small" variant="text" theme={p.row.status === 'active' ? 'warning' : 'primary'} onClick={() => void toggle(p.row)}>
          {p.row.status === 'active' ? '停用' : '启用'}
        </Button>
      </div>
    ) },
  ]

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
        <div className="text-[13px] text-gray-500">
          每张码对应一个投放位置（海报/展台/员工个人二维码）。客户扫码后在链接里带着短码进对话页，
          建档时把渠道位写进客户来源；<strong>首次触达的码永久生效</strong>，后来的码不再改写归属。
        </div>
        {cfg && !cfg.link_base_configured && (
          <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
            落地基址尚未配置：当前链接用的是<strong>你现在访问的域名</strong>。把它印上海报，换域名或对方从别处打开就打不开——
            请先在「系统配置 · 触达通知」填 <code>acquisition_link_base</code>（或绑定白标域名），再打印物料。
          </div>
        )}
        <div className="flex flex-wrap gap-2 items-center">
          <Select value={days} onChange={(v) => { setDays(String(v)) }} options={DAYS_OPTIONS} style={{ width: 120 }} size="small" />
          <Select value={status} onChange={(v) => { setStatus(String(v ?? '')) }} options={STATUS_OPTIONS} style={{ width: 130 }} size="small" />
          <Button theme="default" variant="outline" onClick={reload} disabled={loading}>{loading ? '加载中…' : '刷新'}</Button>
          <Button theme="primary" onClick={() => { setForm({ name: '', channel: cfg?.channels?.[0] || '', remark: '' }); setCreateOpen(true) }}>+ 新建活码</Button>
          <span className="text-xs text-gray-400">共 {rows.length} 张</span>
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" empty="还没有活码，先建一张试试" loading={loading} />
      </div>

      <Dialog header="新建活码" visible={createOpen} onClose={() => setCreateOpen(false)}
        onConfirm={create} confirmBtn={creating ? '提交中…' : '创建'} width="520px">
        <div className="space-y-3">
          <div>
            <label className="block text-xs text-gray-500 mb-1">用途名（如「门店前台立牌-春季车展」）</label>
            <Input value={form.name} onChange={(v) => setForm({ ...form, name: String(v) })} maxlength={40} placeholder="给运营自己看的名字" />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">投放渠道</label>
            <Select value={form.channel} onChange={(v) => setForm({ ...form, channel: String(v) })} style={{ width: '100%' }}
              options={(cfg?.channels || []).map((c) => ({ label: c, value: c }))} placeholder="选择渠道" />
          </div>
          <div>
            <label className="block text-xs text-gray-500 mb-1">备注（可选）</label>
            <Input value={form.remark} onChange={(v) => setForm({ ...form, remark: String(v) })} placeholder="活动编号、负责人等" />
          </div>
          <p className="text-xs text-gray-400">短码由系统随机生成、全平台唯一，创建后不可改——它一旦被印出去就是客户手里的地址。</p>
        </div>
      </Dialog>

      {qr && <QrDialog row={qr} onClose={() => setQr(null)} />}
      {/* key 用「码+指标+窗口」三元组：换了格子就整块重挂载，页码自然回到第 1 页，
          不必在 effect 里 setState（多一次级联渲染，也被 lint 记一条） */}
      {drill && <DrillPanel key={`${drill.id}:${drill.metric}:${drill.days}`} drill={drill} onClose={() => setDrill(null)} />}
    </div>
  )
}

/** 二维码弹窗：图由后端出（链接内容只有后端算得准），此处取 blob 预览并可另存。 */
function QrDialog({ row, onClose }: { row: AcqCodeRow; onClose: () => void }) {
  const [url, setUrl] = useState('')
  const [err, setErr] = useState('')
  useEffect(() => {
    let revoked = ''
    let alive = true
    // 图片端点要带登录态，<img src> 直接指过去拿不到 Authorization，故 fetch 成 blob
    void (async () => {
      try {
        const res = await apiFetch(`/api/v1/admin/acquisition/codes/${row.id}/qr.png?size=400`)
        if (!res.ok) { if (alive) setErr('二维码获取失败'); return }
        revoked = URL.createObjectURL(await res.blob())
        if (alive) setUrl(revoked)
      } catch {
        if (alive) setErr('网络异常，二维码未加载')
      }
    })()
    return () => { alive = false; if (revoked) URL.revokeObjectURL(revoked) }
  }, [row.id])

  return (
    <Dialog header={`二维码 · ${row.name || row.code}`} visible onClose={onClose} footer={
      <div className="flex gap-2 justify-end">
        <Button theme="default" variant="outline" onClick={onClose}>关闭</Button>
        {url && <a href={url} download={`acquisition-${row.code}.png`}><Button theme="primary">下载 PNG</Button></a>}
      </div>
    } width="420px">
      <div className="flex flex-col items-center gap-3 py-2">
        {err ? <div className="text-[13px] text-red-600">{err}</div>
          : url ? <img src={url} alt={`活码 ${row.code} 二维码`} className="w-[280px] h-[280px]" />
            : <div className="text-[13px] text-gray-400 py-10">生成中…</div>}
        <div className="text-[11px] text-gray-500 break-all text-center px-2">{row.link}</div>
      </div>
    </Dialog>
  )
}

/** 漏斗格子点开的名单：横幅 total 与卡片数字逐字相同（两侧同源，见后端文件头）。 */
function DrillPanel({ drill, onClose }: { drill: Drill; onClose: () => void }) {
  const [data, setData] = useState<AcqDrillResp | null>(null)
  const [page, setPage] = useState(1)
  // 与主列表同一口径：loading 由"谁触发的"置真（挂载首帧为真、翻页按钮），effect 只负责收回
  const [loading, setLoading] = useState(true)

  // 换指标/换码由父层用 key 整块重挂载，页码回到 1（见调用点），此处只跟请求
  useEffect(() => {
    void (async () => {
      const j = (await AUTH(`/api/v1/admin/acquisition/codes/${drill.id}/customers?metric=${drill.metric}&days=${drill.days}&page=${page}&page_size=20`)) as ApiResp<AcqDrillResp> | null
      if (j?.code === 0) setData(j.data || null)
      else MessagePlugin.error(j?.message || '名单读取失败')
      setLoading(false)
    })()
  }, [drill.id, drill.metric, drill.days, page])

  const goto = (p: number) => { setLoading(true); setPage(p) }

  const cols = [
    { colKey: 'name', title: '客户', width: 140, cell: (p: { row: AcqDrillResp['list'][number] }) => p.row.name || `客户 #${p.row.id}` },
    { colKey: 'phone', title: '手机号', width: 140 },
    { colKey: 'journey_stage', title: '旅程阶段', width: 110 },
    { colKey: 'spoke', title: '开过口', width: 90, cell: (p: { row: AcqDrillResp['list'][number] }) => (p.row.spoke ? '是' : '否') },
    { colKey: 'created_at', title: '建档时间', width: 170 },
  ]

  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-[13px]">
          <strong>{drill.name || '该活码'}</strong>
          <span className="text-gray-500"> · 近 {drill.days} 天 </span>
          {data ? <>共 <strong data-testid="acq-drill-total">{data.total}</strong> 位客户（{data.label}）</> : <span className="text-gray-400">加载中…</span>}
        </div>
        <Button size="small" variant="outline" onClick={onClose}>返回活码列表</Button>
      </div>
      <Table rowKey="id" data={data?.list || []} columns={cols} size="small" loading={loading} empty="这一格还没有人" />
      {data && (
        <div className="flex items-center gap-3 text-xs text-gray-500">
          <Button size="small" variant="text" disabled={page <= 1} onClick={() => goto(page - 1)}>上一页</Button>
          <span>第 {page} 页 / 共 {Math.max(1, Math.ceil(data.total / data.page_size))} 页</span>
          <Button size="small" variant="text" disabled={page * data.page_size >= data.total} onClick={() => goto(page + 1)}>下一页</Button>
          <span className="text-gray-400">{data.note}</span>
        </div>
      )}
    </div>
  )
}
