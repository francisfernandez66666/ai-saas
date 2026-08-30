// Index.tsx：前端页面/模块（自动补注释）。
import { useBrand } from '../lib/branding'

// 营销落地首页：展示平台能力卡片与 CTA 入口，按 Host 读取白标品牌名/Logo
export default function Index() {
  const brand = useBrand()
  const logo = brand.logoUrl ? (
    <img src={brand.logoUrl} alt={brand.brandName} style={{ height: 64, marginBottom: 24 }} />
  ) : (
    <div style={{ fontSize: 64, marginBottom: 24 }}>🏔️</div>
  )
  const features = [
    { icon: 'M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5', title: '智能接话', desc: 'AI自动接听客户咨询，分钟级响应，永不错过商机' },
    { icon: 'M2 3h20v2H2V3zm0 4h20v2H2V7zm0 4h20v2H2v-2zm0 4h20v2H2v-2zm0 4h20v2H2v-2z', title: '全链路 AI', desc: '从建联到成单的全流程 AI 驱动，提效 100 倍' },
    { icon: 'M12 22c1.1 0 2-.9 2-2h-4c0 1.1.89 2 2 2zm4.93-7.5l1.42-1.42c-.98-.98-2.57-.43-3.54.75L14.5 14.46l-5.33 5.32c-.59.59-.59 1.52 0 2.11zm-11.28.78l1.56 1.56c.39.39 1.02.11 1.41-.7L2.54 9.13c-.47-.66-.79-1.56-.79-2.38 0-1.32.63-2.39 1.28-2.96l2.17-2.17c.38-.38.99-.41 1.41-.19l1.66 1.66L13 15.5l2.66 2.66c.45.45.45 1.06 0 1.51z', title: '多端在线', desc: '客户端/顾问端/管理端 全平台覆盖，随时随地管理客户' },
  ]
  return (
    <div
      style={{
        minHeight: '100vh',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        justifyContent: 'center',
        background: 'linear-gradient(135deg, var(--pri) 0%, #764ba2 100%)',
        padding: '40px 20px',
      }}
    >
      <div style={{ textAlign: 'center', maxWidth: 900, margin: '0 auto' }}>
        {logo}
        <h1 style={{ fontSize: 42, fontWeight: 800, marginBottom: 16, letterSpacing: 2, color: '#1a202c' }}>
          {brand.brandName}
        </h1>
        <p style={{ fontSize: 18, color: '#4a5568', marginBottom: 32 }}>车企AI驱动的智能客户关系管理平台</p>

        <div
          style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fit, minmax(250px, 1fr))',
            gap: 32,
            marginTop: 64,
          }}
        >
          {features.map((f) => (
            <div
              key={f.title}
              style={{
                background: 'white',
                borderRadius: 16,
                padding: '32px 24px',
                boxShadow: '0 4px 20px rgba(0,0,0,0.1)',
              }}
            >
              <div
                style={{
                  width: 48,
                  height: 48,
                  margin: '0 auto 24px',
                  background: 'linear-gradient(135deg, var(--pri), #764ba2)',
                  borderRadius: 12,
                  display: 'flex',
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <svg viewBox="0 0 24 24" style={{ width: 24, height: 24, fill: 'white' }}>
                  <path d={f.icon} />
                </svg>
              </div>
              <div style={{ fontSize: 20, fontWeight: 600, marginBottom: 12 }}>{f.title}</div>
              <div style={{ color: '#718096', fontSize: 14, lineHeight: 1.6 }}>{f.desc}</div>
            </div>
          ))}
        </div>

        <div style={{ display: 'flex', gap: 16, justifyContent: 'center', marginTop: 32, flexWrap: 'wrap' }}>
          <span className="badge" style={badgeStyle}>SaaS 化</span>
          <span className="badge" style={badgeStyle}>多租户</span>
          <span className="badge" style={badgeStyle}>实时数据</span>
        </div>

        <div style={{ marginTop: 64, textAlign: 'center' }}>
          <a className="cta-btn" style={ctaStyle} href="/client">立即体验</a>
          <a className="cta-btn" style={ctaStyle} href="/register">免费开通</a>
          <a className="cta-btn" style={ctaStyle} href="/pricing">查看套餐</a>
        </div>

        <div style={{ marginTop: 48, display: 'flex', gap: 24, justifyContent: 'center', flexWrap: 'wrap' }}>
          <a className="nav-link" href="/pricing">套餐定价</a>
          <a className="nav-link" href="/billing">订阅收银台</a>
          <a className="nav-link" href="/login">登录工作台</a>
          <a className="nav-link" href="/admin">管理后台</a>
          <a className="nav-link" href="/org">组织架构</a>
          <a className="nav-link" href="/super">平台管理</a>
        </div>
      </div>

      <div className="footer-legal">
        <a href="/user-agreement">用户协议</a> ·{' '}
        <a href="/privacy-policy">隐私政策</a> · {brand.brandName} AI-SCRM 平台
      </div>
    </div>
  )
}

// badgeStyle 常量/变量（自动补注释）。
const badgeStyle: React.CSSProperties = {
  background: 'rgba(255,255,255,0.2)',
  padding: '4px 12px',
  borderRadius: 20,
  fontSize: 12,
  color: 'white',
}
// ctaStyle 常量/变量（自动补注释）。
const ctaStyle: React.CSSProperties = {
  display: 'inline-block',
  background: 'linear-gradient(135deg, var(--pri), #764ba2)',
  color: 'white',
  padding: '16px 32px',
  borderRadius: 8,
  fontSize: 18,
  fontWeight: 600,
  textDecoration: 'none',
  margin: '0 8px',
}
