// 金额换算的唯一口径（商机批 · 前端批3，2026-09-24 从 DealsTab 提出，供后台看板与顾问台共用）。
//
// 为什么单独成文件：报价是钱，两个页面各写一遍"元↔分"就等于养出两套四舍五入，
// 迟早出现"后台显示 12,800.50、顾问台提交 1280049"。规则只有一条——
// **界面输入框里是元，中间步骤与提交体里全是分（整数）**，任何一次浮点往返都不许发生。

/** 分 → 展示用元字符串（千分位、两位小数）。只用于显示，拿它再参与计算就是第二次浮点往返。 */
export function fenToYuan(cents: number): string {
  const neg = cents < 0
  const abs = Math.abs(cents)
  const yuan = Math.floor(abs / 100)
  const rest = String(abs % 100).padStart(2, '0')
  const grouped = String(yuan).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  return `${neg ? '-' : ''}${grouped}.${rest}`
}

/** 分 → 输入框里的元字符串（不带千分位）。与 yuanToFen 互为逆运算：
 * 用 `cents / 100` 会得到浮点数，回填进输入框再提交就可能出现 0.28999999999999998。 */
export function fenToYuanInput(cents: number): string {
  const neg = cents < 0
  const abs = Math.abs(cents)
  return `${neg ? '-' : ''}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`
}

/**
 * 分 → 价格展示用元字符串（整元不带小数，有分才带两位）。
 *
 * 为什么需要第三枚而不是直接 `toFixed(0)`：定价卡片过去就是 `(cents / 100).toFixed(0)`，
 * ¥99.50 会被**四舍五入显示成 ¥100**，而下单按分实扣——页面价与扣款额不一致是投诉源，
 * 且没人能从页面上看出被舍掉的那 5 毛。本函数不猜：整元就写整元，有分就把分写出来。
 * 仍走字符串拆位，不做浮点除。
 */
export function fenToYuanCompact(cents: number): string {
  const n = Number.isFinite(cents) ? Math.trunc(cents) : 0
  const rest = Math.abs(n) % 100
  return rest === 0 ? fenToYuan(n).replace(/\.00$/, '') : fenToYuan(n)
}

/**
 * 元 → 分。**用字符串拆位而不是 `* 100`**：`Number('0.29') * 100` 在浮点里是
 * 28.999999999999996，四舍五入救得回来，但截断就把客户的报价单少写一分钱。
 * 返回 null 表示形态非法（交给调用方拒提交，而不是静默当 0）。
 */
export function yuanToFen(raw: string): number | null {
  const s = String(raw ?? '').trim().replace(/,/g, '')
  if (s === '') return null
  if (!/^-?\d+(\.\d{1,2})?$/.test(s)) return null
  const neg = s.startsWith('-')
  const [intPart, decPart = ''] = (neg ? s.slice(1) : s).split('.')
  const fen = Number(intPart) * 100 + Number((decPart + '00').slice(0, 2))
  if (!Number.isFinite(fen)) return null
  return neg ? -fen : fen
}
