/**
 * UserAgreement.tsx：用户协议静态页
 * 展示平台服务条款与责任声明，注册即视为同意
 * 无业务接口依赖，纯静态内容
 */
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

import { useI18n } from '../i18n/useI18n'

/**
 * 用户协议静态页组件
 * 包含九个章节：定义与服务范围、账号注册与安全管理、服务内容与使用规范、
 * AI 生成内容声明、知识产权、费用与订阅、责任限制、协议变更与终止、法律适用与联系
 * P2-14：条款正文全部落词典（agreement.*），本文件只做排版，不再持有可译文本
 */
export default function UserAgreement() {
  const { t } = useI18n()
  return (
    <div style={body}>
      <h1 style={h1}>{t('agreement.title')}</h1>
      <p style={meta}>{t('agreement.meta')}</p>

      {/* 协议引言：说明协议双方与生效条件 */}
      <p>
        {t('agreement.introA')}
        <span style={brand}>{t('agreement.introBrand1')}</span>{t('agreement.introB')}
        <span style={brand}>{t('agreement.introBrand2')}</span>{t('agreement.introC')}
        <strong>{t('agreement.introStrong')}</strong>
      </p>

      {/* 第一章：定义与服务范围 */}
      <h2 style={h2}>{t('agreement.h1')}</h2>
      <p>{t('agreement.s11')}</p>
      <p>{t('agreement.s12')}</p>
      <p>{t('agreement.s13')}</p>

      {/* 第二章：账号注册与安全管理 */}
      <h2 style={h2}>{t('agreement.h2')}</h2>
      <p>{t('agreement.s21')}</p>
      <p>{t('agreement.s22')}</p>
      <p>{t('agreement.s23')}</p>

      {/* 第三章：服务内容与使用规范 */}
      <h2 style={h2}>{t('agreement.h3')}</h2>
      <p>{t('agreement.s31')}</p>
      <p>{t('agreement.s32')}</p>

      {/* 第四章：AI 生成内容声明 */}
      <h2 style={h2}>{t('agreement.h4')}</h2>
      <p>{t('agreement.s41a')}<strong>{t('agreement.s41Strong')}</strong>{t('agreement.s41b')}</p>
      <p>{t('agreement.s42')}</p>

      {/* 第五章：知识产权 */}
      <h2 style={h2}>{t('agreement.h5')}</h2>
      <p>{t('agreement.s51')}</p>
      <p>{t('agreement.s52')}</p>

      {/* 第六章：费用与订阅 */}
      <h2 style={h2}>{t('agreement.h6')}</h2>
      <p>{t('agreement.s61')}</p>
      <p>{t('agreement.s62')}</p>

      {/* 第七章：责任限制 */}
      <h2 style={h2}>{t('agreement.h7')}</h2>
      <p>{t('agreement.s71')}</p>
      <p>{t('agreement.s72')}</p>

      {/* 第八章：协议变更与终止 */}
      <h2 style={h2}>{t('agreement.h8')}</h2>
      <p>{t('agreement.s81')}</p>
      <p>{t('agreement.s82')}</p>

      {/* 第九章：法律适用与联系 */}
      <h2 style={h2}>{t('agreement.h9')}</h2>
      <p>{t('agreement.s91')}</p>
      <p>{t('agreement.s92a')}<a href="mailto:legal@lexcross.example">legal@lexcross.example</a>{t('agreement.s92b')}</p>

      {/* 页脚：版权信息 */}
      <p style={meta}>{t('agreement.footer')}</p>

      {/* 底部导航：法律链接 */}
      <div className="footer-legal">
        <a href="/user-agreement">{t('login.userAgreement')}</a> · <a href="/privacy-policy">{t('login.privacyPolicy')}</a> · {t('agreement.footerBrand')}
      </div>
    </div>
  )
}
