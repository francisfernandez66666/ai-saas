import { createContext, useContext, useEffect, useState } from 'react'

export type Brand = {
  brandName: string
  brandLink: string
  logoUrl: string
  faviconUrl: string
  primaryColor?: string
  secondaryColor?: string
  platformDefault: boolean
}

// 平台级默认品牌（未配置白标时回退到此），避免标题/favicon 出现空白
const DEFAULT: Brand = {
  brandName: '跨山 LexCross',
  brandLink: '',
  logoUrl: '',
  faviconUrl: '',
  platformDefault: true,
}

const Ctx = createContext<Brand>(DEFAULT)

// 消费品牌上下文：各页面用它读取当前租户白标（名称/Logo/主题色等）
export function useBrand() {
  return useContext(Ctx)
}

// 等价于原 frontend/branding.js：按 Host 拉取租户白标并应用到标题/favicon/主题色
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
        // 仅当标题含平台默认名时才替换，避免覆盖已定制标题
        if (document.title.indexOf('跨山 LexCross') > -1) {
          document.title = document.title.replace('跨山 LexCross', brandName)
        }
        if (d.favicon_url) {
          let l = document.querySelector('link[rel="icon"]') as HTMLLinkElement | null
          if (!l) {
            l = document.createElement('link')
            l.rel = 'icon'
            document.head.appendChild(l)
          }
          l.href = d.favicon_url
        }
        if (d.primary_color) {
          document.documentElement.style.setProperty('--brand-primary', d.primary_color)
        }
        if (d.secondary_color) {
          document.documentElement.style.setProperty('--brand-secondary', d.secondary_color)
        }
      })
      .catch(() => {})
  }, [])

  return <Ctx.Provider value={b}>{children}</Ctx.Provider>
}
