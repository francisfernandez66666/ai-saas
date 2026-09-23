// 后台管理页：租户管理员登录后按分类 Tab 维护配置、客户、知识库、标签、通道、审计等运营后台。
// F1 起各业务 Tab 拆至 src/pages/admin/*，本文件保留登录、菜单和配置编辑动作。
import { Suspense, lazy, useEffect, useState } from 'react'
import { confirmDialog } from '../lib/confirm'
import { Button, Layout, Menu, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { useIsMobile } from '../hooks/useMedia'
import { getToken, setToken, logoutAndRedirect, AUTH, apiJSON, getImpersonateTenant, setImpersonateTenant, apiFetch } from '../lib/api'
import { isSuperAdmin, ROLES } from '../lib/roles'
import type { ContributionDrill } from '../types'
import { CONFIG_CATS, MENU_GROUPS, type Cfg, type MenuItemDef } from './admin/shared'

// P2-13(2026-09-22)：后台各业务 Tab 全量 lazy 化（数目随菜单增长，此处刻意不写死计数——
// 原注释写"18 个"，加一个 Tab 就静默失真）——/admin 首屏 chunk 从 177KB 降到骨架级，
// 每个 Tab 按需加载，Suspense 统一骨架态（见 PanelContent 调用点）。
const AuditTab = lazy(() => import('./admin/AuditTab').then(m => ({ default: m.AuditTab })))
const BrandingTab = lazy(() => import('./admin/BrandingTab').then(m => ({ default: m.BrandingTab })))
const ChannelsTab = lazy(() => import('./admin/ChannelsTab').then(m => ({ default: m.ChannelsTab })))
const CdpTab = lazy(() => import('./admin/CdpTab'))
const ConfigPanels = lazy(() => import('./admin/ConfigPanel').then(m => ({ default: m.ConfigPanels })))
const CustomersTab = lazy(() => import('./admin/CustomersTab').then(m => ({ default: m.CustomersTab })))
const DashboardTab = lazy(() => import('./admin/DashboardTab').then(m => ({ default: m.DashboardTab })))
const FlowEngineTab = lazy(() => import('./admin/FlowEngineTab').then(m => ({ default: m.FlowEngineTab })))
const IndustryPackTab = lazy(() => import('./admin/IndustryPackTab'))
const KnowledgeTab = lazy(() => import('./admin/KnowledgeTab'))
const OpenApiTab = lazy(() => import('./admin/OpenApiTab').then(m => ({ default: m.OpenApiTab })))
const ReferralTab = lazy(() => import('./admin/ReferralTab').then(m => ({ default: m.ReferralTab })))
const StrategyTemplateTab = lazy(() => import('./admin/StrategyTab').then(m => ({ default: m.StrategyTemplateTab })))
const StrategyTestTab = lazy(() => import('./admin/StrategyTab').then(m => ({ default: m.StrategyTestTab })))
const TenantKBTab = lazy(() => import('./admin/TenantKBTab'))
const TagSystemTab = lazy(() => import('./admin/TagSystemTab').then(m => ({ default: m.TagSystemTab })))
const UsageTab = lazy(() => import('./admin/UsageTab').then(m => ({ default: m.UsageTab })))
const WebhookTab = lazy(() => import('./admin/WebhookTab').then(m => ({ default: m.WebhookTab })))
const OutreachTab = lazy(() => import('./admin/OutreachTab').then(m => ({ default: m.OutreachTab })))
const AcquisitionTab = lazy(() => import('./admin/AcquisitionTab').then(m => ({ default: m.AcquisitionTab })))
const PrivacyTab = lazy(() => import('./admin/PrivacyTab').then(m => ({ default: m.PrivacyTab })))

const { Header, Aside, Content } = Layout
const { MenuItem, MenuGroup } = Menu

const ZERO_DELAY_KEYS = [
  'merge_window_seconds', 'simple_msg_delay', 'store_visit_first_delay', 'store_visit_second_delay',
]

// Admin 租户管理端页面入口：按 Tab 组织知识库、行业包、CDP、租户等后台功能。
export default function Admin() {
  const brand = useBrand()
  const [tab, setTab] = useState('dashboard')
  const [logged, setLogged] = useState(!!getToken())
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [tenantCode, setTenantCode] = useState('')
  const [loginErr, setLoginErr] = useState('')
  const [all, setAll] = useState<Cfg[]>([])
  const [edits, setEditsState] = useState<Record<string, string>>({})
  // P1-10(2026-09-20)：窄屏（≤900px）折叠左侧 Aside，菜单降为顶栏全宽下拉，390px 手机三台可达
  const isMobile = useIsMobile(900)
  // 受控编辑某个配置项的草稿值（不即时提交，存到 edits 待保存）
  const setEdits = (k: string, v: string) => setEditsState((s) => ({ ...s, [k]: v }))
  // E4 修复(2026-09-14)：super_admin 访问租户作用域路径须显式 X-Tenant-ID（后端 tenant.go:462 强制，
  // 否则 400），旧前端全仓不发该头 → 超管进 /admin 各 Tab 全崩。加"代管租户"选择器，选定后
  // 统一由 apiFetch/authHeaders 注入 X-Tenant-ID；未选前只放行平台级 Tab（配置/超管后台）。
  const role = localStorage.getItem('role') || ''
  const isSuper = isSuperAdmin(role)
  const [impTenant, setImpTenant] = useState(getImpersonateTenant())
  const [tenants, setTenants] = useState<{ id: number; name: string; code?: string }[]>([])
  const [impReload, setImpReload] = useState(0) // 切换租户后强制各 Tab 重挂载刷新
  // 超管代管用：拉取平台全部租户（名称/标识）填充「代管租户」选择器
  // P1-7 迁移(2026-09-20)：裸 fetch → apiJSON（超时/断网归一 res:null，不抛错）；选择器失败仍静默留空，故不走会 toast 的 AUTH
  async function loadTenants() {
    const { json: j } = await apiJSON('/api/v1/super/tenants')
    const list: Array<{ id: number; name?: string; company_name?: string; code?: string }> = j?.data?.list || j?.data || []
    setTenants(Array.isArray(list) ? list.map((t) => ({ id: t.id, name: t.name || t.company_name || ('租户' + t.id), code: t.code })) : [])
  }
  // 选定代管租户：写入全局注入键 + 本地态 + 自增 impReload 强制各 Tab 重挂载刷新
  function pickTenant(id: string) {
    setImpersonateTenant(id)
    setImpTenant(id)
    setImpReload((n) => n + 1)
  }

  // D4(2026-09-23)：AI 贡献度看板的下钻预设。持有在 Admin 而非卡片/列表内部，
  // 因为点数字要换 Tab——谁跨 Tab，谁负责状态。菜单点击即清空：从侧栏进「客户线索」
  // 永远是完整列表，不会带着上一次看板的滤镜却看不见任何提示。
  const [drill, setDrill] = useState<ContributionDrill | null>(null)

  // 管理员登录：带租户码换 token；命中首登强改密标记则跳登录页改密，否则进后台并加载配置
  // P1-7 迁移(2026-09-20)：裸 fetch → apiJSON（断网/超时归一 {res:null,json:null} 不再抛错），
  // 错误仍写回页内 loginErr 横幅（登录失败属预期分支，不走 AUTH 的全局 toast）
  async function doLogin(e: React.FormEvent) {
    e.preventDefault()
    setLoginErr('')
    const { res, json: j } = await apiJSON('/api/v1/auth/login', {
      method: 'POST',
      body: JSON.stringify({ username, password, tenant_code: tenantCode }),
    })
    if (!res) { setLoginErr('网络异常，登录请求未发出，请检查网络后重试'); return }
    if (j?.code !== 0) { setLoginErr(j?.message || '登录失败'); return }
    setToken(j.data.token)
    localStorage.setItem('role', j.data.user.role)
    localStorage.setItem('username', j.data.user.username)
    if (j.data.user.must_change_password) {
      location.href = '/login?mcp=1'
      return
    }
    setLogged(true)
    loadAll()
  }

  // 拉取全部系统配置项，回填展示值与编辑草稿
  // P1-7 迁移(2026-09-20)：走 AUTH 统一鉴权/超时；失败仅 toastError，保留旧草稿不清空
  async function loadAll() {
    const j = await AUTH('/api/v1/admin/config')
    if (j?.code === 0) {
      setAll(j.data || [])
      const e: Record<string, string> = {}
      ;(j.data || []).forEach((c: Cfg) => (e[c.key] = c.value))
      setEditsState(e)
    }
  }
  useEffect(() => { if (logged) loadAll() }, [logged])
  useEffect(() => { if (logged && isSuper) loadTenants() }, [logged])

  // 保存配置：string 类型 JSON 序列化、其余原样，整表 PUT 后热加载
  // P1-7 迁移(2026-09-20)：裸 fetch → AUTH（body 传数组自动序列化；失败由 toastError 统一提示，不再手拼 error）
  async function saveAll() {
    const updates = all.map((c) => {
      const v = edits[c.key] ?? c.value
      if (c.value_type === 'string') return { key: c.key, value: JSON.stringify(v) }
      return { key: c.key, value: String(v) }
    })
    const j = await AUTH('/api/v1/admin/config', { method: 'PUT', body: updates })
    if (j?.code === 0) { MessagePlugin.success('配置已保存并热加载'); loadAll() }
  }
  // 恢复全部配置为默认值（不可撤销，二次确认）
  async function resetAll() {
    if (!(await confirmDialog('确定恢复所有配置为默认值？此操作不可撤销。'))) return
    // P2 修复(2026-09-15)：旧版不看响应码——401/500 也 toast"已恢复默认"误导超管。改走 apiFetch 并判 code。
    const j = await (await apiFetch('/api/v1/admin/config/reset', { method: 'POST' })).json().catch(() => null)
    if (j?.code === 0) { MessagePlugin.success('已恢复默认'); loadAll() }
    else MessagePlugin.error(j?.message || '恢复失败')
  }
  // 延迟参数一键归零并切「秒回」模式（仅影响 ZERO_DELAY_KEYS 命中的键，便于调试）
  async function zeroDelayAll() {
    const targets = ZERO_DELAY_KEYS.filter((k) => all.some((c) => c.key === k))
    if (!(await confirmDialog(`仅将延迟类参数归零并切秒回模式？影响键：${targets.join('、') || '(无)'}`))) return
    const e = { ...edits }
    targets.forEach((k) => (e[k] = '0'))
    e['reply_delay_mode'] = 'instant'
    setEditsState(e)
    const updates = all.map((c) => ({ key: c.key, value: String(e[c.key] ?? c.value) }))
    // P2-9 修复(2026-09-15)：同 resetAll——不判响应码恒报成功；且改 apiFetch 统一鉴权/租户头。
    const j = await (await apiFetch('/api/v1/admin/config', { method: 'PUT', body: JSON.stringify(updates) })).json().catch(() => null)
    if (j?.code === 0) { MessagePlugin.success('延迟已归零'); loadAll() }
    else MessagePlugin.error(j?.message || '归零失败')
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

  // 按分类取配置项子集
  const configsFor = (cat: string) => all.filter((c) => c.category === cat)
  const noAction = ['dashboard', 'customers', 'cdp', 'knowledge', 'tenant_kb', 'advisor', 'org', 'flow_engine', 'industry_packs', 'tags', 'strategy_templates', 'strategy_test', 'channels', 'audit', 'openapi', 'webhooks', 'outreach', 'acquisition', 'usage', 'referral', 'branding', 'billing'].includes(tab)
  const logo = brand.logoUrl ? <img src={brand.logoUrl} alt="" style={{ height: 28, marginRight: 8 }} /> : null
  const userName = localStorage.getItem('username') || ''
  // 切 Tab 的唯一入口：任何菜单点击都清掉下钻预设——从侧栏进「客户线索」必须是完整列表，
  // 不能带着上一次看板点进来的指标筛选却没有任何提示
  const openTab = (k: string) => { setDrill(null); setTab(k) }
  // 菜单点击：外链直接跳转，内链切当前 Tab
  const onMenuClick = (item: MenuItemDef) => {
    if (item.link) { location.href = item.link; return }
    openTab(item.k)
  }
  // 看板点数字 → 客户名单：切 Tab 并带上「指标 + 窗口」。Admin 是当前 SPA 里唯一能跨 Tab
  // 传状态的持有者（各 Tab 之间没有路由参数），所以预设只能放在这里。
  const openDrill = (d: ContributionDrill) => { setDrill(d); setTab('customers') }
  // 名单页「返回看板」：清预设并回工作台，回来时窗口仍是卡片自己的态
  const exitDrill = () => { setDrill(null); setTab('dashboard') }
  const pickMobileMenu = (k: string) => {
    if (k === 'super_link') { location.href = '/super'; return }
    for (const g of MENU_GROUPS) {
      const it = g.items.find((x) => x.k === k)
      if (it) { onMenuClick(it); return }
    }
  }

  return (
    <Layout style={{ minHeight: '100vh', background: '#f5f7fa' }}>
      <Header style={header}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {logo}
          <h1 style={{ fontSize: 18, fontWeight: 700, color: '#1f2937' }}>管理中心</h1>
          <span style={{ fontSize: 12, color: '#9ca3af', background: '#f3f4f6', padding: '2px 8px', borderRadius: 10 }}>{isSuperAdmin(role) ? '平台超管' : '租户后台'}</span>
        </div>
        {/* P1-10 补修(2026-09-21 终局回归实证)：390px 下右侧整块不换行+原生 select 固有宽度
            按最长 option 撑开（租户名一多顶栏 scrollWidth 冲到 790）——右侧允许换行、
            select 封顶 150px 省略号截断，选项点开仍看全称 */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap', minWidth: 0 }}>
          {/* E4：超管代管租户选择器——选定后所有租户作用域接口自动带 X-Tenant-ID */}
          {isSuper && (
            <span style={{ display: 'flex', alignItems: 'center', gap: 6, fontSize: 13, minWidth: 0 }}>
              代管租户：
              <select aria-label="代管租户" value={impTenant} onChange={(e) => pickTenant(e.target.value)} style={{ padding: '3px 8px', borderRadius: 6, border: '1px solid #e2e8f0', fontSize: 13, maxWidth: 150, textOverflow: 'ellipsis', overflow: 'hidden' }}>
                <option value="">（未选择·仅平台级）</option>
                {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}{t.code ? `（${t.code}）` : ''}</option>)}
              </select>
            </span>
          )}
          <a href="/client" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>客户对话页</a>
          <a href="/pricing" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>定价</a>
          <a href="/app" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>移动端</a>
          <span style={{ fontSize: 13, color: '#6b7280' }}>{userName}</span>
          <Button size="small" variant="outline" onClick={logoutAndRedirect}>退出</Button>
        </div>
      </Header>
      <Layout>
        {isMobile ? (
          /* P1-10：窄屏顶栏菜单——原生 select + optgroup 复用 MENU_GROUPS 同一数据源，选择即切 Tab */
          <div style={{ width: '100%', background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '8px 12px' }}>
            <select value={tab} onChange={(e) => pickMobileMenu((e.target as HTMLSelectElement).value)} aria-label="管理菜单" style={{ width: '100%', padding: '8px 10px', borderRadius: 8, border: '1px solid #e2e8f0', fontSize: 14, background: '#fff' }}>
              {MENU_GROUPS.filter((g) => g.items.length > 0).map((g) => (
                <optgroup key={g.title} label={g.title}>
                  {g.items.map((it) => <option key={it.k} value={it.k}>{it.label}</option>)}
                </optgroup>
              ))}
              {isSuperAdmin(role) && (
                <optgroup label="平台级">
                  <option value="super_link">平台超管后台</option>
                </optgroup>
              )}
            </select>
          </div>
        ) : (
        <Aside width="200" style={{ background: '#fff', borderRight: '1px solid #e5e7eb' }}>
          <Menu value={tab} onChange={(v) => openTab(v as string)} style={{ borderRight: 'none' }}>
            {MENU_GROUPS.map((g) => (
              <MenuGroup key={g.title} title={g.title}>
                {g.items.map((it) => (
                  <MenuItem key={it.k} value={it.k} onClick={() => onMenuClick(it)}>{it.label}</MenuItem>
                ))}
              </MenuGroup>
            ))}
            {isSuperAdmin(role) && (
              <MenuGroup title="平台级">
                <MenuItem value="super_link" onClick={() => { location.href = '/super' }}>平台超管后台</MenuItem>
              </MenuGroup>
            )}
          </Menu>
        </Aside>
        )}
        <Content style={{ padding: isMobile ? 12 : '20px 24px', minWidth: 0 }}>
          <div style={{ maxWidth: 1100, margin: '0 auto' }}>
            {(() => {
              const isPlatformTab = CONFIG_CATS.includes(tab) || tab === 'super_link'
              if (isSuper && !impTenant && !isPlatformTab) {
                return (
                  <div style={{ background: '#fffbeb', border: '1px solid #fbbf24', borderRadius: 10, padding: '16px 20px', marginBottom: 16, color: '#92400e', fontSize: 14 }}>
                    超级管理员访问<strong>租户作用域</strong>页面（客户/知识库/通道/用量等）需先在右上角选择"代管租户"——
                    后端对这类路径强制校验 <code>X-Tenant-ID</code>，缺失返回 400。平台参数配置与"平台超管后台"无需选择。
                  </div>
                )
              }
              return (
                <Suspense fallback={<TabLoading />}>
                  <PanelContent key={isSuper ? 'imp' + impReload : 't'} tab={tab} configsFor={configsFor} edits={edits} setEdits={setEdits} all={all} drill={drill} onDrill={openDrill} onExitDrill={exitDrill} />
                </Suspense>
              )
            })()}
            {!noAction && (
              <div style={{ marginTop: 20, display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 10, flexWrap: 'wrap', background: '#fff', border: '1px solid #e5e7eb', borderRadius: 10, padding: '12px 16px' }}>
                <p style={{ fontSize: 13, color: '#6b7280' }}>修改参数后点击"保存配置"，即时生效，无需重启</p>
                <div style={{ display: 'flex', gap: 12 }}>
                  <Button theme="warning" variant="outline" onClick={zeroDelayAll}>⚡ 延迟归零</Button>
                  <Button theme="default" variant="outline" onClick={resetAll}>↩ 恢复默认</Button>
                  <Button theme="primary" onClick={saveAll}>💾 保存配置</Button>
                </div>
              </div>
            )}
          </div>
        </Content>
      </Layout>
    </Layout>
  )
}

/** 后台 Tab 按需加载的骨架态（P2-13 lazy 化配套，避免切 Tab 白屏）。 */
function TabLoading() {
  return (
    <div className="bg-white rounded-lg border border-gray-200 p-10 text-center">
      <p style={{ color: '#9ca3af' }}>模块加载中…</p>
    </div>
  )
}

/** 后台 Tab 内容分发器，根据当前菜单渲染对应管理面板。
 *  drill / onDrill / onExitDrill 是 D4 的下钻三件套：工作台卡片发起、客户列表消费、
 *  「返回看板」回到发起处，状态本身由 Admin 持有。 */
function PanelContent({ tab, configsFor, edits, setEdits, all, drill, onDrill, onExitDrill }: { tab: string; configsFor: (c: string) => Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void; all: Cfg[]; drill?: ContributionDrill | null; onDrill?: (d: ContributionDrill) => void; onExitDrill?: () => void }) {
  if (tab === 'dashboard') return <DashboardTab onDrill={onDrill} />
  if (tab === 'customers') return <CustomersTab drill={drill} onExitDrill={onExitDrill} />
  if (tab === 'cdp') return <CdpTab />
  if (tab === 'knowledge') return <KnowledgeTab />
  if (tab === 'tenant_kb') return <TenantKBTab configs={all} />
  if (CONFIG_CATS.includes(tab)) return <ConfigPanels cfgs={configsFor(tab)} edits={edits} setEdits={setEdits} />
  if (tab === 'commercial') return <ConfigPanels cfgs={configsFor('billing')} edits={edits} setEdits={setEdits} />
  if (tab === 'flow_engine') return <FlowEngineTab configs={all} />
  if (tab === 'industry_packs') return <IndustryPackTab />
  if (tab === 'tags') return <TagSystemTab />
  if (tab === 'strategy_templates') return <StrategyTemplateTab />
  if (tab === 'strategy_test') return <StrategyTestTab />
  if (tab === 'channels') return <ChannelsTab />
  if (tab === 'branding') return <BrandingTab />
  if (tab === 'audit') return <AuditTab />
  if (tab === 'openapi') return <OpenApiTab />
  if (tab === 'webhooks') return <WebhookTab />
  if (tab === 'outreach') return <OutreachTab />
  if (tab === 'acquisition') return <AcquisitionTab />
  if (tab === 'usage') return <UsageTab />
  if (tab === 'referral') return <ReferralTab />
  if (tab === 'privacy') return <PrivacyTab />
  return <Placeholder name={tab} />
}

/** 未拆分 Tab 的占位面板，提示后续维护入口。 */
function Placeholder({ name }: { name: string }) {
  return (
    <div className="bg-white rounded-lg border border-gray-200 p-10 text-center">
      <div style={{ fontSize: 40, color: '#d1d5db' }} className="mb-3">🚧</div>
      <p style={{ color: '#9ca3af' }}>「{name}」模块正在迁移中，下一轮迭代补齐</p>
    </div>
  )
}

const card: React.CSSProperties = { background: '#fff', borderRadius: 12, padding: 24, width: 'min(320px, 92vw)', boxShadow: '0 4px 24px rgba(0,0,0,.08)' }
const inp: React.CSSProperties = { width: '100%', padding: 10, border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14, marginBottom: 12 }
const header: React.CSSProperties = { background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '12px 16px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }
