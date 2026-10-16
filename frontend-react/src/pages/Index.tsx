/**
 * Index.tsx：产品官网落地页
 * 展示平台能力卡片与 CTA 入口，按 Host 读取白标品牌名/Logo
 * 面向潜在客户，无登录态要求
 */
import { useBrand } from '../lib/branding'
import { useI18n } from '../i18n/useI18n'
import type { I18nKey } from '../i18n/context'

/**
 * 营销落地首页组件
 * 1. 顶部：品牌 Logo/名称 + 平台定位语
 * 2. 中部：三大核心能力卡片（智能接话、全链路 AI、多端在线）
 * 3. 标签区：SaaS 化、多租户、实时数据
 * 4. CTA 区：立即体验、免费开通、查看套餐
 * 5. 底部导航：各功能入口链接
 */
export default function Index() {
  const brand = useBrand()
  // 翻译函数（P2-14）
  const { t } = useI18n()
  // 品牌 Logo（有则显示图片，无则显示默认图标）
  const logo = brand.logoUrl ? (
    <img src={brand.logoUrl} alt={brand.brandName} style={{ height: 64, marginBottom: 24 }} />
  ) : (
    <div style={{ fontSize: 64, marginBottom: 24 }}>🏔️</div>
  )
  // 核心能力卡片数据（title/desc 存 i18n 键，渲染时 t() 翻译）
  const features: { icon: string; title: I18nKey; desc: I18nKey }[] = [
    { icon: 'M12 2L2 7l10 5 10-5-10-5zM2 17l10 5 10-5M2 12l10 5 10-5', title: 'index.feature1Title', desc: 'index.feature1Desc' },
    { icon: 'M2 3h20v2H2V3zm0 4h20v2H2V7zm0 4h20v2H2v-2zm0 4h20v2H2v-2zm0 4h20v2H2v-2z', title: 'index.feature2Title', desc: 'index.feature2Desc' },
    { icon: 'M12 22c1.1 0 2-.9 2-2h-4c0 1.1.89 2 2 2zm4.93-7.5l1.42-1.42c-.98-.98-2.57-.43-3.54.75L14.5 14.46l-5.33 5.32c-.59.59-.59 1.52 0 2.11zm-11.28.78l1.56 1.56c.39.39 1.02.11 1.41-.7L2.54 9.13c-.47-.66-.79-1.56-.79-2.38 0-1.32.63-2.39 1.28-2.96l2.17-2.17c.38-.38.99-.41 1.41-.19l1.66 1.66L13 15.5l2.66 2.66c.45.45.45 1.06 0 1.51z', title: 'index.feature3Title', desc: 'index.feature3Desc' },
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
        padding: '32px 16px',
      }}
    >
      <div style={{ textAlign: 'center', maxWidth: 900, margin: '0 auto' }}>
        {/* 品牌 Logo */}
        {logo}
        {/* 品牌名称：clamp 随视口缩放，移动端不溢出 */}
        <h1 style={{ fontSize: 'clamp(26px, 6vw, 42px)', fontWeight: 800, marginBottom: 16, letterSpacing: 2, color: '#1a202c', lineHeight: 1.2, wordBreak: 'break-word' }}>
          {brand.brandName}
        </h1>
        {/* 平台定位语 */}
        <p style={{ fontSize: 'clamp(14px, 3.5vw, 18px)', color: '#4a5568', marginBottom: 32 }}>{t('index.tagline')}</p>

        {/* 核心能力卡片网格 */}
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
              {/* 能力图标 */}
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
              {/* 能力标题与描述 */}
              <div style={{ fontSize: 20, fontWeight: 600, marginBottom: 12 }}>{t(f.title)}</div>
              <div style={{ color: '#718096', fontSize: 14, lineHeight: 1.6 }}>{t(f.desc)}</div>
            </div>
          ))}
        </div>

        {/* 标签区：SaaS 化、多租户、实时数据 */}
        <div style={{ display: 'flex', gap: 16, justifyContent: 'center', marginTop: 32, flexWrap: 'wrap' }}>
          <span className="badge" style={badgeStyle}>{t('index.badgeSaas')}</span>
          <span className="badge" style={badgeStyle}>{t('index.badgeMultiTenant')}</span>
          <span className="badge" style={badgeStyle}>{t('index.badgeRealtime')}</span>
        </div>

        {/* CTA 区：立即体验、免费开通、查看套餐 */}
        <div style={{ marginTop: 64, textAlign: 'center' }}>
          <a className="cta-btn" style={ctaStyle} href="/client">{t('index.ctaTrial')}</a>
          <a className="cta-btn" style={ctaStyle} href="/register">{t('index.ctaSignup')}</a>
          <a className="cta-btn" style={ctaStyle} href="/pricing">{t('index.ctaPricing')}</a>
        </div>

        {/* 底部导航：各功能入口链接 */}
        <div style={{ marginTop: 48, display: 'flex', gap: 24, justifyContent: 'center', flexWrap: 'wrap' }}>
          <a className="nav-link" href="/pricing">{t('index.navPricing')}</a>
          <a className="nav-link" href="/docs/api">{t('index.navApiDocs')}</a>
          <a className="nav-link" href="/billing">{t('index.navBilling')}</a>
          <a className="nav-link" href="/login">{t('index.navLogin')}</a>
          <a className="nav-link" href="/admin">{t('index.navAdmin')}</a>
          <a className="nav-link" href="/org">{t('index.navOrg')}</a>
          <a className="nav-link" href="/super">{t('index.navSuper')}</a>
        </div>
      </div>

      {/* 页脚：法律链接与品牌名 */}
      <div className="footer-legal">
        <a href="/user-agreement">{t('login.userAgreement')}</a> ·{' '}
        <a href="/privacy-policy">{t('login.privacyPolicy')}</a> · {t('index.footerBrand', { brand: brand.brandName })}
      </div>
    </div>
  )
}

// 标签样式（半透明白底圆角标签）
const badgeStyle: React.CSSProperties = {
  background: 'rgba(255,255,255,0.2)',
  padding: '4px 12px',
  borderRadius: 20,
  fontSize: 12,
  color: 'white',
}
// CTA 按钮样式（渐变背景圆角按钮）
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
