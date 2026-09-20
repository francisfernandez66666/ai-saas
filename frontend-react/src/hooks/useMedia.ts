// useMedia.ts：视口断点 hook（P1-10，2026-09-20 审计批二）
// 背景：index.css 无任何 @media 规则，桌面管理台（Admin/SuperAdmin）固定 200px 左栏在
// 390px 手机上挤压正文，AGENTS.md"桌面页均已移动端自适应"与实况漂移。
// 用途：宽度低于断点时管理台把左侧 Aside 折叠为顶栏下拉菜单，达成窄屏可达性。
import { useEffect, useState } from 'react'

/** 当前视口宽度是否 ≤ maxPx（matchMedia 实时判定，缺失时退回 innerWidth；resize 自动复评）。 */
export function useIsMobile(maxPx = 900): boolean {
  const query = `(max-width: ${maxPx}px)`
  const read = () => {
    if (typeof window === 'undefined') return false
    if (typeof window.matchMedia === 'function') return window.matchMedia(query).matches
    return window.innerWidth <= maxPx
  }
  const [mobile, setMobile] = useState(read)
  useEffect(() => {
    const onResize = () => setMobile(read())
    if (typeof window.matchMedia === 'function') {
      const mq = window.matchMedia(query)
      if (typeof mq.addEventListener === 'function') {
        mq.addEventListener('change', onResize)
        return () => mq.removeEventListener('change', onResize)
      }
      // 老版 Safari 只有 addListener（当前目标浏览器已罕见，保底不报错）
      mq.addListener(onResize)
      return () => mq.removeListener(onResize)
    }
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [query])
  return mobile
}
