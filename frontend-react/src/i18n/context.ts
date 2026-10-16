// i18n Context 与类型（P2-14）。默认值 = 中文（与 branding.tsx 同款取向：缺 Provider 时
// 降级到中文而不是抛错白屏——Provider 漏挂的最坏结果该是"没说错话"，不是"整棵树崩"）。
// 拆开而不并进 I18nProvider.tsx：react-refresh/only-export-components 要求组件文件
// 只导出组件（混出 hook/context 会刷 warning、顶高 lint:ci 配额）。
import { createContext } from 'react'
import zhCN from './dictionaries/zh-CN'
import { translate, type Dictionary, type Language } from './translator'

/** 翻译键全集：以中文词典为源真相，写错键名编译期报错 */
export type I18nKey = keyof typeof zhCN

/** t() 的插值变量：字符串或数字（Number/自动 String 化） */
export type I18nVars = Record<string, string | number>

/** i18n 上下文值：当前语言、切语言函数、翻译函数 */
export type I18nValue = {
  lang: Language
  setLang: (lang: Language) => void
  t: (key: I18nKey, vars?: I18nVars) => string
}

/** 缺 Provider 时的降级值：中文 + 直查中文词典（setLang 暂态无效，重新挂载后由 Provider 接管） */
const FALLBACK: I18nValue = {
  lang: 'zh-CN',
  setLang: () => {},
  t: (key, vars) => translate(zhCN as unknown as Dictionary, key, vars),
}

/** i18n Context：由 I18nProvider 注入当前语言与 t()；消费方一律走 useI18n()，不直接 import 本对象 */
export const I18nContext = createContext<I18nValue>(FALLBACK)
