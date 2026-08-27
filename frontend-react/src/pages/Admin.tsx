import { useState, useEffect, useRef } from 'react'
import { Tabs, Input, InputNumber, Switch, Button, MessagePlugin, Tag, Dialog, Drawer, Table, Textarea } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { getToken, setToken, apiJSON } from '../lib/api'

const TabPanel = Tabs.TabPanel

type Cfg = {
  key: string
  category: string
  value: string
  value_type: 'number' | 'bool' | 'string' | 'json'
  description?: string
  default_value?: string
}

const CATEGORY_TABS: { value: string; label: string }[] = [
  { value: 'reply_speed', label: '⚡ 回复速度' },
  { value: 'strategy', label: '🎯 策略引擎' },
  { value: 'mental_stage', label: '🧠 心智阶段' },
  { value: 'ai_chain', label: '🤖 AI链路' },
  { value: 'customers', label: '👥 客户线索' },
  { value: 'flow_engine', label: '📋 流程引擎' },
  { value: 'tags', label: '🏷️ 标签体系' },
  { value: 'billing', label: '💰 商业化' },
  { value: 'notify', label: '📣 触达通知' },
  { value: 'audit', label: '📜 审计日志' },
  { value: 'openapi', label: '🔑 开放平台' },
  { value: 'usage', label: '📊 用量' },
  { value: 'referral', label: '🎁 邀请推广' },
  { value: 'branding', label: '🎨 品牌定制' },
]

const CONFIG_CATS = ['reply_speed', 'strategy', 'mental_stage', 'ai_chain', 'billing', 'notify']

