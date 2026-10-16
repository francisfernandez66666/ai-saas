// useI18n hook（P2-14）。只导出 hook——组件在 I18nProvider.tsx（react-refresh 规则）。
// 与 useBrand 同款语义：缺 Provider 不抛错，降级中文（见 context.ts FALLBACK）。
import { useContext } from 'react'
import { I18nContext, type I18nValue } from './context'

/** 读取当前语言与翻译函数：`const { t, lang, setLang } = useI18n()` */
export function useI18n(): I18nValue {
  return useContext(I18nContext)
}
