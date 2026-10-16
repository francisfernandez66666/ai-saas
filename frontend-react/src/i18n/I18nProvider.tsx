// I18nProvider（P2-14）：语言状态单一事实源。
// 职责：初始语言决策（localStorage > navigator > 出厂中文）→ 选择后写回 localStorage 并同步
// <html lang>（无障碍/SEO）→ 按当前语言取词典包出 t() 下发。
// 只导出组件本身——hook 在 useI18n.ts、context 在 context.ts（react-refresh 规则）。
import { useEffect, useMemo, useState } from 'react'
import { I18nContext, type I18nKey, type I18nVars } from './context'
import { DICTS, setStandaloneLang } from './standalone'
import { detectLanguage, translate, type Language } from './translator'

/** 语言选择持久化键（登录态键，与 TOKEN_KEY 同族：清登录态的路径不必清它——语言是设备偏好不是身份） */
export const LANG_STORAGE_KEY = 'scrm_lang'

/** 读 localStorage 里的语言选择；隐私模式/禁用存储时返回 null（不抛，走浏览器偏好） */
function readStoredLang(): string | null {
  try {
    return localStorage.getItem(LANG_STORAGE_KEY)
  } catch {
    return null
  }
}

/** 语言 Provider：包在应用根（main.tsx），全站 t() 经此下发。 */
export function I18nProvider({ children }: { children: React.ReactNode }) {
  // 初始语言在初始化器里就推给 standalone 镜像：应用启动早期的请求失败 toast
  // （如 401 登出）发生在首个 effect 之前，不能等挂载后才同步语言。
  // 幂等操作，StrictMode 双调用无害。
  const [lang, setLang] = useState<Language>(() => {
    const initial = detectLanguage(readStoredLang(), navigator.language)
    setStandaloneLang(initial)
    return initial
  })

  useEffect(() => {
    try {
      localStorage.setItem(LANG_STORAGE_KEY, lang)
    } catch {
      /* 隐私模式写不进去：保持内存态生效，不阻断 */
    }
    // <html lang> 同步：屏幕阅读器与搜索引擎按它判定页面语言
    document.documentElement.lang = lang
    // 同步给非 React 消费者（lib/api.ts 的 toastError 在模块作用域解析文案，
    // 拿不到 hook——镜像放在 i18n/standalone.ts，这里在语言变化时推过去）
    setStandaloneLang(lang)
  }, [lang])

  const value = useMemo(() => {
    const dict = DICTS[lang]
    return {
      lang,
      setLang,
      t: (key: I18nKey, vars?: I18nVars) => translate(dict, key, vars),
    }
  }, [lang])

  return <I18nContext.Provider value={value}>{children}</I18nContext.Provider>
}
