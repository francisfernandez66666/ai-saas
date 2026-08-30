/**
 * AppHome.tsx：移动端工作台首页
 * 以卡片入口导航至对话/顾问台/收银台/邀请/设置/定价
 * 嵌套在 AppLayout 布局内，受登录态守卫保护
 */
import { useBrand } from '../lib/branding'

/**
 * 移动端工作台首页组件
 * 展示 6 个功能入口卡片，每个卡片包含图标、标题、描述和进入按钮
 * 卡片链接到 /app 下的各子路由
 */
export default function AppHome() {
  const brand = useBrand()
  // 功能入口卡片数据
  const cards = [
    { to: '/app/chat', icon: '💬', title: '客户对话', desc: 'C端访客入口' },
    { to: '/app/advisor', icon: '🧑‍💼', title: '顾问台', desc: '客户/会话/接管' },
    { to: '/app/billing', icon: '💰', title: '收银台', desc: '三桶余额/套餐充值' },
    { to: '/app/referral', icon: '🎁', title: '邀请推广', desc: '链接/二维码/奖励' },
    { to: '/app/settings', icon: '⚙️', title: '账号设置', desc: '改密/换绑/知识库/注销' },
    { to: '/app/billing', icon: '🧾', title: '定价', desc: '行业包免费·token计费' },
  ]
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
