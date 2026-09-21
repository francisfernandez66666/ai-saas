// 平台超管后台页（SuperAdmin）：租户管理/商业包/模型成本/反馈/待确认收款/审计/协议/白标
// C2 拆分(2026-09-21)：原单文件 40,276 字节（14 个菜单视图挤在一个组件里），
// 现按视图拆到 ./super/*Tab，本文件只保留**状态、数据加载与写操作编排** + 布局骨架（顶栏/左栏菜单/内容区），
// 各视图的列定义与表单随视图下沉，行为与交互逐字保持不变。
import { useState, useEffect, useRef } from 'react'
import { confirmDialog } from '../lib/confirm'
import { Layout, Menu, Button, MessagePlugin } from 'tdesign-react'
import { useIsMobile } from '../hooks/useMedia'
import { AUTH, logoutAndRedirect } from '../lib/api'
import { MonitorTab } from './super/MonitorTab'
// §八-6 平台运营 UI 批：发票受理 + 行业包上架/共享管理（独立 Tab 组件，按需挂载）
import { InvoiceTab } from './super/InvoiceTab'
import { RefundTab } from './super/RefundTab'
import { PackTab } from './super/PackTab'
// P1-9 零UI补齐批（2026-09-20）：素材审核——/super/materials 三端点此前前端零消费者
import { MaterialsTab } from './super/MaterialsTab'
// C2 拆分新增的 8 个视图组件 + 共享菜单数据源
import { SUPER_MENUS, type Ag, type Audit, type BdForm, type Cost, type Fb, type PackQualityRow, type Pending, type Pkg, type PlanOpt, type Tenant } from './super/shared'
import TenantsTab from './super/TenantsTab'
import PackagesTab from './super/PackagesTab'
import PackQualityTab from './super/PackQualityTab'
import CostTab from './super/CostTab'
import FeedbacksTab from './super/FeedbacksTab'
import PendingTab from './super/PendingTab'
import AuditLogsTab from './super/AuditLogsTab'
import AgreementsTab from './super/AgreementsTab'
import BrandingTab from './super/BrandingTab'

// 布局解构（与租户后台一致：左侧正式菜单 + 右侧内容区）
const { Header, Aside, Content } = Layout
const { MenuItem, MenuGroup } = Menu

