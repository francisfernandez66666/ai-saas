/**
 * branding.tsx：白标品牌模块
 * 按当前域名（Host）拉取租户白标配置，通过 React Context 下发品牌名/Logo/主题色等
 * 支持自定义 CSS/JS 注入，实现 SaaS 多租户白标能力
 */
import { createContext, useContext, useEffect, useState } from 'react'

/**
 * 租户白标类型定义：品牌名/Logo/主题色等（按 Host 拉取）
 * platformDefault 为 true 表示使用平台默认配置，未做白标定制
 */
export type Brand = {
  brandName: string       // 品牌名称
  brandLink: string       // 品牌链接（点击 Logo 跳转）
  logoUrl: string         // 品牌 Logo 地址
  faviconUrl: string      // 浏览器 favicon 地址
  primaryColor?: string   // 主题色（CSS 变量 --brand-primary）
  secondaryColor?: string // 辅助色（CSS 变量 --brand-secondary）
  platformDefault: boolean // 是否为平台默认配置（未定制白标）
}

// 平台级默认品牌（未配置白标时回退到此），避免标题/favicon 出现空白
const DEFAULT: Brand = {
  brandName: '跨山 LexCross',
  brandLink: '',
  logoUrl: '',
  faviconUrl: '',
  platformDefault: true,
}

// 品牌上下文：跨组件共享当前租户白标
const Ctx = createContext<Brand>(DEFAULT)

/**
 * 消费品牌上下文：各页面用它读取当前租户白标（名称/Logo/主题色等）
 * @returns 当前租户的 Brand 配置对象
 */
export function useBrand() {
  return useContext(Ctx)
}

/**
 * 品牌上下文提供者：按当前 Host 拉取租户白标并应用到页面
 * 等价于原 frontend/branding.js 的逻辑
 * 职责：
 * 1. 从 /api/v1/public/branding 获取白标配置
 * 2. 更新页面标题（document.title）
 * 3. 注入 favicon 和 logo 链接
 * 4. 设置 CSS 变量（--brand-primary、--brand-secondary）
 * 5. 注入自定义 CSS/JS（SaaS 白标定制）
 * @param children - 子组件
 */
export function BrandingProvider({ children }: { children: React.ReactNode }) {
  const [b, setB] = useState<Brand>(DEFAULT)

  useEffect(() => {
    // 按当前 Host 拉取租户白标配置（接口匿名可访问，用于登录前展示）
    fetch('/api/v1/public/branding')
      .then((r) => (r.ok ? r.json() : null))
      .then((d) => {
        if (!d) return
        const brandName =
          d.brand_name && d.brand_name !== '跨山 LexCross' ? d.brand_name : '跨山 LexCross'
        setB({
          brandName,
          brandLink: d.brand_link || '',
          logoUrl: d.logo_url || '',
          faviconUrl: d.favicon_url || '',
          primaryColor: d.primary_color,
          secondaryColor: d.secondary_color,
          platformDefault: !!d.platform_default,
        })
        // 标题：自定义品牌强制替换（P2 全量白标）；平台默认仅替换含默认名部分
        if (!d.platform_default && brandName) {
          document.title = brandName
        } else if (document.title.indexOf('跨山 LexCross') > -1) {
          document.title = document.title.replace('跨山 LexCross', brandName)
        }
        // 动态更新 favicon
        if (d.favicon_url) {
          let l = document.querySelector('link[rel="icon"]') as HTMLLinkElement | null
          if (!l) {
            l = document.createElement('link')
            l.rel = 'icon'
            document.head.appendChild(l)
          }
          l.href = d.favicon_url
        }
        // Logo 链接注入（供浏览器/爬虫识别，页面内 Logo 由 useBrand().logoUrl 渲染）
        if (d.logo_url) {
          let logo = document.querySelector('link[rel="logo"]') as HTMLLinkElement | null
          if (!logo) {
            logo = document.createElement('link')
            logo.rel = 'logo'
            document.head.appendChild(logo)
          }
          logo.href = d.logo_url
        }
        // 设置主题色 CSS 变量
        if (d.primary_color) {
          document.documentElement.style.setProperty('--brand-primary', d.primary_color)
        }
        if (d.secondary_color) {
          document.documentElement.style.setProperty('--brand-secondary', d.secondary_color)
        }
        // 白标自定义CSS/JS（P1-5）：按需注入，平台默认不注入
        if (d.custom_css) {
          let style = document.getElementById('brand-custom-css') as HTMLStyleElement | null
          if (!style) {
            style = document.createElement('style')
            style.id = 'brand-custom-css'
            document.head.appendChild(style)
          }
          style.textContent = d.custom_css
        }
        if (d.custom_js) {
          const s = document.createElement('script')
          s.id = 'brand-custom-js'
          s.textContent = d.custom_js
          document.body.appendChild(s)
        }
      })
      .catch(() => {})
  }, [])

  return <Ctx.Provider value={b}>{children}</Ctx.Provider>
}
