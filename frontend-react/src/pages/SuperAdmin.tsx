// 平台超管后台页（SuperAdmin）：租户管理/商业包/模型成本/反馈/待确认收款/审计/协议/白标
import { useState, useEffect } from 'react'
import { confirmDialog } from '../lib/confirm'
import { Layout, Menu, Table, Tag, Button, Input, Select, MessagePlugin, Dialog } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { AUTH, apiJSON, logoutAndRedirect } from '../lib/api'
import type { TableRowData, CellProps } from '../types'
import { MonitorTab } from './super/MonitorTab'

// 布局解构（与租户后台一致：左侧正式菜单 + 右侧内容区）
const { Header, Aside, Content } = Layout
const { MenuItem, MenuGroup } = Menu

// FB_TYPES 反馈类型码 → 中文名（反馈列表列渲染）
const FB_TYPES: Record<string, string> = { ai_reply: 'AI话术', feature: '功能建议', rating: '满意度', other: '其他', client_error: '前端异常' }
// TYPE_NAMES 商业包类型码 → 中文名（包管理列表列渲染）
const TYPE_NAMES: Record<string, string> = { free: '试用', paid: '包月', increment: '增量买断' }

// 租户摘要行（超管租户列表）
type Tenant = { id: number; name: string; code: string; plan_name?: string; used_customers: number; max_customers?: number; status: string; created_at: string }
// AI 商业包模型（后台包管理）
type Pkg = { id: number; code: string; name: string; p_type: string; ai_calls: number; price_cents: number; duration_days?: number; enabled: boolean }
// 模型成本核算汇总（近 N 天）
type Cost = { days: number; total_calls: number; total_tokens: number; total_cost_yuan: number; models: { provider: string; model: string; calls: number; tokens: number; cost_yuan: number; cost_share_pct: number }[] }
// 用户反馈条目（顾问端提交）
type Fb = { id: number; tenant_id: number; tenant_name?: string; username?: string; target_type: string; content: string; context?: string; status: string; created_at: string }
// 待确认收款订单（已扫码付款）
type Pending = { id: number; order_no: string; tenant_id: number; tenant_name?: string; package_name?: string; amount_cents: number; created_at: string }
// 审计日志记录行
type Audit = { created_at: string; tenant_id: number; action: string; username?: string; resource?: string; detail?: string; ip?: string }
// 协议签署记录（用户/隐私）
type Ag = { id: number; username?: string; tenant_id: number; tenant_name?: string; agreement_type: string; version: string; status: string; signed_at: string }
type PackQualityRow = { key: string; tenant_id: number; pack_code: string; pack_version: string; template_id: string; sample_count: number; hook_rate: number; lead_rate: number; pending_human_rate: number; avg_intent_delta: number; avg_eval_score?: number | null }

