// 后台管理页：租户管理员登录后按分类 Tab 维护配置、客户、知识库、标签、通道、审计等运营后台。
// F1 起各业务 Tab 拆至 src/pages/admin/*，本文件保留登录、菜单和配置编辑动作。
import { useEffect, useState } from 'react'
import { Button, Layout, Menu, MessagePlugin } from 'tdesign-react'
import { useBrand } from '../lib/branding'
import { getToken, setToken, logoutAndRedirect } from '../lib/api'
import { AuditTab } from './admin/AuditTab'
import { BrandingTab } from './admin/BrandingTab'
import { ChannelsTab } from './admin/ChannelsTab'
import CdpTab from './admin/CdpTab'
import { ConfigPanels } from './admin/ConfigPanel'
import { CustomersTab } from './admin/CustomersTab'
import { DashboardTab } from './admin/DashboardTab'
import { FlowEngineTab } from './admin/FlowEngineTab'
import IndustryPackTab from './admin/IndustryPackTab'
import KnowledgeTab from './admin/KnowledgeTab'
import { OpenApiTab } from './admin/OpenApiTab'
import { ReferralTab } from './admin/ReferralTab'
import { CONFIG_CATS, MENU_GROUPS, type Cfg, type MenuItemDef } from './admin/shared'
import { StrategyTemplateTab, StrategyTestTab } from './admin/StrategyTab'
import TenantKBTab from './admin/TenantKBTab'
import { TagSystemTab } from './admin/TagSystemTab'
import { UsageTab } from './admin/UsageTab'
import { WebhookTab } from './admin/WebhookTab'

const { Header, Aside, Content } = Layout
const { MenuItem, MenuGroup } = Menu

const ZERO_DELAY_KEYS = [
  'merge_window_seconds', 'simple_msg_delay', 'store_visit_first_delay', 'store_visit_second_delay',
]

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
    localStorage.setItem('username', j.data.user.username)
    if (j.data.user.must_change_password) {
      location.href = '/login?mcp=1'
      return
    }
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
    const targets = ZERO_DELAY_KEYS.filter((k) => all.some((c) => c.key === k))
    if (!confirm(`仅将延迟类参数归零并切秒回模式？影响键：${targets.join('、') || '(无)'}`)) return
    const e = { ...edits }
    targets.forEach((k) => (e[k] = '0'))
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
  const noAction = ['dashboard', 'customers', 'cdp', 'knowledge', 'tenant_kb', 'advisor', 'org', 'flow_engine', 'industry_packs', 'tags', 'strategy_templates', 'strategy_test', 'channels', 'audit', 'openapi', 'webhooks', 'usage', 'referral', 'branding', 'billing'].includes(tab)
  const logo = brand.logoUrl ? <img src={brand.logoUrl} alt="" style={{ height: 28, marginRight: 8 }} /> : null
  const role = localStorage.getItem('role') || ''
  const userName = localStorage.getItem('username') || ''
  const onMenuClick = (item: MenuItemDef) => {
    if (item.link) { location.href = item.link; return }
    setTab(item.k)
  }

  return (
    <Layout style={{ minHeight: '100vh', background: '#f5f7fa' }}>
      <Header style={header}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          {logo}
          <h1 style={{ fontSize: 18, fontWeight: 700, color: '#1f2937' }}>管理中心</h1>
          <span style={{ fontSize: 12, color: '#9ca3af', background: '#f3f4f6', padding: '2px 8px', borderRadius: 10 }}>{role === 'super_admin' ? '平台超管' : '租户后台'}</span>
        </div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <a href="/client" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>客户对话页</a>
          <a href="/pricing" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>定价</a>
          <a href="/app" style={{ fontSize: 13, color: '#4f46e5', textDecoration: 'none' }}>移动端</a>
          <span style={{ fontSize: 13, color: '#6b7280' }}>{userName}</span>
          <Button size="small" variant="outline" onClick={logoutAndRedirect}>退出</Button>
        </div>
      </Header>
      <Layout>
        <Aside width="200" style={{ background: '#fff', borderRight: '1px solid #e5e7eb' }}>
          <Menu value={tab} onChange={(v) => setTab(v as string)} style={{ borderRight: 'none' }}>
            {MENU_GROUPS.map((g) => (
              <MenuGroup key={g.title} title={g.title}>
                {g.items.map((it) => (
                  <MenuItem key={it.k} value={it.k} onClick={() => onMenuClick(it)}>{it.label}</MenuItem>
                ))}
              </MenuGroup>
            ))}
            {role === 'super_admin' && (
              <MenuGroup title="平台级">
                <MenuItem value="super_link" onClick={() => { location.href = '/super' }}>平台超管后台</MenuItem>
              </MenuGroup>
            )}
          </Menu>
        </Aside>
        <Content style={{ padding: '20px 24px', minWidth: 0 }}>
          <div style={{ maxWidth: 1100, margin: '0 auto' }}>
            <PanelContent tab={tab} configsFor={configsFor} edits={edits} setEdits={setEdits} all={all} />
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

function PanelContent({ tab, configsFor, edits, setEdits, all }: { tab: string; configsFor: (c: string) => Cfg[]; edits: Record<string, string>; setEdits: (k: string, v: string) => void; all: Cfg[] }) {
  if (tab === 'dashboard') return <DashboardTab />
  if (tab === 'customers') return <CustomersTab />
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

const card: React.CSSProperties = { background: '#fff', borderRadius: 12, padding: 24, width: 'min(320px, 92vw)', boxShadow: '0 4px 24px rgba(0,0,0,.08)' }
const inp: React.CSSProperties = { width: '100%', padding: 10, border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14, marginBottom: 12 }
const header: React.CSSProperties = { background: '#fff', borderBottom: '1px solid #e5e7eb', padding: '12px 16px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap' }
