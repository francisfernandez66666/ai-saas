// i18n 非 React 消费者入口（P2-14 收尾，2026-10-16）。
//
// 要解决的事：lib/api.ts 的 toastError 在**模块作用域**里解析文案——错误码表
// ERROR_CODE_MESSAGES 是模块级常量，toastError 本身也不是组件，拿不到 useI18n()
// （React 上下文只在渲染树内可达）。没有这个入口，api.ts 的 15 处用户可见文案
// （错误码 toast + 网络/解析失败兜底）只能留硬编码中文，是 P2-14 收口后
// .ts 数据层仅剩的面向用户文案。
//
// 实现：一份"当前语言"镜像 + 同一套 translate 纯函数。镜像由 I18nProvider 在语言
// 变化时同步（setStandaloneLang）；缺同步时保持上一次值（初始中文）——与 context.ts
// 的 FALLBACK 同取向：没说错话 > 白屏。语言→词典表也收在这里，Provider 复用同一份，
// 新增语言只改一处（translator.LANGUAGES + dictionaries/<lang>.ts + 本表）。
import enUS from './dictionaries/en-US'
import zhCN from './dictionaries/zh-CN'
import zhTW from './dictionaries/zh-TW'
import { translate, type Dictionary, type Language } from './translator'
import type { I18nKey, I18nVars } from './context'

/** 语言 → 词典。新增语言：加 dictionaries/<lang>.ts + translator.LANGUAGES 加码 + 本表登记。 */
export const DICTS: Record<Language, Dictionary> = {
  'zh-CN': zhCN as unknown as Dictionary,
  'en-US': enUS as unknown as Dictionary,
  'zh-TW': zhTW as unknown as Dictionary,
}

/** 当前语言镜像：Provider 经 setStandaloneLang 同步；未同步时为出厂中文。 */
let currentLang: Language = 'zh-CN'

/** Provider 的语言 effect 里调用（初始挂载与每次切语言）。非 React 代码路径不直接调它。 */
export function setStandaloneLang(lang: Language): void {
  currentLang = lang
}

/** 读当前镜像语言（测试/诊断用；业务代码用 t）。 */
export function getStandaloneLang(): Language {
  return currentLang
}

/** 模块级翻译：供 api.ts 等非 React 代码路径使用。键缺失时行为与 t() 一致（返回 key）。 */
export function t(key: I18nKey, vars?: I18nVars): string {
  return translate(DICTS[currentLang], key, vars)
}