// 平台超管后台：租户管理/商业包/模型成本/反馈/待确认收款/审计/协议/白标
// 依赖 /api/v1/super/* 系列接口；仅 role=super_admin 可访问（前端双重守卫 + 后端鉴权）
export default function SuperAdmin() {
  // 读取品牌配置（页脚展示品牌名）
  const brand = useBrand()
  // 当前超管用户名（来自 localStorage）
  const [me, setMe] = useState(localStorage.getItem('username') || '-')
  // 租户搜索关键字
  const [kw, setKw] = useState('')
  // 租户列表
  const [tenants, setTenants] = useState<Tenant[]>([])
  // AI 商业包列表
  const [pkgs, setPkgs] = useState<Pkg[]>([])
  // D9 包质量跨租户聚合
  const [packQuality, setPackQuality] = useState<PackQualityRow[]>([])
  const [packQualityDays, setPackQualityDays] = useState('30')
  // 模型成本核算汇总（近 N 天）
  const [cost, setCost] = useState<Cost | null>(null)
  // 用户反馈列表
  const [fbs, setFbs] = useState<Fb[]>([])
  // 反馈筛选状态（open/resolved/''）
  const [fbStatus, setFbStatus] = useState('open')
  // C6 前端异常与用户反馈共用列表，可按 target_type 分流
  const [fbTarget, setFbTarget] = useState('')
  // 待确认收款订单列表
  const [pendings, setPendings] = useState<Pending[]>([])
  // 审计日志列表
  const [audits, setAudits] = useState<Audit[]>([])
  // 协议签署记录列表
  const [ags, setAgs] = useState<Ag[]>([])
  // 协议类型筛选（user/privacy/''）
  const [agType, setAgType] = useState('')
  // 白标定制：当前选中的租户 ID
  const [bdTenant, setBdTenant] = useState<number | ''>('')
  // 白标表单字段（自定义域名/品牌名/Logo/主题色等；A5：补 custom_css/custom_js 编辑位）
  const [bd, setBd] = useState({ custom_domain: '', brand_name: '', brand_link: '', logo_url: '', favicon_url: '', primary_color: '', secondary_color: '', custom_css: '', custom_js: '' })
  // 白标保存结果提示
  const [bdMsg, setBdMsg] = useState('')
  // 当前菜单页（左侧正式菜单切换，不再纵向堆叠全部功能）
  const [view, setView] = useState('tenants')

  // 拉取租户列表
  async function load() {
    // G-18 分页信封(2026-09-11)：/super/tenants 返回 {list,total,page,page_size}，
    // 此前读 j.data(数组) 恒 undefined → 租户表全空。取 list，page_size 拉满(上限100)
    const j = await AUTH('/api/v1/super/tenants?page_size=100'); setTenants((j.data && j.data.list) || [])
  }
  // 拉取 AI 商业包列表
  async function loadPkgs() {
    const j = await AUTH('/api/v1/super/packages'); setPkgs(j.data || [])
  }
  // 拉取 D9 包质量视图
  async function loadPackQuality() {
    const j = await AUTH('/api/v1/super/packs/stats?days=' + encodeURIComponent(packQualityDays))
    const list = ((j.data && j.data.list) || []) as Omit<PackQualityRow, 'key'>[]
    setPackQuality(list.map((r) => ({ ...r, key: `${r.tenant_id}:${r.pack_code}:${r.pack_version}:${r.template_id}` })))
  }
  // 拉取近 30 天模型成本核算汇总
  async function loadCost() {
    const j = await AUTH('/api/v1/super/usage/cost?days=30'); if (j.code === 0) setCost(j.data)
  }
  // 按状态/类型拉取用户反馈列表
  async function loadFeedbacks() {
    const q = new URLSearchParams({ page_size: '50' })
    if (fbStatus) q.set('status', fbStatus)
    if (fbTarget) q.set('target_type', fbTarget)
    const j = await AUTH('/api/v1/super/feedbacks?' + q); setFbs((j.data && j.data.list) || [])
  }
  // 拉取待确认收款订单
  async function loadPending() {
    const j = await AUTH('/api/v1/super/orders/pending'); setPendings(j.data || [])
  }
  // 按筛选条件（动作/租户/时间区间）拉取审计日志
  async function loadAudit() {
    const q = new URLSearchParams()
    const a = (document.getElementById('aAction') as HTMLSelectElement)?.value
    const ti = (document.getElementById('aTenant') as HTMLInputElement)?.value
    const f = (document.getElementById('aFrom') as HTMLInputElement)?.value
    const t = (document.getElementById('aTo') as HTMLInputElement)?.value
    if (a) q.set('action', a); if (ti) q.set('tenant_id', ti); if (f) q.set('from', f); if (t) q.set('to', t)
    const j = await AUTH('/api/v1/super/audit-logs?' + q); setAudits((j.data && j.data.list) || [])
  }
  // 按类型拉取协议签署记录
  async function loadAgreements() {
    const j = await AUTH('/api/v1/super/agreements' + (agType ? '?type=' + agType : '')); setAgs((j.data && j.data.list) || [])
  }
  // 读取选中租户的白标配置（品牌名若为平台默认值则清空待填）
  async function loadBdTenant() {
    if (bdTenant === '') return
    const j = await AUTH('/api/v1/super/tenants/' + bdTenant + '/branding'); const b = j.data || {}
    setBd({ custom_domain: b.custom_domain || '', brand_name: (b.brand_name && b.brand_name !== '跨山 LexCross') ? b.brand_name : '', brand_link: b.brand_link || '', logo_url: b.logo_url || '', favicon_url: b.favicon_url || '', primary_color: b.primary_color || '', secondary_color: b.secondary_color || '', custom_css: b.custom_css || '', custom_js: b.custom_js || '' })
  }
  // 保存选中租户的白标配置
  async function saveBd() {
    if (bdTenant === '') return
    setBdMsg('保存中...')
    const j = await AUTH('/api/v1/super/tenants/' + bdTenant + '/branding', { method: 'PUT', body: { ...bd, custom_domain: bd.custom_domain.trim() || null, brand_name: bd.brand_name.trim() } }); setBdMsg(j.code === 0 ? '✅ 已保存' : '❌ ' + (j.message || '失败'))
  }

  // 超管守卫：非 super_admin 直接跳登录；加载各模块并每 30s 刷新待确认收款
  useEffect(() => {
    if (localStorage.getItem('role') !== 'super_admin') { location.href = '/login'; return }
    load(); loadPkgs(); loadPackQuality(); loadCost(); loadPending(); loadFeedbacks(); loadAudit(); loadAgreements()
    const t = setInterval(loadPending, 30000)
    return () => clearInterval(t)
  }, [])
  useEffect(() => { loadFeedbacks() }, [fbStatus, fbTarget])
  useEffect(() => { loadAgreements() }, [agType])
  useEffect(() => { loadPackQuality() }, [packQualityDays])

  if (localStorage.getItem('role') !== 'super_admin') return null

  // 按关键字过滤租户（名称或标识模糊匹配）
  const filtered = tenants.filter((t) => !kw || t.name.includes(kw) || t.code.includes(kw))
  // 把空值安全转成字符串，避免表格渲染出 undefined
  const esc = (s?: string) => (s == null ? '' : String(s))

  // 给租户发放一次性试用额度（幂等，已发放过的后端拒绝）
  async function grantTrial(id: number) {
    if (!(await confirmDialog('将为该租户发放一次性试用额度（幂等，已发放过的租户会被拒绝）。确认？', '发放试用额度'))) return
    const j = await AUTH(`/api/v1/super/tenants/${id}/grant-trial`, { method: 'POST' })
    MessagePlugin[j?.code === 0 ? 'success' : 'warning'](j?.message || (j?.code === 0 ? '已发放' : '发放失败'))
  }
  // 停用/恢复租户（带二次确认），成功后刷新租户列表
  async function setStatus(id: number, st: string) {
    if (!(await confirmDialog('确认将租户 #' + id + ' 置为 ' + st + ' ?'))) return
    await AUTH(`/api/v1/super/tenants/${id}/status`, { method: 'PUT', body: { status: st } })
    load()
  }
  // 上架/下架商业包
  async function togglePkg(id: number, enabled: boolean) {
    const j = await AUTH(`/api/v1/super/packages/${id}`, { method: 'PUT', body: { enabled } }); if (j.code !== 0) MessagePlugin.error(j.message || '操作失败'); loadPkgs()
  }
  // 新增商业包（读取弹窗表单字段后提交，成功后清空并刷新）
  async function createPkg() {
    const code = (document.getElementById('pCode') as HTMLInputElement).value.trim()
    const name = (document.getElementById('pName') as HTMLInputElement).value.trim()
    const p_type = (document.getElementById('pType') as HTMLSelectElement).value
    const ai_calls = parseInt((document.getElementById('pCalls') as HTMLInputElement).value) || 0
    const price_cents = parseInt((document.getElementById('pPrice') as HTMLInputElement).value) || 0
    let duration_days = parseInt((document.getElementById('pDays') as HTMLInputElement).value) || 0
    if (!code || !name) { MessagePlugin.warning('标识和名称必填'); return }
    if (p_type === 'free') duration_days = 0
    const j = await AUTH('/api/v1/super/packages', { method: 'POST', body: { code, name, p_type, ai_calls, price_cents, duration_days } }); if (j.code !== 0) { MessagePlugin.error(j.message || '创建失败'); return }
    ;['pCode', 'pName', 'pCalls', 'pPrice', 'pDays'].forEach((id) => { const el = document.getElementById(id) as HTMLInputElement; if (el) el.value = '' })
    loadPkgs()
  }
  // 人工确认收款：核实到账后发放权益，接口幂等（重复确认自动跳过）
  async function confirmOrder(id: number) {
    if (!(await confirmDialog('确认已收到该笔款项？确认后立即发放对应权益。'))) return
    const j = await AUTH(`/api/v1/super/orders/${id}/confirm`, { method: 'POST' }); MessagePlugin.info(j.message || '操作完成'); loadPending()
  }
  // 标记反馈为已处理（可填处理备注）
  async function resolveFb(id: number) {
    const note = prompt('处理备注（可空）：'); if (note === null) return
    await AUTH('/api/v1/super/feedbacks/resolve', { method: 'POST', body: { id, note } })
    loadFeedbacks()
  }

  // 租户列表列定义（含状态标签、用量、停用/恢复与发试用操作）
  const tenantCols = [
    { colKey: 'id', title: 'ID', width: 60 },
    { colKey: 'name', title: '企业名称', width: 160 },
    { colKey: 'code', title: '标识', width: 120 },
    { colKey: 'plan_name', title: '套餐', width: 100, cell: (p: CellProps) => p.row.plan_name || '-' },
    { colKey: 'used', title: '客户用量', width: 100, cell: (p: CellProps) => `${p.row.used_customers}/${p.row.max_customers || '∞'}` },
    { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme={p.row.status === 'suspended' ? 'danger' : 'success'}>{p.row.status}</Tag> },
    { colKey: 'created_at', title: '开通日期', width: 120 },
    { colKey: 'op', title: '操作', width: 190, cell: (p: CellProps) => (
      <div style={{ display: 'flex', gap: 6 }}>
        {p.row.status === 'suspended'
          ? <Button size="small" theme="success" onClick={() => setStatus(p.row.id, 'active')}>恢复</Button>
          : <Button size="small" theme="danger" variant="outline" onClick={() => setStatus(p.row.id, 'suspended')}>停用</Button>}
        <Button size="small" variant="outline" onClick={() => grantTrial(p.row.id)}>发试用</Button>
      </div>
    ) },
  ]
  // AI 商业包列定义（类型/售价/有效期/上下架切换）
  const pkgCols = [
    { colKey: 'id', title: 'ID', width: 50 }, { colKey: 'code', title: '标识', width: 120 }, { colKey: 'name', title: '名称', width: 120 },
    { colKey: 'p_type', title: '类型', width: 90, cell: (p: CellProps) => TYPE_NAMES[p.row.p_type] || p.row.p_type },
    { colKey: 'ai_calls', title: 'AI次数', width: 80 },
    { colKey: 'price_cents', title: '售价', width: 90, cell: (p: CellProps) => '¥' + (p.row.price_cents / 100).toFixed(p.row.price_cents % 100 ? 2 : 0) },
    { colKey: 'duration_days', title: '有效期', width: 90, cell: (p: CellProps) => p.row.duration_days ? p.row.duration_days + '天' : '—' },
    { colKey: 'enabled', title: '状态', width: 80, cell: (p: CellProps) => <Tag theme={p.row.enabled ? 'success' : 'default'}>{p.row.enabled ? '上架' : '下架'}</Tag> },
    { colKey: 'op', title: '操作', width: 90, cell: (p: CellProps) => <Button size="small" theme={p.row.enabled ? 'danger' : 'success'} variant="outline" onClick={() => togglePkg(p.row.id, !p.row.enabled)}>{p.row.enabled ? '下架' : '上架'}</Button> },
  ]
  // 用户反馈列定义（租户/提交人/类型/意见/标记处理）
  const fbCols = [
    { colKey: 'created_at', title: '时间', width: 140, cell: (p: CellProps) => (p.row.created_at || '').replace('T', ' ').slice(0, 16) },
    { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `#${p.row.tenant_id} ${esc(p.row.tenant_name)}` },
    { colKey: 'username', title: '提交人', width: 100, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'target_type', title: '类型', width: 90, cell: (p: CellProps) => FB_TYPES[p.row.target_type] || p.row.target_type },
    { colKey: 'content', title: '意见', width: 220, ellipsis: true },
    { colKey: 'op', title: '操作', width: 100, cell: (p: CellProps) => p.row.status === 'open' ? <Button size="small" theme="success" onClick={() => resolveFb(p.row.id)}>标记处理</Button> : <span style={{ fontSize: 12, color: '#718096' }}>已处理</span> },
  ]
  // 待确认收款订单列定义（含确认到账发放按钮）
  const pendingCols = [
    { colKey: 'order_no', title: '订单号', width: 160 }, { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'package_name', title: '商业包', width: 140 }, { colKey: 'amount_cents', title: '金额', width: 100, cell: (p: CellProps) => '¥' + (p.row.amount_cents / 100).toFixed(2) },
    { colKey: 'created_at', title: '提交时间', width: 160 }, { colKey: 'op', title: '操作', width: 140, cell: (p: CellProps) => <Button size="small" theme="success" onClick={() => confirmOrder(p.row.id)}>确认到账并发放</Button> },
  ]
  // 平台审计日志列定义（动作/critical 高亮/操作人/IP）
  const auditCols = [
    { colKey: 'created_at', title: '时间', width: 150 }, { colKey: 'tenant_id', title: '租户', width: 70, cell: (p: CellProps) => '#' + p.row.tenant_id },
    { colKey: 'action', title: '动作', width: 200, cell: (p: CellProps) => <Tag theme={p.row.action.includes('critical') ? 'danger' : 'primary'}>{p.row.action}</Tag> },
    { colKey: 'username', title: '操作人', width: 100, cell: (p: CellProps) => p.row.username || p.row.user_id }, { colKey: 'resource', title: '资源', width: 160 },
    { colKey: 'detail', title: '详情', width: 260, ellipsis: true }, { colKey: 'ip', title: 'IP', width: 120 },
  ]
  // 协议签署记录列定义（用户/租户/协议类型/版本/状态）
  const agCols = [
    { colKey: 'id', title: 'ID', width: 60 }, { colKey: 'username', title: '用户', width: 120, cell: (p: CellProps) => esc(p.row.username) },
    { colKey: 'tenant', title: '租户', width: 160, cell: (p: CellProps) => `${esc(p.row.tenant_name)} (#${p.row.tenant_id})` },
    { colKey: 'agreement_type', title: '协议类型', width: 100, cell: (p: CellProps) => ({ user: '用户协议', privacy: '隐私政策' } as Record<string, string>)[p.row.agreement_type] || p.row.agreement_type },
    { colKey: 'version', title: '版本', width: 80 }, { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => <Tag theme="success">{p.row.status}</Tag> },
    { colKey: 'signed_at', title: '签署时间', width: 160, cell: (p: CellProps) => new Date(p.row.signed_at).toLocaleString() },
  ]

  return (
    <Layout style={{ minHeight: '100vh', background: '#f5f7fa' }}>
      <Header style={{ background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '12px 16px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ fontSize: 18, fontWeight: 700, color: '#1f2937' }}>平台超管中心</span>
          <span style={{ fontSize: 12, color: '#9ca3af', background: '#f3f4f6', padding: '2px 8px', borderRadius: 10 }}>super_admin</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <span style={{ fontSize: 13, color: '#6b7280' }}>{me}</span>
          <a href="/admin" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>租户后台</a>
          <Button size="small" variant="outline" onClick={logoutAndRedirect}>退出</Button>
        </div>
      </Header>
      <Layout>
        <Aside width="200" style={{ background: '#fff', borderRight: '1px solid #e5e7eb' }}>
          <Menu value={view} onChange={(v) => setView(v as string)} style={{ borderRight: 'none' }}>
            <MenuGroup title="平台管理">
              <MenuItem value="tenants">租户管理</MenuItem>
              <MenuItem value="packages">AI 商业包</MenuItem>
              <MenuItem value="pack_quality">包质量</MenuItem>
              <MenuItem value="cost">模型成本核算</MenuItem>
              <MenuItem value="feedbacks">用户反馈</MenuItem>
              <MenuItem value="pending">待确认收款</MenuItem>
              <MenuItem value="audit">审计日志</MenuItem>
              <MenuItem value="agreements">协议签署</MenuItem>
              <MenuItem value="branding">品牌定制（白标）</MenuItem>
              <MenuItem value="monitor">平台健康监控</MenuItem>
            </MenuGroup>
          </Menu>
        </Aside>
        <Content style={{ padding: '20px 24px', minWidth: 0 }}>
          <div style={{ maxWidth: 1100, margin: '0 auto' }}>
            {view === 'monitor' && <MonitorTab />}
            {view === 'tenants' && (
              <>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 14, gap: 10, flexWrap: 'wrap' }}>
                  <h2 style={{ margin: 0, fontSize: 18 }}>租户管理</h2>
                  <Input value={kw} onChange={(v) => setKw(v)} placeholder="按名称/标识搜索" style={{ maxWidth: 280 }} />
                </div>
                <div className="bg-white rounded-lg shadow-sm overflow-hidden"><Table rowKey="id" data={filtered} columns={tenantCols} size="small" /></div>
              </>
            )}
            {view === 'packages' && (
              <Section title="AI 商业包管理" desc="公开定价页与租户订阅入口实时读取；已有订单引用的包删除时自动转下架">
                <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
                  <input id="pCode" placeholder="标识(如 pro_8000)" style={{ width: 130, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
                  <input id="pName" placeholder="名称" style={{ width: 110, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
                  <select id="pType" defaultValue="paid" style={{ width: 120, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }}>
                    <option value="paid">包月</option><option value="increment">增量买断</option><option value="free">试用</option>
                  </select>
                  <input id="pCalls" placeholder="AI次数" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
                  <input id="pPrice" placeholder="售价分" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
                  <input id="pDays" placeholder="有效天数" style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
                  <Button theme="primary" onClick={createPkg}>新增</Button>
                </div>
                <Table rowKey="id" data={pkgs} columns={pkgCols} size="small" />
              </Section>
            )}
            {view === 'pack_quality' && (
              <Section title="包质量视图" desc="跨租户查看行业包/企业包模板效果；样本 <50 仅作趋势参考，不直接作为包迭代结论。">
                <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
                  <Select value={packQualityDays} onChange={(v) => setPackQualityDays(String(v))} options={[{ label: '近7天', value: '7' }, { label: '近30天', value: '30' }, { label: '近90天', value: '90' }]} style={{ width: 130 }} />
                  <Button theme="primary" variant="outline" onClick={loadPackQuality}>刷新</Button>
                </div>
                <Table rowKey="key" data={packQuality as TableRowData[]} columns={[
                  { colKey: 'tenant_id', title: '租户', width: 90 },
                  { colKey: 'pack_code', title: '包', width: 150 },
                  { colKey: 'pack_version', title: '版本', width: 110, cell: (p: CellProps) => <Tag>{p.row.pack_version || '-'}</Tag> },
                  { colKey: 'template_id', title: '模板', width: 220 },
                  { colKey: 'sample_count', title: '样本', width: 100, cell: (p: CellProps) => Number(p.row.sample_count || 0) < 50 ? <Tag theme="warning">积累中 {p.row.sample_count}</Tag> : p.row.sample_count },
                  { colKey: 'hook_rate', title: '接钩率', width: 100, cell: (p: CellProps) => (Number(p.row.hook_rate || 0) * 100).toFixed(1) + '%' },
                  { colKey: 'lead_rate', title: '留资率', width: 100, cell: (p: CellProps) => (Number(p.row.lead_rate || 0) * 100).toFixed(1) + '%' },
                  { colKey: 'pending_human_rate', title: '待人工', width: 100, cell: (p: CellProps) => (Number(p.row.pending_human_rate || 0) * 100).toFixed(1) + '%' },
                  { colKey: 'avg_eval_score', title: '质量分', width: 90, cell: (p: CellProps) => typeof p.row.avg_eval_score === 'number' ? p.row.avg_eval_score.toFixed(0) : '-' },
                ]} size="small" empty="暂无包质量数据" />
              </Section>
            )}
            {view === 'cost' && (
              <Section title={<>模型成本核算 <span style={{ fontSize: 13, color: '#718096' }}>{cost ? `近${cost.days}天 · 共${cost.total_calls}次 / ${cost.total_tokens} tokens / ¥${cost.total_cost_yuan}` : ''}</span></>}>
                <Table rowKey="model" data={cost?.models || []} columns={[
                  { colKey: 'provider', title: '供应商', width: 120 }, { colKey: 'model', title: '模型', width: 160 }, { colKey: 'calls', title: '调用次数', width: 100 },
                  { colKey: 'tokens', title: 'Tokens', width: 120 }, { colKey: 'cost_yuan', title: '成本(¥)', width: 110, cell: (p: CellProps) => '¥' + p.row.cost_yuan.toFixed(4) },
                  { colKey: 'cost_share_pct', title: '占比', width: 140, cell: (p: CellProps) => <div style={{ background: '#edf2f7', borderRadius: 4, overflow: 'hidden', width: 90, height: 8 }}><div style={{ background: '#4f46e5', height: '100%', width: Math.min(100, p.row.cost_share_pct) + '%' }} /></div> },
                ]} size="small" empty="暂无用量数据（真实AI对话后生成）" />
              </Section>
            )}
            {view === 'feedbacks' && (
              <Section title="用户反馈" desc="顾问端AI回复气泡「反馈」提交，C6 前端异常也会进入这里；新反馈推群机器人">
                <div style={{ marginBottom: 12, display: 'flex', gap: 10, flexWrap: 'wrap' }}>
                  <Select value={fbStatus} onChange={(v) => setFbStatus(v as string)} options={[{ label: '待处理', value: 'open' }, { label: '已处理', value: 'resolved' }, { label: '全部', value: '' }]} style={{ width: 160 }} />
                  <Select value={fbTarget} onChange={(v) => setFbTarget(v as string)} options={[{ label: '全部类型', value: '' }, { label: 'AI话术', value: 'ai_reply' }, { label: '功能建议', value: 'feature' }, { label: '满意度', value: 'rating' }, { label: '前端异常', value: 'client_error' }, { label: '其他', value: 'other' }]} style={{ width: 160 }} />
                </div>
                <Table rowKey="id" data={fbs} columns={fbCols} size="small" empty="暂无反馈" />
              </Section>
            )}
            {view === 'pending' && (
              <Section title={<>待确认收款 <span style={{ fontSize: 13, color: '#975a16' }}>{pendings.length ? `(${pendings.length}笔待核实)` : ''}</span></>} desc="租户已扫码付款并点击「我已付费」，请核对账户到账后确认发放权益（重复确认自动幂等跳过）">
                <Table rowKey="id" data={pendings} columns={pendingCols} size="small" empty="暂无待确认收款" />
              </Section>
            )}
            {view === 'audit' && (
              <Section title="审计日志">
                <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
                  <select id="aAction" defaultValue="" style={sel}><option value="">全部动作</option><option value="order_manual_confirm_critical">我已付费(critical)</option><option value="order_paid_confirm">订单确认发放</option><option value="order_paid_mock">模拟支付</option><option value="super_admin_access">超管访问</option><option value="super_tenant_status">租户封禁/恢复</option><option value="tenant_signup">租户注册</option><option value="apikey_create">API Key签发</option></select>
                  <input id="aTenant" type="number" placeholder="按租户ID过滤" style={{ ...sel, width: 130 }} />
                  <input id="aFrom" type="date" style={sel} /> <span style={{ color: '#718096' }}>至</span> <input id="aTo" type="date" style={sel} />
                  <Button theme="primary" onClick={loadAudit}>查询</Button>
                </div>
                <Table rowKey="id" data={audits} columns={auditCols} size="small" empty="暂无记录" />
              </Section>
            )}
            {view === 'agreements' && (
              <Section title="协议签署" desc="用户注册即视为同意《用户协议》《隐私政策》">
                <div style={{ marginBottom: 12 }}><Select value={agType} onChange={(v) => setAgType(v as string)} options={[{ label: '全部协议', value: '' }, { label: '用户协议', value: 'user' }, { label: '隐私政策', value: 'privacy' }]} style={{ width: 160 }} /></div>
                <Table rowKey="id" data={ags} columns={agCols} size="small" empty="暂无签署记录" />
              </Section>
            )}
            {view === 'branding' && (
              <Section title="品牌定制（白标）" desc="为任意租户设置自定义访问域名、显示品牌名、Logo、主题色与外链。保存后按自定义域名访问即生效。">
                <div style={{ display: 'flex', gap: 10, marginBottom: 12, alignItems: 'center' }}>
                  <Select value={bdTenant} onChange={(v) => setBdTenant(v as number)} options={tenants.map((t) => ({ label: `#${t.id} ${t.name}（${t.code}）`, value: t.id }))} placeholder="选择租户" style={{ width: 280 }} />
                  <Button theme="primary" onClick={loadBdTenant}>读取</Button>
                </div>
                <div style={{ maxWidth: 640 }} className="grid grid-cols-1 gap-3">
                  <Field label="自定义访问域名" v={bd.custom_domain} set={(x) => setBd({ ...bd, custom_domain: x })} ph="如 crm.your-company.com（留空用平台子域名）" />
                  <Field label="显示品牌名" v={bd.brand_name} set={(x) => setBd({ ...bd, brand_name: x })} ph="如 极石汽车" />
                  <Field label="品牌外链" v={bd.brand_link} set={(x) => setBd({ ...bd, brand_link: x })} ph="https://www.your-company.com" />
                  <Field label="Logo 图片地址" v={bd.logo_url} set={(x) => setBd({ ...bd, logo_url: x })} ph="https://.../logo.png" />
                  <Field label="Favicon" v={bd.favicon_url} set={(x) => setBd({ ...bd, favicon_url: x })} ph="https://.../favicon.ico" />
                  <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
                    <Field label="主题主色" v={bd.primary_color} set={(x) => setBd({ ...bd, primary_color: x })} ph="#4f46e5" />
                    <Field label="主题辅色" v={bd.secondary_color} set={(x) => setBd({ ...bd, secondary_color: x })} ph="#6366f1" />
                  </div>
                  {/* A5 修复(2026-09-14)：custom_css/custom_js 有库列有注入执行链但全站无编辑入口；
                      A3：旧表单缺这两字段时后端按零值覆盖，超管每次保存即抹掉既有 CSS/JS——后端已指针化，此处补编辑位 */}
                  <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>自定义 CSS（注入该租户全部页面）</label>
                    <textarea value={bd.custom_css} onChange={(e) => setBd({ ...bd, custom_css: e.target.value })} placeholder="如 .tdesign-header { display:none }" style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
                  <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>自定义 JS（仅超管可改；空=不改）</label>
                    <textarea value={bd.custom_js} onChange={(e) => setBd({ ...bd, custom_js: e.target.value })} placeholder="平台侧注入脚本（信任面：超管专属）" style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
                    <Button theme="primary" onClick={saveBd}>保存品牌配置</Button>
                    <span style={{ fontSize: 13, color: bdMsg.includes('✅') ? '#16a34a' : '#dc2626' }}>{bdMsg}</span>
                  </div>
                </div>
              </Section>
            )}
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}

// 后台通用分区容器：标题 + 可选说明 + 内容
function Section({ title, desc, children }: { title: React.ReactNode; desc?: string; children: React.ReactNode }) {
  return (
    <div style={{ marginBottom: 30 }}>
      <h2 style={{ fontSize: 18, marginBottom: 4, marginTop: 10 }}>{title}</h2>
      {desc && <p style={{ color: '#718096', fontSize: 13, marginBottom: 12 }}>{desc}</p>}
      {children}
    </div>
  )
}
// 白标配置的单行输入字段：标签 + 输入框
function Field({ label, v, set, ph }: { label: string; v: string; set: (x: string) => void; ph?: string }) {
  return <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>{label}</label><Input value={v} onChange={(x) => set(x)} placeholder={ph} style={{ width: '100%' }} /></div>
}
// 审计日志筛选控件（下拉/输入框）统一样式
const sel: React.CSSProperties = { padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }
