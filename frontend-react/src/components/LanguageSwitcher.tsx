// LanguageSwitcher（P2-14）：语言切换下拉。语言名用各自语言书写（"中文"/"English"），
// 这是通行做法——切换器里的语言名不该被翻译（否则看不懂要切到什么）。
// 只导出组件；切后经 I18nProvider 写 localStorage 并全站生效。
import { Select } from 'tdesign-react'
import { useI18n } from '../i18n/useI18n'
import { LANGUAGES, LANG_ENDONYM, type Language } from '../i18n/translator'

/** 语言切换器：紧凑尺寸，放登录卡/顶栏等窄位。 */
export function LanguageSwitcher() {
  const { lang, setLang } = useI18n()
  return (
    <Select
      value={lang}
      onChange={(v) => setLang(v as Language)}
      // 语言名按惯例用**各自的自称**（endonym），不随界面语言切换——切到英文时这一栏仍显示「中文」
      options={LANGUAGES.map((l) => ({ label: LANG_ENDONYM[l], value: l }))}
      size="small"
      style={{ width: 116 }}
    />
  )
}
