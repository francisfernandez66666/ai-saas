/**
 * jsonMode.ts：JSON 类型配置编辑器的形态判定纯函数（供 Admin.tsx JsonEditor 复用）
 *
 * 本轮修复背景（2026-09-08 React error #31）：
 * 策略引擎配置 `anchor_weights` 的值是"对象数组"
 * （如 [{"intent_score_weight":-2,...}, {...}]），原 JsonEditor 只识别数字数组/字符串数组，
 * 把对象数组里的每个对象当字符串 `{s}` 渲染 → 抛 "Objects are not valid as a React child"。
 * 这里把形态判定抽成纯函数，vitest 直接覆盖，组件侧仅按结果分发渲染控件。
 */

/** JSON 编辑形态：数字数组 / 字符串数组 / 对象数组 / 非数组（纯文本兜底） */
export type JsonMode = 'numberArray' | 'stringArray' | 'objectArray' | 'plain'

/**
 * resolveJsonMode 解析 JSON 字符串的渲染形态
 * @param value 配置值原串（可能非法 JSON）
 * @returns 形态标识；非法 JSON 或非数组一律回退 'plain'
 */
export function resolveJsonMode(value: string): JsonMode {
  let parsed: unknown
  try {
    parsed = JSON.parse(value)
  } catch {
    return 'plain'
  }
  if (!Array.isArray(parsed)) return 'plain'
  if (parsed.length === 0) return 'stringArray'
  const first = typeof parsed[0]
  if (first === 'number') return 'numberArray'
  if (first === 'object') return 'objectArray'
  return 'stringArray'
}