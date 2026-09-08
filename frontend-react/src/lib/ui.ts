/**
 * ui.ts：全站统一设计令牌（UI 统一）
 * 收敛各页面散落的取色：主色统一走 --pri / --td-brand-color（靛蓝 #4f46e5），
 * 语义色固定为 success/warning/danger，背景/边框/文字统一走灰阶。
 * 页面禁止再硬编码其他彩色（#16a34a、#6366f1、#ea580c 等），如需强调色用下方令牌。
 */

// 主色（与 index.css :root --pri 对齐，TDesign 品牌色）
export const PRI = '#4f46e5'
export const PRI_LIGHT = '#eef2ff'
export const PRI_LIGHT_HOVER = '#e0e7ff'

// 语义色（仅表达状态含义）
export const SUCCESS = '#16a34a'
export const WARNING = '#d97706'
export const DANGER = '#dc2626'

// 中性灰阶（背景/边框/文字）
export const BG = '#f5f7fa'
export const CARD = '#ffffff'
export const BORDER = '#e5e7eb'
export const TEXT = '#1f2937'
export const TEXT_SUB = '#6b7280'
export const TEXT_MUTED = '#9ca3af'

// 圆角 / 阴影
export const RADIUS = 12
export const SHADOW = '0 1px 2px rgba(16,24,40,.06), 0 1px 3px rgba(16,24,40,.1)'

// 阶段徽标（客户旅程）统一配色：文本色 + 背景
export const STAGE_BADGE: Record<string, { text: string; bg: string }> = {
  ai_connected: { text: '#6b7280', bg: '#f3f4f6' },
  human_connected: { text: '#1d4ed8', bg: '#dbeafe' },
  lead_captured: { text: '#0e7490', bg: '#cffafe' },
  arrived: { text: '#047857', bg: '#d1fae5' },
  ordered: { text: '#c2410c', bg: '#ffedd5' },
  delivered: { text: '#b91c1c', bg: '#fee2e2' },
  lost: { text: '#4b5563', bg: '#e5e7eb' },
}
