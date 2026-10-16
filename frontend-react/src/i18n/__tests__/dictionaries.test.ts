// 词典对齐门禁（P2-14）：en-US 必须与 zh-CN 逐键对齐——缺键/多键/空值/占位符不一致全红。
// 这是全站文案迁移的唯一漂移抓取点：translate() 对缺失键返回 key 本身（不白屏），
// 若没有本门禁，漏翻译的键会以"键名原文"悄悄显示在界面上而无人发现。
// 变异自证：删 enUS 一个键→对齐红；清空一个值→空值红；删一个 {{brand}}→占位符红。
import { describe, expect, it } from 'vitest'
import enUS from '../dictionaries/en-US'
import zhCN from '../dictionaries/zh-CN'
import zhTW from '../dictionaries/zh-TW'
import { LANGUAGES } from '../translator'

const zhKeys = Object.keys(zhCN).sort()
const enKeys = Object.keys(enUS).sort()

/** 取文案里的 {{name}} 占位符集合（排序后比对，顺序无关） */
function placeholders(s: string): string[] {
  return [...s.matchAll(/\{\{(\w+)\}\}/g)].map((m) => m[1]).sort()
}

describe('词典对齐门禁', () => {
  it('LANGUAGES 声明的每一种语言都有词典', () => {
    expect(LANGUAGES).toEqual(['zh-CN', 'en-US', 'zh-TW'])
    expect(typeof zhCN).toBe('object')
    expect(typeof enUS).toBe('object')
    expect(typeof zhTW).toBe('object')
  })

  it('LANGUAGES 中每种语言的键集/占位符都与 zh-CN 对齐', () => {
    const dicts: Record<string, Record<string, string>> = { 'zh-CN': zhCN, 'en-US': enUS, 'zh-TW': zhTW }
    for (const lang of LANGUAGES) {
      const keys = Object.keys(dicts[lang]).sort()
      expect(keys, `${lang} 键集与 zh-CN 不一致（缺: ${zhKeys.filter((k) => !keys.includes(k)).join(', ')}）`).toEqual(zhKeys)
      for (const k of zhKeys) {
        const p = placeholders(dicts[lang][k])
        expect(p, `${lang} 占位符不一致: ${k}`).toEqual(placeholders(zhCN[k as keyof typeof zhCN]))
      }
    }
  })

  it('en-US 与 zh-CN 键集合完全一致（缺键/多键皆红）', () => {
    expect(enKeys, `en-US 缺失: ${zhKeys.filter((k) => !enKeys.includes(k)).join(', ')}`).toEqual(zhKeys)
  })

  it('无空文案（trim 后为空即红，注明键名）', () => {
    for (const [k, v] of [...Object.entries(zhCN), ...Object.entries(enUS)] as [string, string][]) {
      expect(v.trim(), `空文案: ${k}`).not.toBe('')
    }
  })

  it('同键占位符一致（防中文有 {{brand}} 英文漏写，插值丢参）', () => {
    for (const k of zhKeys) {
      const zhP = placeholders(zhCN[k as keyof typeof zhCN])
      const enP = placeholders(enUS[k as keyof typeof enUS])
      expect(enP, `占位符不一致: ${k}（zh=${zhP.join('/')} en=${enP.join('/')}）`).toEqual(zhP)
    }
  })
})