// 配置项渲染面板：按 value_type 渲染不同控件（number/bool/json/string），ai_chain 走专属布局
function ConfigPanels({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  // ai_chain 自定义布局
  if (cfgs[0]?.category === 'ai_chain') {
    return <AIChainPanel cfgs={cfgs} edits={edits} setEdits={setEdits} />
  }
  const cards = cfgs.map((c) => {
    const v = edits[c.key] ?? c.value
    let control: React.ReactNode = null
    if (c.key === 'reply_delay_mode') {
      const instant = v === 'instant'
      control = (
        <div className="flex items-center gap-3">
          <Tag theme={instant ? 'warning' : 'success'}>{instant ? '⚡ 秒回模式' : '🕐 正常延迟'}</Tag>
          <span className="text-xs text-gray-400">使用顶部"⚡ 延迟归零"按钮切换</span>
        </div>
      )
    } else if (c.value_type === 'number') {
      control = (
        <InputNumber
          value={parseFloat(v) || 0}
          step={String(c.default_value).includes('.') ? 0.01 : 1}
          onChange={(val) => setEdits(c.key, String(val ?? 0))}
          style={{ width: '100%' }}
        />
      )
    } else if (c.value_type === 'bool') {
      control = <Switch value={v === 'true'} onChange={(val) => setEdits(c.key, val ? 'true' : 'false')} />
    } else if (c.value_type === 'json') {
      control = <JsonEditor cfg={c} value={v} onChange={(nv) => setEdits(c.key, nv)} />
    } else {
      control = <Input value={v} onChange={(val) => setEdits(c.key, val)} />
    }
    return (
      <div key={c.key} className="bg-white rounded-lg border border-gray-200 p-4">
        <h3 className="text-sm font-semibold text-gray-800">{c.description || c.key}</h3>
        <p className="text-xs text-gray-400 mt-0.5">
          {c.key} <span className="ml-2 px-1.5 py-0.5 bg-gray-100 text-gray-500 rounded text-xs">{c.value_type}</span>
        </p>
        <div className="mt-3">{control}</div>
        {c.default_value ? <p className="text-xs text-gray-400 mt-2">默认值: {c.default_value}</p> : null}
      </div>
    )
  })
  return <div className="grid grid-cols-1 md:grid-cols-2 gap-4">{cards}</div>
}

// JSON 类型配置编辑器：区分数字数组/字符串数组/纯文本三种形态的可视化编辑
function JsonEditor({ cfg, value, onChange }: { cfg: Cfg; value: string; onChange: (v: string) => void }) {
  let parsed: any = null
  try { parsed = JSON.parse(value) } catch { parsed = null }
  if (Array.isArray(parsed)) {
    if (parsed.length > 0 && typeof parsed[0] === 'number') {
      return (
        <div className="flex flex-wrap items-center gap-2">
          {parsed.map((n, i) => (
            <span key={i} className="inline-flex items-center bg-indigo-50 text-indigo-700 rounded px-2 py-1 text-sm">
              <input
                type="number"
                className="w-14 bg-transparent text-center outline-none border-r border-indigo-200"
                value={n}
                onChange={(e) => {
                  const arr = [...parsed]
                  arr[i] = parseInt(e.target.value) || 0
                  onChange(JSON.stringify(arr))
                }}
              />
              <button className="ml-1 text-indigo-400 hover:text-indigo-700" onClick={() => { const arr = parsed.filter((_: any, j: number) => j !== i); onChange(JSON.stringify(arr)) }}>✕</button>
            </span>
          ))}
          <Button size="small" variant="outline" theme="primary" onClick={() => onChange(JSON.stringify([...parsed, 0]))}>+ 添加</Button>
        </div>
      )
    }
    return (
      <div className="flex flex-wrap items-center gap-2">
        {parsed.map((s: string, i: number) => (
          <span key={i} className="inline-flex items-center bg-emerald-50 text-emerald-700 rounded px-2 py-1 text-sm">
            {s}
            <button className="ml-1 text-emerald-400 hover:text-emerald-700" onClick={() => { const arr = parsed.filter((_: any, j: number) => j !== i); onChange(JSON.stringify(arr)) }}>✕</button>
          </span>
        ))}
        <Button size="small" variant="outline" theme="primary" onClick={() => { const t = prompt('请输入新项'); if (t) onChange(JSON.stringify([...parsed, t])) }}>+ 添加</Button>
      </div>
    )
  }
  return <Textarea value={value} onChange={(v) => onChange(v)} autosize={{ minRows: 2, maxRows: 6 }} />
}

// AI 链路面板：展示模型降级优先级（可拖拽排序）与 Mock 开关（开启后不调真实模型）
function AIChainPanel({ cfgs, edits, setEdits }: { cfgs: Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void }) {
  const priority = cfgs.find((c) => c.key === 'model_priority')
  const mock = cfgs.find((c) => c.key === 'mock_mode')
  let models: string[] = []
  try { models = JSON.parse(edits[priority?.key || 'model_priority'] || priority?.value || '[]') } catch { models = [] }
  const fallbackNames: Record<string, string> = {
    siliconflow_deepseek_v4_flash: 'DeepSeek V4 Flash (硅基流动)',
    siliconflow_glm4_9b: 'GLM-4-9B (硅基流动免费)',
    zhipu_glm4_flash: 'GLM-4-Flash (智谱)',
    template_fallback: '模板兜底（不调用AI）',
  }
  const move = (i: number, dir: -1 | 1) => {
    const arr = [...models]
    const j = i + dir
    if (j < 0 || j >= arr.length) return
    ;[arr[i], arr[j]] = [arr[j], arr[i]]
    if (priority) setEdits(priority.key, JSON.stringify(arr))
  }
  return (
    <div className="space-y-6">
      {priority && (
        <div className="bg-white rounded-lg border border-gray-200 p-5">
          <h3 className="text-sm font-semibold text-gray-800">模型降级优先级</h3>
          <p className="text-xs text-gray-400 mt-0.5">调整顺序，排在前面的模型优先使用</p>
          <ul className="mt-3 space-y-2">
            {models.map((id, i) => (
              <li key={id} className="flex items-center bg-white border border-gray-200 rounded-lg px-4 py-3">
                <span className="text-gray-400 mr-3 font-mono text-xs">{i + 1}</span>
                <span className="text-sm font-medium text-gray-800 flex-1">{fallbackNames[id] || id}</span>
                <span className="text-xs text-gray-400 font-mono mr-3">{id}</span>
                <Button size="small" variant="text" onClick={() => move(i, -1)}>↑</Button>
                <Button size="small" variant="text" onClick={() => move(i, 1)}>↓</Button>
              </li>
            ))}
          </ul>
        </div>
      )}
      {mock && (
        <div className="bg-white rounded-lg border border-gray-200 p-4 flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-gray-800">{mock.description}</h3>
            <p className="text-xs text-orange-500 mt-2">⚠️ Mock模式开启后AI不会真实调用模型，仅返回模拟回复</p>
          </div>
          <Switch value={(edits[mock.key] ?? mock.value) === 'true'} onChange={(val) => setEdits(mock.key, val ? 'true' : 'false')} />
        </div>
      )}
    </div>
  )
}

// 租户后台管理：登录后按分类 Tab 编辑系统配置（热加载）、查看客户线索/流程/标签/品牌/审计/开放平台/用量/邀请
// 标签体系 Tab 已接入后端 /admin/tags CRUD（按 category 分组增删），自动打标规则为只读展示
// 依赖 /api/v1/admin/config（读写重置）、/api/v1/auth/login、/api/v1/admin/audit-logs、/api/v1/admin/apikeys、/api/v1/admin/usage/summary、/api/v1/admin/referral/*
export default function Admin() {
  const brand = useBrand()
  const [tab, setTab] = useState('reply_speed')
  const [logged, setLogged] = useState(!!getToken())
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [tenantCode, setTenantCode] = useState('')
  const [loginErr, setLoginErr] = useState('')
  const [all, setAll] = useState<Cfg[]>([])
  const [edits, setEditsState] = useState<Record<string, string>>({})
  const setEdits = (k: string, v: string) => setEditsState((s) => ({ ...s, [k]: v }))

  async function doLogin(e: React.FormEvent) {
    e.preventDefault()
    setLoginErr('')
    const res = await fetch('/api/v1/auth/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password, tenant_code: tenantCode }),
    })
    const j = await res.json()
    if (j.code !== 0) { setLoginErr(j.message || '登录失败'); return }
    setToken(j.data.token)
    localStorage.setItem('role', j.data.user.role)
    setLogged(true)
    loadAll()
  }

  async function loadAll() {
    const r = await fetch('/api/v1/admin/config', { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    if (j.code === 0) {
      setAll(j.data || [])
      const e: Record<string, string> = {}
      ;(j.data || []).forEach((c: Cfg) => (e[c.key] = c.value))
      setEditsState(e)
    }
  }
  useEffect(() => { if (logged) loadAll() }, [logged])

  async function saveAll() {
    const updates = all.map((c) => {
      const v = edits[c.key] ?? c.value
      // string 类型需 JSON 序列化后提交，其余直接字符串化
      // 注意：此分支仅处理 string 类型，其余类型走下方 String(v)
      if (c.value_type === 'string') return { key: c.key, value: JSON.stringify(v) }
      return { key: c.key, value: String(v) }
    })
    const res = await fetch('/api/v1/admin/config', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() },
      body: JSON.stringify(updates),
    })
    const j = await res.json()
    if (j.code === 0) { MessagePlugin.success('配置已保存并热加载'); loadAll() }
    else MessagePlugin.error('保存失败: ' + (j.message || ''))
  }
  async function resetAll() {
    if (!confirm('确定恢复所有配置为默认值？不可撤销')) return
    await fetch('/api/v1/admin/config/reset', { method: 'POST', headers: { Authorization: 'Bearer ' + getToken() } })
    MessagePlugin.success('已恢复默认'); loadAll()
  }
  async function zeroDelayAll() {
    if (!confirm('将所有延迟参数清零并切换为秒回模式？')) return
    const e = { ...edits }
    // 将所有数值型延迟参数清零并切换为秒回（instant）模式，便于调试实时性
    all.forEach((c) => { if (c.value_type === 'number') e[c.key] = '0' })
    e['reply_delay_mode'] = 'instant'
    setEditsState(e)
    const updates = all.map((c) => ({ key: c.key, value: String(e[c.key] ?? c.value) }))
    await fetch('/api/v1/admin/config', { method: 'PUT', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() }, body: JSON.stringify(updates) })
    MessagePlugin.success('延迟已归零'); loadAll()
  }

  if (!logged) {
    return (
      <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: '#f5f7fa' }}>
        <form onSubmit={doLogin} style={card}>
          <h1 style={{ fontSize: 18, fontWeight: 600, marginBottom: 16 }}>后台管理员登录</h1>
          <input placeholder="租户码 (如 rox-admin)" value={tenantCode} onChange={(e) => setTenantCode(e.target.value)} style={inp} />
          <input placeholder="用户名" value={username} onChange={(e) => setUsername(e.target.value)} style={inp} />
          <input placeholder="密码" type="password" value={password} onChange={(e) => setPassword(e.target.value)} style={inp} />
          {loginErr && <div style={{ color: '#dc2626', fontSize: 12, marginBottom: 8 }}>{loginErr}</div>}
          <Button theme="primary" type="submit" style={{ width: '100%' }}>登录</Button>
          <div style={{ marginTop: 12, fontSize: 13 }}><a href="/register">没有账号？免费开通</a></div>
        </form>
      </div>
    )
  }

  const configsFor = (cat: string) => all.filter((c) => c.category === cat)
  const noAction = ['customers', 'flow_engine', 'tags', 'audit', 'openapi', 'usage', 'referral', 'branding'].includes(tab)
  const logo = brand.logoUrl ? <img src={brand.logoUrl} alt="" style={{ height: 28, marginRight: 8 }} /> : null

  return (
    <div style={{ minHeight: '100vh', background: '#f9fafb' }}>
      <header style={header}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {logo}
          <h1 style={{ fontSize: 18, fontWeight: 700 }}>AI自动化SCRM - 后台管理</h1>
        </div>
        <span style={{ fontSize: 13, color: '#6b7280' }}>已连接</span>
      </header>
      <div style={{ maxWidth: 1100, margin: '0 auto', padding: '0 24px' }}>
        <Tabs value={tab} onChange={(v) => setTab(v as string)} size="medium">
          {CATEGORY_TABS.map((t) => (
            <TabPanel key={t.value} value={t.value} label={t.label}>
              <PanelContent tab={t.value} configsFor={configsFor} edits={edits} setEdits={setEdits} all={all} />
            </TabPanel>
          ))}
        </Tabs>
      </div>
      {!noAction && (
        <div style={{ maxWidth: 1100, margin: '0 auto', padding: '0 24px 24px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
          <p style={{ fontSize: 13, color: '#6b7280' }}>修改参数后点击"保存配置"，即时生效，无需重启</p>
          <div style={{ display: 'flex', gap: 12 }}>
            <Button theme="warning" variant="outline" onClick={zeroDelayAll}>⚡ 延迟归零</Button>
            <Button theme="default" variant="outline" onClick={resetAll}>↩ 恢复默认</Button>
            <Button theme="primary" onClick={saveAll}>💾 保存配置</Button>
          </div>
        </div>
      )}
    </div>
  )
}

function PanelContent({ tab, configsFor, edits, setEdits, all }: { tab: string; configsFor: (c: string) => Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void; all: Cfg[] }) {
  if (CONFIG_CATS.includes(tab)) return <ConfigPanels cfgs={configsFor(tab)} edits={edits} setEdits={setEdits} />
  if (tab === 'customers') return <CustomersTab />
  if (tab === 'flow_engine') return <FlowEngineTab configs={all} />
  if (tab === 'tags') return <TagSystemTab />
  if (tab === 'branding') return <BrandingTab />
  if (tab === 'audit') return <AuditTab />
  if (tab === 'openapi') return <OpenApiTab />
  if (tab === 'usage') return <UsageTab />
  if (tab === 'referral') return <ReferralTab />
  return <Placeholder name={tab} />
}

function Placeholder({ name }: { name: string }) {
  return (
    <div className="bg-white rounded-lg border border-gray-200 p-10 text-center">
      <div style={{ fontSize: 40, color: '#d1d5db' }} className="mb-3">🚧</div>
      <p style={{ color: '#9ca3af' }}>「{name}」模块正在迁移中，下一轮迭代补齐</p>
    </div>
  )
}

// ------------------------- 客户线索 -------------------------
const STAGE_LABELS: Record<string, string> = { ai_connected: 'AI建联', human_connected: '人工建联', lead_captured: '已留资', arrived: '已到店', ordered: '已下单', delivered: '已交车', lost: '已战败' }
const STAGE_COLORS: Record<string, string> = { ai_connected: 'bg-gray-100 text-gray-600', human_connected: 'bg-blue-100 text-blue-600', lead_captured: 'bg-cyan-100 text-cyan-600', arrived: 'bg-green-100 text-green-600', ordered: 'bg-orange-100 text-orange-600', delivered: 'bg-red-100 text-red-600', lost: 'bg-gray-200 text-gray-600' }
const STATUS_OPTS = ['', 'ai_connected', 'human_connected', 'lead_captured', 'arrived', 'ordered', 'delivered', 'lost']

// 客户线索 Tab：按阶段筛选、分页加载客户，抽屉展示详情/试驾单/聊天记录
// 依赖 /api/v1/advisor/customers、/api/v1/advisor/customer/:id、/api/v1/advisor/test-drives、/api/v1/chat/history
function CustomersTab() {
  const [filter, setFilter] = useState('')
  const [page, setPage] = useState(1)
  const [leads, setLeads] = useState<any[]>([])
  const [loading, setLoading] = useState(false)
  const [detail, setDetail] = useState<any>(null)
  const [drawer, setDrawer] = useState(false)
  const [testDrives, setTestDrives] = useState<any[]>([])
  const [chat, setChat] = useState<any[]>([])
  const [showTd, setShowTd] = useState(false)
  const [showChat, setShowChat] = useState(false)
    // 匿名访客（用户名以"访客_"开头）在 UI 上统一展示为"客户"，避免泄露原始标识
  const H = (n?: string) => (!n || n.startsWith('访客_')) ? '客户' : n
  const HI = (n?: string) => (!n || n.startsWith('访客_')) ? '客' : (n?.[0] || '?')

  async function load() {
    setLoading(true)
    try {
      const r = await fetch(`/api/v1/advisor/customers?status=${filter}&page=${page}&page_size=20&assigned=all`, { headers: { Authorization: 'Bearer ' + getToken() } })
      const j = await r.json()
      if (j.code === 0) setLeads((j.data?.list) || [])
    } finally { setLoading(false) }
  }
  useEffect(() => { load() }, [filter, page])

  async function openDetail(id: number) {
    const r = await fetch(`/api/v1/advisor/customer/${id}`, { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    if (j.code === 0) { setDetail(j.data); setDrawer(true); setShowTd(false); setShowChat(false); setTestDrives([]); setChat([]) }
    else MessagePlugin.error('获取详情失败')
  }
  async function loadTd() {
    if (showTd) { setShowTd(false); return }
    const cid = detail?.customer?.id
    const r = await fetch(`/api/v1/advisor/test-drives?customer_id=${cid}`, { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    setTestDrives(j.data || []); setShowTd(true)
  }
  async function loadChat() {
    if (showChat) { setShowChat(false); return }
    const cid = detail?.customer?.id
    const r = await fetch(`/api/v1/chat/history?customer_id=${cid}&limit=30`, { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    setChat(j.data || []); setShowChat(true)
  }
  const c = detail?.customer
  const who = (t: string) => (t === 'customer' ? '客户' : t === 'ai' ? 'AI' : t === 'human' ? '人工' : '系统')
  const wcolor = (t: string) => (t === 'customer' ? 'text-blue-600' : t === 'ai' ? 'text-green-600' : 'text-orange-600')

  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex items-center gap-3">
        <span className="text-sm text-gray-500">筛选阶段：</span>
        <select value={filter} onChange={(e) => { setFilter((e.target as HTMLSelectElement).value); setPage(1) }} className="px-3 py-2 border rounded-lg text-sm">
          {STATUS_OPTS.map((s) => <option key={s} value={s}>{s === '' ? '全部' : (STAGE_LABELS[s] || s)}</option>)}
        </select>
        <Button variant="outline" theme="primary" onClick={() => { setPage(1); load() }}>刷新</Button>
        <span className="text-xs text-gray-400">共 {leads.length} 条（当前页）</span>
      </div>
      <div className="space-y-2">
        {loading && <div className="text-center text-gray-400 py-6">加载中...</div>}
        {!loading && leads.length === 0 && <div className="text-center text-gray-400 py-6">暂无客户线索</div>}
        {leads.map((l) => (
          <div key={l.id} onClick={() => openDetail(l.id)} className="bg-white rounded-lg border border-gray-100 p-3 hover:bg-gray-50 cursor-pointer flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="w-9 h-9 bg-gray-100 rounded-full flex items-center justify-center text-sm font-medium text-gray-500">{HI(l.name)}</div>
              <div>
                <div className="flex items-center gap-2">
                  <span className="text-sm font-medium text-gray-800">{H(l.name)}</span>
                  <span className={`text-[10px] px-1.5 py-0.5 rounded-full ${STAGE_COLORS[l.journey_stage] || 'bg-gray-100 text-gray-500'}`}>{STAGE_LABELS[l.journey_stage] || l.journey_stage || '-'}</span>
                  {l.assigned_user_name && <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-indigo-100 text-indigo-600">{l.assigned_user_name}</span>}
                </div>
                <p className="text-xs text-gray-400">{l.phone || ''} {l.interest_model ? '· ' + l.interest_model : ''}</p>
              </div>
            </div>
            <div className="text-right">
              <p className="text-xs text-gray-400">{l.last_message ? l.last_message.slice(0, 30) + '...' : '暂无消息'}</p>
              <p className="text-[10px] text-gray-300">{l.updated_at ? new Date(l.updated_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''}</p>
            </div>
          </div>
        ))}
      </div>
      <Drawer visible={drawer} onClose={() => setDrawer(false)} size="520px" header="客户线索详情">
        {c && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">
              <div><span className="text-xs text-gray-400">姓名</span><p className="text-sm font-medium">{H(c.name) || '-'}</p></div>
              <div><span className="text-xs text-gray-400">手机</span><p className="text-sm font-medium">{c.phone || '-'}</p></div>
              <div><span className="text-xs text-gray-400">兴趣车型</span><p className="text-sm">{c.interest_model || '-'}</p></div>
              <div><span className="text-xs text-gray-400">预算</span><p className="text-sm">{c.budget > 0 ? c.budget + '万' : '-'}</p></div>
              <div><span className="text-xs text-gray-400">意向分</span><p className="text-sm">{((c.intent_score || 0) * 100).toFixed(0)}%</p></div>
              <div><span className="text-xs text-gray-400">城市</span><p className="text-sm">{c.city || '-'}</p></div>
            </div>
            <div><span className="text-xs text-gray-400">分配顾问</span><p className="text-sm font-medium text-indigo-600">{detail.assigned_user_name || (c.assigned_user_id > 0 ? '顾问' + c.assigned_user_id : '未分配')}</p></div>
            <div><span className="text-xs text-gray-400">标签</span><div className="mt-1 flex flex-wrap gap-1">{(detail.tags || []).map((t: any, i: number) => <Tag key={i} theme="primary" variant="light">{t.tag_name}</Tag>)} {(detail.tags || []).length === 0 && <span className="text-xs text-gray-300">暂无</span>}</div></div>
            <div><span className="text-xs text-gray-400">备注</span><p className="text-sm text-gray-600 mt-1">{c.remark || '-'}</p></div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="success" variant="outline" onClick={loadTd}>{showTd ? '🚗 隐藏试驾单' : '🚗 查看试驾单'}</Button>
              <div className="mt-2 space-y-2">
                {showTd && testDrives.length === 0 && <div className="text-gray-400 text-xs">暂无试驾记录</div>}
                {testDrives.map((td, i) => (
                  <div key={i} className="bg-gray-50 rounded-lg p-2 border border-gray-100">
                    <div className="flex items-center justify-between"><span className="text-xs font-medium text-gray-700">{td.model_name || '试驾'}</span><span className="text-[10px] text-gray-500">{td.status === 'pending' ? '待试驾' : td.status === 'completed' ? '已完成' : td.status === 'cancelled' ? '已取消' : td.status}</span></div>
                    <p className="text-xs text-gray-400 mt-0.5">{td.scheduled_at ? new Date(td.scheduled_at).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) : ''} {td.location ? '· ' + td.location : ''}</p>
                  </div>
                ))}
              </div>
            </div>
            <div className="pt-2 border-t border-gray-100">
              <Button size="small" theme="primary" variant="outline" onClick={loadChat}>{showChat ? '💬 隐藏聊天记录' : '💬 查看聊天记录'}</Button>
              <div className="mt-2 max-h-48 overflow-y-auto bg-gray-50 rounded-lg p-3 text-xs space-y-1">
                {showChat && chat.length === 0 && <div className="text-gray-400">暂无聊天记录</div>}
                {chat.map((m, i) => <div key={i}><span className={`${wcolor(m.sender_type)} font-medium`}>[{who(m.sender_type)}]</span> {m.content}</div>)}
              </div>
            </div>
          </div>
        )}
      </Drawer>
    </div>
  )
}

// ------------------------- 流程引擎可视化（静态 + 配置摘要） -------------------------
const nodeStyle = (c: string): React.CSSProperties => ({ minWidth: 120, padding: '10px 14px', borderRadius: 10, color: '#fff', textAlign: 'center', fontSize: 13, fontWeight: 600, background: c })
// 流程引擎 Tab：静态可视化客户旅程与策略决策链路，并摘要当前配置关键参数（实时读取）
function FlowEngineTab({ configs }: { configs: Cfg[] }) {
  const stages = [
    { t: 'AI建联', d: 'AI自动首次触达', c: '#6366f1' },
    { t: '人工建联', d: '顾问接手跟进', c: '#0ea5e9' },
    { t: '已留资', d: '客户留下联系方式', c: '#06b6d4' },
    { t: '已到店', d: '客户到店看车', c: '#22c55e' },
    { t: '已下单', d: '客户支付定金', c: '#f97316' },
    { t: '已交车', d: '完成交付，成交闭环', c: '#ef4444' },
  ]
  const steps = [
    { n: '1', t: '意图分析', d: '解析客户消息意图和关键词' },
    { n: '2', t: '锚点选择', d: '根据意图匹配话术锚点' },
    { n: '2.5', t: '阶段锁 🔒', d: 'aggressiveness ≤ 当前阶段上限', lock: true },
    { n: '3', t: '模板匹配', d: '查找匹配的话术模板' },
    { n: '4', t: '降级判断', d: '模板不匹配时降级处理' },
    { n: '5', t: '回复生成', d: '组装最终AI回复并发送' },
  ]
  const keys = ['tau', 'merge_window', 'stage_lock', 'aggressiveness_default', 'max_delay', 'min_delay', 'delay_range', 'model_priority', 'mock_mode', 'reply_probability']
  const chips = keys.map((k) => { const cfg = configs.find((c) => c.key === k); return cfg ? { k, v: cfg.value.length > 40 ? cfg.value.slice(0, 37) + '...' : cfg.value } : null }).filter(Boolean) as { k: string; v: string }[]
  const Arrow = () => <span className="text-gray-300 text-xl px-2">→</span>
  return (
    <div className="space-y-8">
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="flex items-center gap-2 mb-5"><span className="text-lg">🗺️</span><div><h3 className="text-base font-bold text-gray-800">客户旅程流程图</h3><p className="text-xs text-gray-400 mt-0.5">6 个主流程阶段 + 1 个独立战败分支</p></div></div>
        <div className="overflow-x-auto pb-4"><div className="flex items-center min-w-max">
          {stages.map((s, i) => (<><div key={s.t} className="flex flex-col items-center"><div style={nodeStyle(s.c)}><div className="font-bold text-sm">{s.t}</div><div className="text-[10px] opacity-80 mt-0.5">{s.d}</div></div><span className="text-[10px] text-gray-400 mt-1">Stage {i + 1}</span></div>{i < stages.length - 1 && <Arrow key={'a' + i} />}</>))}
        </div></div>
        <div className="mt-4 pt-4 border-t border-dashed border-gray-200 flex items-center gap-4">
          <span className="text-xs text-gray-500 font-semibold">⚠️ 独立分支</span>
          <span className="text-[10px] text-gray-400">任一阶段均可转入战败，且不可逆转</span>
          <Arrow />
          <div className="flex flex-col items-center"><div style={nodeStyle('#9ca3af')}><div className="font-bold text-sm">已战败</div><div className="text-[10px] opacity-80 mt-0.5">客户明确拒绝/竞品成交</div></div><span className="text-[10px] text-gray-400 mt-1">Lost</span></div>
          <span className="text-[10px] px-2 py-0.5 bg-red-50 text-red-500 rounded">不可逆转</span>
        </div>
      </div>
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="flex items-center gap-2 mb-5"><span className="text-lg">⚙️</span><div><h3 className="text-base font-bold text-gray-800">策略引擎决策流程图</h3><p className="text-xs text-gray-400 mt-0.5">从意图分析到回复生成的 6 步决策链路</p></div></div>
        <div className="overflow-x-auto pb-4"><div className="flex items-start min-w-max" style={{ gap: 12 }}>
          {steps.map((s, i) => (<><div key={s.t} style={{ width: 150 }} className="flex flex-col items-center"><div style={{ width: '100%', padding: '10px 12px', borderRadius: 10, border: s.lock ? '1px solid #f59e0b' : '1px solid #e0e7ff', background: s.lock ? '#fffbeb' : '#eef2ff' }}><span className="inline-block w-5 h-5 text-center rounded-full bg-indigo-600 text-white text-[10px] leading-5 mr-1">{s.n}</span><span className="font-bold text-sm text-indigo-700">{s.t}</span><div className="text-[10px] text-gray-500 mt-1">{s.d}</div></div></div>{i < steps.length - 1 && <span key={'b' + i} className="text-gray-300 text-xl pt-4">→</span>}</>))}
        </div></div>
        <div className="mt-4 pt-4 border-t border-gray-100 flex items-start gap-4 flex-wrap text-xs text-gray-500">
          <span className="text-gray-500 font-semibold mt-0.5">降级路径：</span>
          <span className="px-2 py-0.5 bg-indigo-50 text-indigo-600 rounded">模板匹配成功 → 直接使用模板</span>
          <span className="px-2 py-0.5 bg-amber-50 text-amber-600 rounded">模板匹配失败 → 调用LLM生成</span>
          <span className="px-2 py-0.5 bg-red-50 text-red-600 rounded">LLM也失败 → 兜底回复</span>
        </div>
      </div>
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="flex items-center gap-2 mb-4"><span className="text-lg">📊</span><div><h3 className="text-base font-bold text-gray-800">当前配置参数摘要</h3><p className="text-xs text-gray-400 mt-0.5">从配置数据中提取的关键参数（实时读取）</p></div></div>
        <div className="flex flex-wrap gap-2">
          {chips.length === 0 && <div className="text-sm text-gray-400 py-4">暂无配置数据，请先初始化配置</div>}
          {chips.map((ch) => (
            <div key={ch.k} className="px-3 py-1.5 bg-gray-50 border border-gray-200 rounded-lg text-xs"><span className="text-gray-500 font-mono">{ch.k}</span> <span className="text-gray-800">= {ch.v}</span></div>
          ))}
        </div>
      </div>
    </div>
  )
}

// ------------------------- 标签体系（接后端 /admin/tags） -------------------------
// 标签分类与后端 tag.category 字段对齐：展示用中文，存储用英文 key
const TAG_CATS = [
  { key: 'intent', name: '意向等级', tags: ['高意向', '中意向', '低意向', '无意向'] },
  { key: 'source', name: '来源渠道', tags: ['线上广告', '朋友推荐', '到店咨询', 'IG', '小红书'] },
  { key: 'car', name: '兴趣车型', tags: ['ADAMAS', '01', '其他'] },
  { key: 'type', name: '客户类型', tags: ['首次咨询', '回头客', '转介绍'] },
  { key: 'status', name: '跟进状态', tags: ['待跟进', '跟进中', '已试驾', '已到店', '已战败'] },
]
const AUTO_TAG_RULES = [
  { trigger: '消息中出现手机号', tag: '已留资', category: '跟进状态' },
  { trigger: '消息中提到试驾', tag: '高意向', category: '意向等级' },
  { trigger: '消息中提到价格/预算', tag: '中意向', category: '意向等级' },
  { trigger: '消息中提到到店/看车', tag: '已到店', category: '跟进状态' },
  { trigger: '客户3天未回复', tag: '低意向', category: '意向等级' },
  { trigger: '客户明确拒绝/竞品成交', tag: '已战败', category: '跟进状态' },
  { trigger: '消息中提到朋友推荐', tag: '转介绍', category: '客户类型' },
  { trigger: '客户首次发送消息', tag: '首次咨询', category: '客户类型' },
]
const CAT_COLOR: Record<string, string> = { intent: 'bg-indigo-50 text-indigo-600', source: 'bg-emerald-50 text-emerald-600', car: 'bg-cyan-50 text-cyan-600', type: 'bg-amber-50 text-amber-600', status: 'bg-rose-50 text-rose-600' }
// 标签体系 Tab：接后端 /admin/tags CRUD（按 category 分组）；自动打标规则只读
function TagSystemTab() {
  const [byCat, setByCat] = useState<Record<string, any[]>>({})
  const [loading, setLoading] = useState(true)
  const load = async () => {
    setLoading(true)
    const { res, json } = await apiJSON('/api/v1/admin/tags?page_size=500')
    if (res.ok && json?.code === 0) {
      const list: any[] = json.data?.list || []
      const grouped: Record<string, any[]> = {}
      for (const c of TAG_CATS) grouped[c.key] = list.filter((t) => t.category === c.key)
      setByCat(grouped)
    } else {
      MessagePlugin.error(json?.message || '标签加载失败')
    }
    setLoading(false)
  }
  useEffect(() => { load() }, [])
  const add = async (catKey: string) => {
    const text = prompt('请输入新标签名称')
    if (!text || !text.trim()) return
    const name = text.trim()
    const code = name.toLowerCase().replace(/\s+/g, '_')
    const { json } = await apiJSON('/api/v1/admin/tags', {
      method: 'POST',
      body: JSON.stringify({ name, code, category: catKey, status: 1 }),
    })
    if (json?.code !== 0) { MessagePlugin.error(json?.message || '添加失败'); return }
    MessagePlugin.success('已添加'); load()
  }
  const remove = async (catKey: string, tag: any) => {
    if (!confirm(`确定删除标签"${tag.name}"吗？`)) return
    const { json } = await apiJSON('/api/v1/admin/tags/' + tag.id, { method: 'DELETE' })
    if (json?.code !== 0) { MessagePlugin.error(json?.message || '删除失败'); return }
    MessagePlugin.success('已删除'); load()
  }
  return (
    <div className="space-y-8">
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="flex items-center gap-2 mb-5"><span className="text-lg">📁</span><div><h3 className="text-base font-bold text-gray-800">标签分类管理</h3><p className="text-xs text-gray-400 mt-0.5">管理标签分类及分类下的标签值（已接入后端）</p></div></div>
        {loading && <p className="text-xs text-gray-400">加载中…</p>}
        <div className="space-y-4">
          {TAG_CATS.map((cat) => (
            <div key={cat.key} className="border border-gray-100 rounded-lg p-4">
              <div className="flex items-center justify-between mb-3"><div className="flex items-center gap-2"><span className="text-sm font-semibold text-gray-700">{cat.name}</span><span className="text-[10px] px-2 py-0.5 bg-gray-100 text-gray-500 rounded-full">{(byCat[cat.key] || []).length} 个标签</span></div><Button size="small" theme="primary" variant="outline" onClick={() => add(cat.key)}>+ 添加标签</Button></div>
              <div className="flex flex-wrap gap-2">
                {(byCat[cat.key] || []).map((tag: any) => (
                  <span key={tag.id} className={`inline-flex items-center gap-1 text-xs px-2 py-1 rounded-full ${CAT_COLOR[cat.key] || 'bg-gray-100 text-gray-600'}`}>{tag.name}<button className="opacity-60 hover:opacity-100" onClick={() => remove(cat.key, tag)}>✕</button></span>
                ))}
                {(byCat[cat.key] || []).length === 0 && <span className="text-xs text-gray-300">暂无标签</span>}
              </div>
            </div>
          ))}
        </div>
      </div>
      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="flex items-center justify-between mb-5"><div className="flex items-center gap-2"><span className="text-lg">🤖</span><div><h3 className="text-base font-bold text-gray-800">自动打标规则</h3><p className="text-xs text-gray-400 mt-0.5">系统根据消息内容自动打标签（只读）</p></div></div><span className="text-[10px] px-2 py-1 bg-gray-100 text-gray-500 rounded-lg">🔒 由AI策略引擎控制，暂不支持手动配置</span></div>
        <table className="w-full text-sm">
          <thead><tr className="border-b border-gray-200 text-left text-xs font-semibold text-gray-500"><th className="py-2 px-3 w-8">#</th><th className="py-2 px-3">触发条件</th><th className="py-2 px-3">自动打标</th><th className="py-2 px-3">所属分类</th></tr></thead>
          <tbody>
            {AUTO_TAG_RULES.map((r, i) => (
              <tr key={i} className="border-b border-gray-50"><td className="py-2.5 px-3 text-xs text-gray-400">{i + 1}</td><td className="py-2.5 px-3 text-gray-700">{r.trigger}</td><td className="py-2.5 px-3"><span className="inline-block text-xs px-2 py-0.5 rounded-full bg-indigo-50 text-indigo-600">{r.tag}</span></td><td className="py-2.5 px-3 text-xs text-gray-500">{r.category}</td></tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

// ------------------------- 品牌定制 -------------------------
// 品牌定制 Tab：配置租户白标（域名/名称/Logo/主题色），写入 /api/v1/admin/tenant/branding
function BrandingTab() {
  const [f, setF] = useState({ custom_domain: '', brand_name: '', brand_link: '', logo_url: '', favicon_url: '', primary_color: '', secondary_color: '' })
  const [msg, setMsg] = useState('')
  useEffect(() => {
    fetch('/api/v1/public/branding').then((r) => r.json()).then((b) => {
      setF({
        custom_domain: b.custom_domain || '',
        brand_name: b.brand_name && b.brand_name !== '跨山 LexCross' ? b.brand_name : '',
        brand_link: b.brand_link || '',
        logo_url: b.logo_url || '',
        favicon_url: b.favicon_url || '',
        primary_color: b.primary_color || '',
        secondary_color: b.secondary_color || '',
      })
    }).catch(() => {})
  }, [])
  async function save() {
    setMsg('保存中...')
    const payload = {
      custom_domain: f.custom_domain.trim() || null,
      brand_name: f.brand_name.trim(),
      brand_link: f.brand_link.trim(),
      logo_url: f.logo_url.trim(),
      favicon_url: f.favicon_url.trim(),
      primary_color: f.primary_color.trim(),
      secondary_color: f.secondary_color.trim(),
    }
    const res = await fetch('/api/v1/admin/tenant/branding', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() },
      body: JSON.stringify(payload),
    })
    const j = await res.json()
    setMsg(j.code === 0 ? '✅ 已保存，刷新页面即可看到效果' : '❌ ' + (j.message || '保存失败'))
  }
  const field = (k: keyof typeof f, label: string, ph: string) => (
    <div style={{ marginBottom: 12 }}>
      <label style={{ display: 'block', fontSize: 13, marginBottom: 4, color: '#475569' }}>{label}</label>
      <Input value={f[k]} onChange={(v) => setF({ ...f, [k]: v })} placeholder={ph} style={{ width: '100%' }} />
    </div>
  )
  return (
    <div className="bg-white rounded-lg shadow-sm p-6 max-w-2xl">
      <h3 style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>品牌定制（白标）</h3>
      <p style={{ fontSize: 13, color: '#6b7280', marginBottom: 16 }}>自定义访问域名、显示品牌名、Logo、主题色与外链。</p>
      {field('custom_domain', '自定义访问域名', '如 crm.your-company.com（留空则用平台子域名）')}
      {field('brand_name', '显示品牌名', '如 极石汽车')}
      {field('brand_link', '品牌外链', 'https://www.your-company.com')}
      {field('logo_url', 'Logo 图片地址', 'https://.../logo.png')}
      {field('favicon_url', 'Favicon', 'https://.../favicon.ico')}
      <div style={{ display: 'flex', gap: 12 }}>
        {field('primary_color', '主题主色', '#4f46e5')}
        {field('secondary_color', '主题辅色', '#6366f1')}
      </div>
      <Button theme="primary" onClick={save}>保存品牌配置</Button>
      <span style={{ marginLeft: 12, fontSize: 13, color: '#16a34a' }}>{msg}</span>
    </div>
  )
}

// ------------------------- 审计日志 -------------------------
// 审计日志 Tab：按动作/时间范围查询平台与租户关键操作记录
// 依赖 /api/v1/admin/audit-logs（critical 类动作高亮为 danger）
function AuditTab() {
  const [rows, setRows] = useState<any[]>([])
  const [action, setAction] = useState('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  async function load() {
    const q = new URLSearchParams()
    if (action) q.set('action', action)
    if (from) q.set('from', from)
    if (to) q.set('to', to)
    const r = await fetch('/api/v1/admin/audit-logs?' + q.toString(), { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    if (j.code === 0) setRows(j.data.list || [])
  }
  useEffect(() => { load() }, [])
  const cols = [
    { colKey: 'created_at', title: '时间', width: 160 },
    { colKey: 'action', title: '动作', width: 200, cell: (p: any) => <Tag theme={String(p.row.action).includes('critical') ? 'danger' : 'primary'}>{p.row.action}</Tag> },
    { colKey: 'username', title: '操作人', width: 120, cell: (p: any) => p.row.username || p.row.user_id },
    { colKey: 'resource', title: '资源', width: 200 },
    { colKey: 'detail', title: '详情', width: 280, ellipsis: true },
    { colKey: 'ip', title: 'IP', width: 120 },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex flex-wrap items-center gap-3">
        <select value={action} onChange={(e) => setAction((e.target as HTMLSelectElement).value)} className="px-3 py-2 border rounded-lg text-sm">
          <option value="">全部动作</option>
          <option value="order_manual_confirm_critical">我已付费(critical)</option>
          <option value="order_paid_confirm">订单确认发放</option>
          <option value="order_paid_mock">模拟支付</option>
          <option value="apikey_create">API Key签发</option>
          <option value="apikey_disable">API Key停用</option>
          <option value="super_admin_access">超管访问</option>
          <option value="tenant_signup">租户注册</option>
        </select>
        <input type="date" value={from} onChange={(e) => setFrom(e.target.value)} className="px-3 py-2 border rounded-lg text-sm" />
        <span className="text-gray-400 text-sm">至</span>
        <input type="date" value={to} onChange={(e) => setTo(e.target.value)} className="px-3 py-2 border rounded-lg text-sm" />
        <Button theme="primary" onClick={load}>查询</Button>
      </div>
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={rows} columns={cols} size="small" />
      </div>
    </div>
  )
}

// ------------------------- 开放平台 -------------------------
const PERM_LABELS: Record<string, string> = { 'customer.read': '客户读取', 'cdp.read': '画像读取', 'chat.read': '会话读取', 'all': '全部权限' }
// 开放平台 Tab：签发/停用/删除 API Key，明文 Key 仅展示一次需立即保存
// 依赖 /api/v1/admin/apikeys（Bearer sk_ 鉴权，停用即时生效）
function OpenApiTab() {
  const [keys, setKeys] = useState<any[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [name, setName] = useState('')
  const [perms, setPerms] = useState<string[]>(['customer.read'])
  async function load() {
    const r = await fetch('/api/v1/admin/apikeys', { headers: { Authorization: 'Bearer ' + getToken() } })
    const j = await r.json()
    if (j.code === 0) setKeys(j.data || [])
  }
  useEffect(() => { load() }, [])
  async function create() {
    if (!name || perms.length === 0) { MessagePlugin.warning('请填写名称并至少选择一项权限'); return }
    const r = await fetch('/api/v1/admin/apikeys', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() }, body: JSON.stringify({ name, perms }) })
    const j = await r.json()
    if (j.code !== 0) { MessagePlugin.error(j.message || '签发失败'); return }
    const key = j.data.key
    if (confirm('【请立即保存】明文 Key 仅显示这一次：\n\n' + key + '\n\n点击确定尝试复制到剪贴板')) {
      try { await navigator.clipboard.writeText(key) } catch {}
    }
    setName(''); setShowCreate(false); load()
  }
  const toggle = async (id: number, active: boolean) => { await fetch(`/api/v1/admin/apikeys/${id}/${active ? 'enable' : 'disable'}`, { method: 'POST', headers: { Authorization: 'Bearer ' + getToken() } }); load() }
  const del = async (id: number) => { if (!confirm('确认删除该 Key？')) return; await fetch(`/api/v1/admin/apikeys/${id}`, { method: 'DELETE', headers: { Authorization: 'Bearer ' + getToken() } }); load() }
  const cols = [
    { colKey: 'name', title: '名称', width: 160 },
    { colKey: 'key_prefix', title: 'Key前缀', width: 160, cell: (p: any) => <code className="bg-gray-100 px-2 py-0.5 rounded text-xs">{p.row.key_prefix}...</code> },
    { colKey: 'permissions', title: '权限', width: 200, cell: (p: any) => { try { return JSON.parse(p.row.permissions).map((x: string) => PERM_LABELS[x] || x).join('、') } catch { return p.row.permissions } } },
    { colKey: 'call_count', title: '调用次数', width: 100 },
    { colKey: 'last_used_at', title: '最近使用', width: 160, cell: (p: any) => p.row.last_used_at ? new Date(p.row.last_used_at).toLocaleString() : '从未使用' },
    { colKey: 'status', title: '状态', width: 100, cell: (p: any) => <Tag theme={p.row.is_active ? 'success' : 'danger'}>{p.row.is_active ? '启用' : '停用'}</Tag> },
    { colKey: 'op', title: '操作', width: 160, cell: (p: any) => (<><Button size="small" variant="outline" theme="warning" onClick={() => toggle(p.row.id, !p.row.is_active)}>{p.row.is_active ? '停用' : '启用'}</Button> <Button size="small" variant="outline" theme="danger" onClick={() => del(p.row.id)}>删除</Button></>) },
  ]
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-4 mb-4 flex items-center justify-between">
        <div style={{ fontSize: 13, color: '#6b7280' }}>开放 API 基地址：<code className="bg-gray-100 px-2 py-0.5 rounded">/openapi/v1</code>，鉴权：<code className="bg-gray-100 px-2 py-0.5 rounded">Bearer sk_xxx</code>。停用即时生效。</div>
        <Button theme="primary" onClick={() => setShowCreate(true)}>+ 签发新 Key</Button>
      </div>
      {showCreate && (
        <div className="bg-white rounded-lg shadow-sm p-4 mb-4">
          <Input value={name} onChange={(v) => setName(v)} placeholder="Key 名称（如：BI系统对接）" style={{ width: 280, marginRight: 12 }} />
          {['customer.read', 'cdp.read', 'chat.read'].map((p) => (
            <label key={p} style={{ marginRight: 12, fontSize: 13 }}>
              <input type="checkbox" checked={perms.includes(p)} onChange={(e) => { const ck = (e.target as HTMLInputElement).checked; setPerms(ck ? [...perms, p] : perms.filter((x) => x !== p)) }} /> {PERM_LABELS[p]}
            </label>
          ))}
          <Button theme="success" onClick={create}>确认签发</Button>
          <Button theme="default" variant="outline" onClick={() => setShowCreate(false)}>取消</Button>
        </div>
      )}
      <div className="bg-white rounded-lg shadow-sm overflow-hidden">
        <Table rowKey="id" data={keys} columns={cols} size="small" />
      </div>
    </div>
  )
}

// ------------------------- 用量看板 -------------------------
// 用量看板 Tab：展示近 30 天 AI 调用等用量汇总（/api/v1/admin/usage/summary）
function UsageTab() {
  const [data, setData] = useState<any>(null)
  useEffect(() => {
    fetch('/api/v1/admin/usage/summary?days=30', { headers: { Authorization: 'Bearer ' + getToken() } })
      .then((r) => r.json()).then((j) => { if (j.code === 0) setData(j.data) }).catch(() => {})
  }, [])
  if (!data) return <p style={{ color: '#9ca3af', padding: 40, textAlign: 'center' }}>加载中...</p>
  const entries = Object.entries(data).filter(([, v]) => typeof v === 'number' || typeof v === 'string')
  return (
    <div className="grid grid-cols-2 md:grid-cols-4 gap-4">
      {entries.map(([k, v]) => (
        <div key={k} className="bg-white rounded-lg shadow-sm p-4">
          <p style={{ fontSize: 12, color: '#9ca3af', marginBottom: 4 }}>{k}</p>
          <b style={{ fontSize: 20, color: '#4f46e5' }}>{String(v)}</b>
        </div>
      ))}
    </div>
  )
}

// ------------------------- 邀请推广 -------------------------
// 邀请推广 Tab：展示邀请码/链接/二维码与返利余额（/api/v1/admin/referral/info、/qrcode）
function ReferralTab() {
  const [info, setInfo] = useState<any>(null)
  const [qr, setQr] = useState('')
  useEffect(() => {
    fetch('/api/v1/admin/referral/info', { headers: { Authorization: 'Bearer ' + getToken() } }).then((r) => r.json()).then((j) => { if (j.code === 0) setInfo(j.data) }).catch(() => {})
    setQr('/api/v1/admin/referral/qrcode?size=280')
  }, [])
  if (!info) return <p style={{ color: '#9ca3af', padding: 40, textAlign: 'center' }}>加载中...</p>
  const r = info.referral || {}
  return (
    <div>
      <div className="bg-white rounded-lg shadow-sm p-6">
        <h3 style={{ fontSize: 16, fontWeight: 600 }}>邀请推广</h3>
        <p style={{ fontSize: 13, color: '#6b7280', marginTop: 8 }}>邀请码：<b>{r.invite_code}</b>　邀请链接：<a href={info.invite_url} target="_blank" rel="noreferrer">{info.invite_url}</a></p>
      </div>
      <div className="bg-white rounded-lg shadow-sm p-6 mt-4 grid grid-cols-2 md:grid-cols-4 gap-4">
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>已成功邀请</p><b style={{ fontSize: 20, color: '#4f46e5' }}>{r.invited_count} 人</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>已付费好友</p><b style={{ fontSize: 20, color: '#16a34a' }}>{r.paid_count} 人</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>免费体验桶余额</p><b style={{ fontSize: 20, color: '#f59e0b' }}>{r.free_token_balance}</b></div>
        <div><p style={{ fontSize: 12, color: '#9ca3af' }}>永久token余额</p><b style={{ fontSize: 20, color: '#10b981' }}>{r.token_balance}</b></div>
      </div>
      {qr && <div className="bg-white rounded-lg shadow-sm p-6 mt-4 inline-block"><img src={qr} alt="邀请二维码" /><p style={{ fontSize: 12, color: '#9ca3af', marginTop: 8 }}>扫码直达注册页（已自动携带邀请码）</p></div>}
    </div>
  )
}

const card: React.CSSProperties = { background: '#fff', borderRadius: 12, padding: 24, width: 320, boxShadow: '0 4px 24px rgba(0,0,0,.08)' }
const inp: React.CSSProperties = { width: '100%', padding: 10, border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14, marginBottom: 12 }
const header: React.CSSProperties = { background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '16px 24px', display: 'flex', alignItems: 'center', justifyContent: 'space-between' }
