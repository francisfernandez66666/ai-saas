/**
 * PrivacyPolicy.tsx：隐私政策静态页
 * 展示《个人信息保护法》等合规条款，无业务接口依赖
 * 纯静态内容，注册即视为同意
 * P2-14：条款正文全部落词典（privacy.*），本文件只做排版，不再持有可译文本
 */
import { useI18n } from '../i18n/useI18n'
// 正文容器样式（最大宽度居中，行高宽松；移动端 padding 收紧避免长文本贴边）
const body: React.CSSProperties = {
  fontFamily: '-apple-system,"PingFang SC","Microsoft YaHei",sans-serif',
  maxWidth: 860,
  margin: '0 auto',
  padding: 'clamp(16px, 4vw, 32px) clamp(14px, 4vw, 20px)',
  color: '#1f2937',
  lineHeight: 1.8,
  wordBreak: 'break-word',
}
// 一级标题样式（主色下边框）
const h1: React.CSSProperties = { fontSize: 24, borderBottom: '2px solid var(--pri)', paddingBottom: 10 }
// 小节标题样式
const h2: React.CSSProperties = { fontSize: 18, marginTop: 32, color: '#4338ca' }
// 元信息（更新日期、版权）灰色小字样式
const meta: React.CSSProperties = { color: '#94a3b8', fontSize: 13 }
// 品牌名高亮样式（用在正文中强调平台名）
const brand = { color: '#4f46e5', fontWeight: 600 } as React.CSSProperties

/**
 * 隐私政策静态页组件
 * 包含九个章节：适用范围与联系人、我们收集的信息、信息使用目的、
 * 信息共享与披露、数据存储与安全、您的权利、儿童信息、Cookie 与技术跟踪、政策更新
 */
export default function PrivacyPolicy() {
  const { t } = useI18n()
  return (
    <div style={body}>
      <h1 style={h1}>{t('privacy.title')}</h1>
      <p style={meta}>{t('privacy.meta')}</p>

      {/* 隐私政策引言：说明适用法律与生效条件 */}
      <p>
        {t('privacy.introA')}
        <span style={brand}>{t('privacy.introBrand')}</span>{t('privacy.introB')}
      </p>

      {/* 第一章：适用范围与联系人 */}
      <h2 style={h2}>{t('privacy.h1')}</h2>
      <p>{t('privacy.s1')}<a href="mailto:privacy@lexcross.example">privacy@lexcross.example</a>。</p>

      {/* 第二章：我们收集的信息 */}
      <h2 style={h2}>{t('privacy.h2')}</h2>
      <p>{t('privacy.s2')}</p>
      <ul>
        <li><strong>{t('privacy.li1a')}</strong>{t('privacy.li1b')}</li>
        <li><strong>{t('privacy.li2a')}</strong>{t('privacy.li2b')}</li>
        <li><strong>{t('privacy.li3a')}</strong>{t('privacy.li3b')}</li>
        <li><strong>{t('privacy.li4a')}</strong>{t('privacy.li4b')}</li>
      </ul>
      <p>{t('privacy.s2b')}</p>

      {/* 第三章：信息使用目的 */}
      <h2 style={h2}>{t('privacy.h3')}</h2>
      <p>{t('privacy.s3')}</p>

      {/* 第四章：信息共享与披露 */}
      <h2 style={h2}>{t('privacy.h4')}</h2>
      <p>{t('privacy.s41a')}<strong>{t('privacy.s41strong')}</strong>{t('privacy.s41b')}</p>
      <p>{t('privacy.s42')}</p>
      <p>{t('privacy.s43')}</p>

      {/* 第五章：数据存储与安全 */}
      <h2 style={h2}>{t('privacy.h5')}</h2>
      <p>{t('privacy.s51')}</p>
      <p>{t('privacy.s52')}</p>

      {/* 第六章：您的权利 */}
      <h2 style={h2}>{t('privacy.h6')}</h2>
      <p>{t('privacy.s6')}</p>
      <ul>
        <li>{t('privacy.r1')}</li>
        <li>{t('privacy.r2')}</li>
        <li>{t('privacy.r3')}</li>
        <li>{t('privacy.r4')}</li>
        <li>{t('privacy.r5')}</li>
      </ul>

      {/* 第七章：儿童信息 */}
      <h2 style={h2}>{t('privacy.h7')}</h2>
      <p>{t('privacy.s7')}</p>

      {/* 第八章：Cookie 与技术跟踪 */}
      <h2 style={h2}>{t('privacy.h8')}</h2>
      <p>{t('privacy.s8')}</p>

      {/* 第九章：政策更新 */}
      <h2 style={h2}>{t('privacy.h9')}</h2>
      <p>{t('privacy.s9')}</p>

      {/* 页脚：版权信息 */}
      <p style={meta}>{t('privacy.footer')}</p>

      {/* 底部导航：法律链接 */}
      <div className="footer-legal">
        <a href="/user-agreement">{t('login.userAgreement')}</a> · <a href="/privacy-policy">{t('login.privacyPolicy')}</a> · {t('privacy.footerBrand')}
      </div>
    </div>
  )
}
