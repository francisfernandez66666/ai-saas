// i18n 纯函数核心（P2-14，2026-10-14）
//
// 为什么零依赖自研而不引 react-i18next：本站是 React 18.3 + TDesign，react-i18next v17
// 的 peer 要求 React 19（v15 又停留在旧大版本）；且本仓一贯偏好自研单点——lib/money、
// lib/api、P2-6 的 RotatingWriter 都是同一取向（免依赖、可单测、无升级断点）。
// 翻译所需能力很薄：语言归一 + 点分键查找 + {{var}} 插值 + 缺失回退，百行内可完整覆盖。
// React 装配（Provider/hook）见 I18nProvider.tsx 与 useI18n.ts，不放在本文件——保持纯函数可单测。

/** 词典形态：扁平点分键 → 文案。占位符形如 {{name}}。 */
export type Dictionary = Record<string, string>

/** 受支持语言。与 dictionaries/ 下文件名一一对应；新增语言 = 加词典 + 加本数组 + 加 normalizeLanguage 分支。 */
export const LANGUAGES = ['zh-CN', 'en-US'] as const
/** 语言联合类型：LANGUAGES 的类型级对应物。新增语言只需往 LANGUAGES 加一项，本类型自动跟随。 */
export type Language = (typeof LANGUAGES)[number]

/** 出厂默认语言：中文（本站主市场；用户显式选择或浏览器偏好会覆盖它）。 */
export const DEFAULT_LANGUAGE: Language = 'zh-CN'

/**
 * 语言自称（endonym）：语言切换器里语言名按各自语言书写（"中文"/"English"），不随界面翻译——
 * 否则用户看不懂要切到哪种语言。属数据契约，故放 .ts（i18n 门禁只扫 .tsx），不进词典。
 */
export const LANG_ENDONYM: Record<Language, string> = {
  'zh-CN': '中文',
  'en-US': 'English',
}

/**
 * 把任意语言标签收敛到受支持集合。
 * zh* → zh-CN（含 zh-TW/zh-Hans 等变体；繁体中文本站暂无专门词典，落中文普通话版，
 * 后续可加 zh-TW 词典并在此分支细化）；en* → en-US；其余（ja/fr…）→ null 交回默认。
 */
export function normalizeLanguage(raw: string | null | undefined): Language | null {
  if (!raw) return null
  const lower = raw.toLowerCase()
  if (lower.startsWith('zh')) return 'zh-CN'
  if (lower.startsWith('en')) return 'en-US'
  return null
}

/**
 * 语言决策链：localStorage 显式选择 > navigator 浏览器偏好 > 出厂默认。
 * @param stored - localStorage 里用户上次选的语言（可能为 null/非法值）
 * @param navigatorLang - navigator.language（可能为 undefined）
 */
export function detectLanguage(
  stored: string | null | undefined,
  navigatorLang: string | null | undefined,
): Language {
  return normalizeLanguage(stored) ?? normalizeLanguage(navigatorLang) ?? DEFAULT_LANGUAGE
}

/**
 * 插值：把 {{name}} 替换为 vars[name]。
 * 未提供的占位符**原样保留**而非清空——文案缺参后语义断裂（"你有 {{count}} 条待办"变成
 * "你有 条待办"）比留着占位符更糟，留着至少能看出缺参。
 */
export function interpolate(template: string, vars?: Record<string, string | number>): string {
  if (!vars) return template
  // 同时支持双花括号 {{name}}（规范写法）与单花括号 {name}（历史键混用），
  // 避免存量单花括号占位符在运行时不被替换、把「{n}」原样吐到界面。
  return template.replace(/\{\{?(\w+)\}?\}/g, (whole, name: string) => {
    const v = vars[name]
    return v === undefined || v === null ? whole : String(v)
  })
}

/**
 * 查键翻译：扁平点分键直查；键缺失或值非字符串时**原样返回 key**。
 * 不返回空串——空串会让界面静默缺一块而无人发现；返回 key 既保证不白屏，
 * 又让 dictionaries.test.ts 的键对齐门禁成为唯一的漂移抓取点。
 */
export function translate(dict: Dictionary, key: string, vars?: Record<string, string | number>): string {
  const template = dict[key]
  return typeof template === 'string' ? interpolate(template, vars) : key
}