// 平台超管后台：租户管理/商业包/模型成本/反馈/待确认收款/审计/协议/白标
// 依赖 /api/v1/super/* 系列接口；仅 role=super_admin 可访问（前端双重守卫 + 后端鉴权）
export default function SuperAdmin() {
  // 当前超管用户名（来自 localStorage）
  const [me, setMe] = useState(localStorage.getItem('username') || '-')
  // P1-10(2026-09-20)：窄屏折叠左侧 Aside 为顶栏下拉
  const isMobile = useIsMobile(900)
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
  // P2-13(2026-09-20 批三)：超管审计同租户 AuditTab——后端默认 20 条封顶，接 page/page_size 并给翻页条
  const [auditPage, setAuditPage] = useState(1)
  const [auditTotal, setAuditTotal] = useState(0)
  // 协议签署记录列表
  const [ags, setAgs] = useState<Ag[]>([])
  // 协议类型筛选（user/privacy/''）
  const [agType, setAgType] = useState('')
  // 白标定制：当前选中的租户 ID
  const [bdTenant, setBdTenant] = useState<number | ''>('')
  // 白标表单字段（自定义域名/品牌名/Logo/主题色等；A5：补 custom_css/custom_js 编辑位）
  const [bd, setBd] = useState<BdForm>({ custom_domain: '', brand_name: '', brand_link: '', logo_url: '', favicon_url: '', primary_color: '', secondary_color: '', custom_css: '', custom_js: '' })
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
  // 按筛选条件（动作/租户/时间区间）拉取审计日志（P2-13：带分页参数，50/页）
  async function loadAudit(p = auditPage) {
    const q = new URLSearchParams({ page: String(p), page_size: '50' })
    const a = (document.getElementById('aAction') as HTMLSelectElement)?.value
    const ti = (document.getElementById('aTenant') as HTMLInputElement)?.value
    const f = (document.getElementById('aFrom') as HTMLInputElement)?.value
    const t = (document.getElementById('aTo') as HTMLInputElement)?.value
    if (a) q.set('action', a); if (ti) q.set('tenant_id', ti); if (f) q.set('from', f); if (t) q.set('to', t)
    const j = await AUTH('/api/v1/super/audit-logs?' + q)
    if (j?.code === 0) { setAudits(j.data.list || []); setAuditTotal(Number(j.data.total) || 0) }
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
  // P2-13：审计翻页重拉（首挂载由总装载 effect 负责，此处跳过一次防双请）
  const auditFirstRun = useRef(true)
  useEffect(() => {
    if (auditFirstRun.current) { auditFirstRun.current = false; return }
    void loadAudit(auditPage)
  }, [auditPage])
  useEffect(() => { loadAgreements() }, [agType])
  useEffect(() => { loadPackQuality() }, [packQualityDays])

  // P2-5 修复(2026-09-19 审计批一)：换套餐弹窗两枚 state 由此前放在角色 early-return
  // 之后（rules-of-hooks：Hook 不得条件调用）上移至全部 hook 区——非超管渲染 null 时
  // hooks 仍按固定顺序执行，行为不变。
  const [planDlg, setPlanDlg] = useState<Tenant | null>(null)
  const [planOpts, setPlanOpts] = useState<PlanOpt[]>([])

  if (localStorage.getItem('role') !== 'super_admin') return null

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
  // 换套餐弹窗（商业缺口批 2026-09-16）：席位/客户/部门/AI 配额随套餐快照同步
  // （planDlg/planOpts 两枚 state 已上移至 hook 区，P2-5 修复）
  async function openPlan(t: Tenant) {
    const j = await AUTH('/api/v1/super/plans')
    setPlanOpts((j?.data && j.data.items) || [])
    setPlanDlg(t)
  }
  async function doChangePlan() {
    const el = document.getElementById('spPlan') as HTMLSelectElement | null
    if (!planDlg || !el || !el.value) return
    const j = await AUTH(`/api/v1/super/tenants/${planDlg.id}/plan`, { method: 'PUT', body: { plan_id: parseInt(el.value) || 0 } })
    MessagePlugin[j?.code === 0 ? 'success' : 'error'](j?.message || (j?.code === 0 ? '套餐已更新' : '操作失败'))
    if (j?.code === 0) { setPlanDlg(null); load() }
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
        {isMobile ? (
          /* P1-10(2026-09-20)：窄屏（≤900px）折叠左栏为顶栏下拉，与桌面菜单同一数据源 SUPER_MENUS */
          <div style={{ width: '100%', background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '8px 12px' }}>
            <select value={view} onChange={(e) => setView((e.target as HTMLSelectElement).value)} aria-label="平台管理菜单" style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid #e2e8f0', fontSize: 14, background: '#fff' }}>
              <optgroup label="平台管理">
                {SUPER_MENUS.map((m) => <option key={m.k} value={m.k}>{m.label}</option>)}
              </optgroup>
            </select>
          </div>
        ) : (
        <Aside width="200" style={{ background: '#fff', borderRight: '1px solid #e5e7eb' }}>
          <Menu value={view} onChange={(v) => setView(v as string)} style={{ borderRight: 'none' }}>
            <MenuGroup title="平台管理">
              {SUPER_MENUS.map((m) => (
                <MenuItem key={m.k} value={m.k}>{m.label}</MenuItem>
              ))}
            </MenuGroup>
          </Menu>
        </Aside>
        )}
        <Content style={{ padding: isMobile ? 12 : '20px 24px', minWidth: 0 }}>
          <div style={{ maxWidth: 1100, margin: '0 auto' }}>
            {view === 'monitor' && <MonitorTab />}
            {/* §八-6 平台运营 UI 批：两个新 Tab 仅在选中时挂载（各自内部懒加载接口） */}
            {view === 'invoices' && <InvoiceTab />}
            {view === 'refunds' && <RefundTab />}
            {view === 'industry_packs' && <PackTab />}
            {view === 'materials' && <MaterialsTab />}
            {view === 'tenants' && (
              <TenantsTab
                tenants={tenants} kw={kw} onKw={setKw}
                onGrant={grantTrial} onSetStatus={setStatus} onOpenPlan={openPlan}
                planDlg={planDlg} planOpts={planOpts} onClosePlan={() => setPlanDlg(null)} onConfirmPlan={doChangePlan}
              />
            )}
            {view === 'packages' && <PackagesTab pkgs={pkgs} onToggle={togglePkg} onCreate={createPkg} />}
            {view === 'pack_quality' && (
              <PackQualityTab rows={packQuality} days={packQualityDays} onDays={setPackQualityDays} onRefresh={loadPackQuality} />
            )}
            {view === 'cost' && <CostTab cost={cost} />}
            {view === 'feedbacks' && (
              <FeedbacksTab fbs={fbs} status={fbStatus} onStatus={setFbStatus} target={fbTarget} onTarget={setFbTarget} onResolve={resolveFb} />
            )}
            {view === 'pending' && <PendingTab pendings={pendings} onConfirm={confirmOrder} />}
            {view === 'audit' && (
              <AuditLogsTab
                audits={audits} page={auditPage} total={auditTotal} onPage={setAuditPage}
                onQuery={() => { if (auditPage === 1) void loadAudit(1); else setAuditPage(1) }}
              />
            )}
            {view === 'agreements' && <AgreementsTab ags={ags} type={agType} onType={setAgType} />}
            {view === 'branding' && (
              <BrandingTab
                tenants={tenants} tenant={bdTenant} onTenant={setBdTenant}
                bd={bd} onBd={setBd} msg={bdMsg} onLoad={loadBdTenant} onSave={saveBd}
              />
            )}
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}
