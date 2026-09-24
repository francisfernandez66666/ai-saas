// 金额换算与商机文案单测（商机批 · 前端批3，2026-09-24）。
//
// 这两个 lib 是后台看板与顾问台共用的单点，测的是"单点本身对不对"，
// 页面层再测一遍没有意义——所以这里把最容易翻车的四条钉死：
//   1. 元↔分往返必须精确回到原值（浮点除法回不来：0.29 元 → 28.999… 分）；
//   2. 非法形态一律回 null，由调用方拒提交，**不许静默当 0**（当 0 就是把客户的报价改成免费）；
//   3. 展示串与输入串分工：展示带千分位、输入不带（把展示串回填输入框，下一次提交就带着逗号去了）；
//   4. 原因码表覆盖后端全部拒绝码——漏一个码，页面就会退回后端那句英文技术信息。
import { describe, expect, it } from 'vitest'

import { fenToYuan, fenToYuanInput, yuanToFen } from '../money'
import { DEAL_REASON_CODES, dealReasonText } from '../dealReasons'

describe('lib/money 元分换算', () => {
  it('往返精确：一分钱的误差都不能有', () => {
    for (const cents of [0, 1, 99, 100, 2900, 1280050, 2114500, 999999999]) {
      expect(yuanToFen(fenToYuanInput(cents))).toBe(cents)
    }
    // 用户手输的形态也要能原样回去（含逗号与空白，因为输入框里可能粘来带格式的钱数）
    expect(yuanToFen('12,800.50')).toBe(1280050)
    expect(yuanToFen(' 0.29 ')).toBe(29)
  })

  it('非法形态回 null，绝不静默当 0', () => {
    for (const bad of ['', '  ', 'abc', '1.', '1.234', '.5', '1e3', 'NaN', '1.2.3', '12 800']) {
      expect(yuanToFen(bad)).toBeNull()
    }
    // 千分位是被允许的（从别处粘来的钱数就长这样）：去掉逗号后必须是纯数字形态才收
    expect(yuanToFen('12,800.50')).toBe(1280050)
    // 负数是合法形态（金额正负由业务判据拒），回 null 会把"负数"这一类错误码丢掉
    expect(yuanToFen('-100.00')).toBe(-10000)
  })

  it('展示串带千分位、输入串不带，两者不能互用', () => {
    expect(fenToYuan(1280050)).toBe('12,800.50')
    expect(fenToYuanInput(1280050)).toBe('12800.50')
    expect(fenToYuan(-1280050)).toBe('-12,800.50')
    // 反证：展示串带逗号，直接回填输入框再提交会带着千分位出去——所以必须有两个函数
    expect(fenToYuan(1280050)).not.toBe(fenToYuanInput(1280050))
  })
})

describe('lib/dealReasons 商机原因码文案', () => {
  it('码表覆盖后端全部拒绝码，且每格都有中文话术', () => {
    expect(DEAL_REASON_CODES.length).toBeGreaterThan(20)
    for (const code of DEAL_REASON_CODES) {
      const text = dealReasonText(code, '兜底')
      expect(text).not.toBe('兜底')
      expect(/[一-龥]/.test(text)).toBe(true)
    }
  })

  it('未知码与空码回落兜底话术（后端加码时页面不能空白）', () => {
    expect(dealReasonText('some_future_code', '后端说的原话')).toBe('后端说的原话')
    expect(dealReasonText(undefined, '原话')).toBe('原话')
    expect(dealReasonText('', '原话')).toBe('原话')
  })
})
