/**
 * AppHome.tsx：移动端工作台首页
 * 以卡片入口导航至对话/顾问台/收银台/邀请/设置/定价
 * 嵌套在 AppLayout 布局内，受登录态守卫保护
 */
import { useBrand } from '../lib/branding'
import { isStaff } from '../lib/roles'
import { useI18n } from '../i18n/useI18n'
import type { I18nKey } from '../i18n/context'

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
  // 翻译函数（P2-14）
  const { t } = useI18n()
  const isAdmin = isStaff(localStorage.getItem('role') || '')
  // 功能入口卡片数据（title/desc 存 i18n 键，渲染时 t() 翻译）。
  // 标注必须落在 filter **之前**的字面量上：若写成 `[...].filter(...)` 再把标注加在变量上，
  // 数组字面量是 filter 的接收者、拿不到上下文类型，title/desc 会被放宽成 string 而丢失
  // I18nKey 校验（tsc TS2322）。拆成两步让字面量先吃标注。
  const ALL_CARDS: { to: string; icon: string; title: I18nKey; desc: I18nKey; needAdmin: boolean }[] = [
    { to: '/app/chat', icon: '💬', title: 'appHome.cardChatTitle', desc: 'appHome.cardChatDesc', needAdmin: false },
    { to: '/app/advisor', icon: '🧑‍💼', title: 'appHome.cardAdvisorTitle', desc: 'appHome.cardAdvisorDesc', needAdmin: false },
    { to: '/app/billing', icon: '💰', title: 'appHome.cardBillingTitle', desc: 'appHome.cardBillingDesc', needAdmin: true },
    { to: '/app/referral', icon: '🎁', title: 'appHome.cardReferralTitle', desc: 'appHome.cardReferralDesc', needAdmin: false },
    { to: '/app/settings', icon: '⚙️', title: 'appHome.cardSettingsTitle', desc: 'appHome.cardSettingsDesc', needAdmin: false },
    { to: '/pricing', icon: '🧾', title: 'appHome.cardPricingTitle', desc: 'appHome.cardPricingDesc', needAdmin: false },
  ]
  const cards = ALL_CARDS.filter((c) => !c.needAdmin || isAdmin)
  return (
    <div style={{ maxWidth: 760, margin: '0 auto', padding: 16 }}>
      {/* 工作台标题卡片 */}
      <div style={{ background: '#fff', borderRadius: 12, padding: 20, boxShadow: '0 4px 18px rgba(0,0,0,.07)', marginBottom: 16 }}>
        <h2 style={{ margin: 0, fontSize: 20 }}>{t('appHome.headerTitle', { brand: brand.brandName })}</h2>
        <p style={{ color: '#718096', margin: '6px 0 0', fontSize: 14 }}>{t('appHome.headerSubtitle')}</p>
      </div>
      {/* 功能入口卡片网格 */}
      <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
        {cards.map((c) => (
          <a key={c.title} href={c.to} style={{ display: 'block', background: '#fff', borderRadius: 12, padding: 18, boxShadow: '0 4px 18px rgba(0,0,0,.06)', textDecoration: 'none', color: 'inherit' }}>
            <h3 style={{ margin: '0 0 4px', fontSize: 16 }}>{c.icon} {t(c.title)}</h3>
            <p style={{ margin: 0, color: '#718096', fontSize: 13 }}>{t(c.desc)}</p>
            <button style={{ marginTop: 12, padding: '8px 14px', borderRadius: 8, border: 'none', background: 'var(--pri)', color: '#fff', fontSize: 13, cursor: 'pointer' }}>{t('appHome.enter')}</button>
          </a>
        ))}
      </div>
    </div>
  )
}
