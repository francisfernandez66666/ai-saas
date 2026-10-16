// i18n 纯函数核心单测（P2-14）。断言做过变异自证：翻转 normalizeLanguage 分支 /
// 让 translate 缺失键返回空串 / 让 interpolate 清空未提供占位符，各自转红后还原。
import { describe, expect, it } from 'vitest'
import {
  DEFAULT_LANGUAGE,
  LANG_ENDONYM,
  LANGUAGES,
  detectLanguage,
  interpolate,
  normalizeLanguage,
  translate,
  type Dictionary,
} from '../translator'

describe('normalizeLanguage', () => {
  it('zh 简体系归一到 zh-CN（含地区/大小写变体）', () => {
    for (const raw of ['zh-CN', 'zh', 'zh-Hans', 'ZH-cn']) {
      expect(normalizeLanguage(raw), raw).toBe('zh-CN')
    }
  })

  it('zh 繁体变体归一到 zh-TW（台湾/香港/澳门/Hant）', () => {
    for (const raw of ['zh-TW', 'zh-Hant', 'zh-HK', 'zh-MO']) {
      expect(normalizeLanguage(raw), raw).toBe('zh-TW')
    }
  })

  it('en 系归一到 en-US', () => {
    for (const raw of ['en-US', 'en', 'EN-gb']) {
      expect(normalizeLanguage(raw), raw).toBe('en-US')
    }
  })

  it('不受支持的语言/空值返回 null（交回默认，不猜）', () => {
    for (const raw of ['ja', 'fr-FR', '', undefined, null]) {
      expect(normalizeLanguage(raw), String(raw)).toBeNull()
    }
  })
})

describe('detectLanguage 决策链', () => {
  it('localStorage 显式选择优先于浏览器偏好', () => {
    expect(detectLanguage('en-US', 'zh-CN')).toBe('en-US')
  })

  it('无存储选择时取浏览器偏好', () => {
    expect(detectLanguage(null, 'en-US')).toBe('en-US')
  })

  it('存储值非法 → 回落 navigator，再缺 → 出厂中文', () => {
    expect(detectLanguage('ja', 'zh-Hans')).toBe('zh-CN') // 非法存储 → 回落 navigator（zh-Hans 归 zh-CN）
    expect(detectLanguage(null, 'zh-TW')).toBe('zh-TW') // navigator 为受支持的繁体 → 直接采用
    expect(detectLanguage(null, null)).toBe(DEFAULT_LANGUAGE)
    expect(detectLanguage(undefined, undefined)).toBe('zh-CN')
  })

  it('默认值必须落在 LANGUAGES 声明集合内（加语言漏改数组会红）', () => {
    expect(LANGUAGES).toContain(DEFAULT_LANGUAGE)
  })
})

describe('interpolate', () => {
  it('替换 {{name}} 占位符（含数字自动字符串化）', () => {
    expect(interpolate('你好 {{name}}，{{n}} 条待办', { name: '阿玲', n: 3 })).toBe('你好 阿玲，3 条待办')
    expect(interpolate('{{n}}/{{z}}', { n: 0, z: 12 })).toBe('0/12')
  })

  it('未提供的占位符原样保留——缺参数义断裂比留着占位符更糟', () => {
    expect(interpolate('你有 {{count}} 条', {})).toBe('你有 {{count}} 条')
    expect(interpolate('你有 {{count}} 条', { other: 1 })).toBe('你有 {{count}} 条')
  })

  it('无 vars 时原样返回', () => {
    expect(interpolate('{{a}} 不变')).toBe('{{a}} 不变')
  })

  // P2-14 回归：存量词典里单/双花括号混用（{n} 与 {{n}}），旧实现正则只认双括号，
  // 单括号键在运行时不被替换、把字面量「{n}」原样吐到界面。变异自证：把正则改回
  // /\{\{(\w+)\}\}/g 后下面两条立刻转红。
  it('单花括号 {name} 同样替换（混写兼容，不是可选项）', () => {
    expect(interpolate('剩 {n}', { n: 5 })).toBe('剩 5')
    expect(interpolate('共 {total} 条 · 第 {page} 页', { total: 30, page: 2 })).toBe('共 30 条 · 第 2 页')
    expect(interpolate('{n}% 档', { n: 80 })).toBe('80% 档')
  })

  it('同一串里单双花括号混用都要替换', () => {
    expect(interpolate('{a} 和 {{b}}', { a: 1, b: 2 })).toBe('1 和 2')
  })
})

describe('LANG_ENDONYM 语言自称', () => {
  it('每个受支持语言都有非空字符串自称', () => {
    for (const l of LANGUAGES) {
      expect(typeof LANG_ENDONYM[l], l).toBe('string')
      expect(LANG_ENDONYM[l].length, l).toBeGreaterThan(0)
    }
  })

  // 语言名按各自语言书写且不随界面翻译：切到英文界面时选项仍显示「中文」，
  // 否则用户看不懂要切哪种语言。属数据契约，不在词典里。
  it('自称恒定不随界面语言漂移', () => {
    expect(LANG_ENDONYM['zh-CN']).toBe('中文')
    expect(LANG_ENDONYM['en-US']).toBe('English')
  })
})

describe('translate', () => {
  const dict: Dictionary = { 'a.b': '值', plain: '平' }

  it('点分键命中并插值', () => {
    expect(translate(dict, 'a.b')).toBe('值')
    expect(translate(dict, 'plain')).toBe('平')
    expect(translate({ x: '{{v}}!' }, 'x', { v: 7 })).toBe('7!')
  })

  it('缺失键原样返回 key——不返回空串（空串会让界面静默缺一块）', () => {
    expect(translate(dict, 'a.missing')).toBe('a.missing')
    expect(translate(dict, 'nope')).toBe('nope')
  })

  it('键存在但值非字符串（词典被写坏）→ 返回 key 兜底', () => {
    expect(translate({ bad: 42 } as unknown as Dictionary, 'bad')).toBe('bad')
  })
})
