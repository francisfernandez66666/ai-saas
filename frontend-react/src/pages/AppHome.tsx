/**
 * AppHome.tsx：移动端工作台首页
 * 以卡片入口导航至对话/顾问台/收银台/邀请/设置/定价
 * 嵌套在 AppLayout 布局内，受登录态守卫保护
 */
import { useBrand } from '../lib/branding'
import { isStaff } from '../lib/roles'

/**
 * 移动端工作台首页组件
 * 展示功能入口卡片，每个卡片包含图标、标题、描述和进入按钮
 * 卡片链接到 /app 下的各子路由
 *
 * G-23(2026-09-25) 角色分流：卡片集合与 AppLayout 顶栏用同一判据（isStaff = 后端
 * AdminRequired 集合），否则会出现"顶栏没有收银台、首页卡片点进去却是禁用态"。
 * needAdmin 的卡片仅管理岗展示；其余卡片对未登录访客保留（点进去由 AppLayout 守卫甩到
 * /app/login 带 redirect 回跳——首页是注册漏斗的一环，不把入口藏掉）。
 */
export default function AppHome() {
  const brand = useBrand()
  const isAdmin = isStaff(localStorage.getItem('role') || '')
  // 功能入口卡片数据
  const cards = [
    { to: '/app/chat', icon: '💬', title: '客户对话', desc: 'C端访客入口', needAdmin: false },
    { to: '/app/advisor', icon: '🧑‍💼', title: '顾问台', desc: '客户/会话/接管', needAdmin: false },
    { to: '/app/billing', icon: '💰', title: '收银台', desc: '三桶余额/套餐充值', needAdmin: true },
    { to: '/app/referral', icon: '🎁', title: '邀请推广', desc: '链接/二维码/奖励', needAdmin: false },
    { to: '/app/settings', icon: '⚙️', title: '账号设置', desc: '改密/换绑/知识库/注销', needAdmin: false },
    { to: '/pricing', icon: '🧾', title: '定价', desc: '行业包免费·token计费', needAdmin: false },
  ].filter((c) => !c.needAdmin || isAdmin)
  return (
    <div style={{ maxWidth: 760, margin: '0 auto', padding: 16 }}>
      {/* 工作台标题卡片 */}
      <div style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }}>
        <h2 style={{ margin: 0, fontSize: 20 }}>{brand.brandName} 工作台</h2>
        <p style={{ color: '#718096', margin: '6px 0 0', fontSize: 14 }}>策略锚定 · 行业知识库 · 流程培育 · 数据飞轮</p>
      </div>
      {/* 功能入口卡片网格 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        {cards.map((c) => (
          <a key={c.title} href={c.to} style={{ display: 'block', background: '#fff', borderRadius: 12, padding: 18, boxShadow: '0 4px 18px rgba(0,0,0,.06)', textDecoration: 'none', color: 'inherit' }}>
            <h3 style={{ margin: '0 0 4px', fontSize: 16 }}>{c.icon} {c.title}</h3>
            <p style={{ margin: 0, color: '#718096', fontSize: 13 }}>{c.desc}</p>
            <button style={{ marginTop: 12, padding: '8px 14px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', fontSize: 13, cursor: 'pointer' }}>进入</button>
          </a>
        ))}
      </div>
    </div>
  )
}
