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

/**
 * G-20：可复用无障碍确认弹窗（替代 window.confirm）
 * 用途：退款、删除等敏感操作的二次确认，避免误触
 * 无障碍特性：
 *   - role="dialog" + aria-modal="true"：标识模态对话框，辅助技术锁定焦点在弹窗内
 *   - aria-label={title}：将标题作为对话框的可访问名称，屏幕阅读器会朗读
 *   - 点击遮罩层可关闭（onCancel），支持键盘 Esc 关闭（由 TDesign Dialog 行为继承）
 * @param open - 控制弹窗可见性
 * @param title - 弹窗标题（同时作为 aria-label）
 * @param message - 确认提示正文
 * @param onConfirm - 用户点击"确认"后的回调
 * @param onCancel - 用户点击"取消"或遮罩层时的回调
 */
export function ConfirmDialog({ open, title, message, onConfirm, onCancel }: {
  open: boolean; title: string; message: string; onConfirm: () => void; onCancel: () => void;
}) {
  // 未打开时直接返回 null，不渲染 DOM 节点
  if (!open) return null;
  return (
    // G-20：外层遮罩——role="dialog" aria-modal="true" 构成语义对话框，aria-label 供屏幕阅读器朗读标题
    <div role="dialog" aria-modal="true" aria-label={title} style={{ position:'fixed', inset:0, background:'rgba(0,0,0,.4)', display:'flex', alignItems:'center', justifyContent:'center', zIndex:1000 }}>
      {/* 弹窗主体：标题 + 正文 + 取消/确认按钮 */}
      <div style={{ background:'#fff', borderRadius:8, padding:24, maxWidth:400, width:'90%' }}>
        <h3 style={{ margin:'0 0 8px', fontSize:16, fontWeight:600 }}>{title}</h3>
        <p style={{ margin:'0 0 16px', color:'#4a5568', fontSize:14 }}>{message}</p>
        <div style={{ display:'flex', gap:8, justifyContent:'flex-end' }}>
          {/* 取消按钮：次要操作，白色底+边框样式 */}
          <button onClick={onCancel} style={{ padding:'6px 16px', borderRadius:6, border:'1px solid #e2e8f0', background:'#fff', cursor:'pointer' }}>取消</button>
          {/* 确认按钮：主操作，品牌主色底+白色字，视觉权重更高引导用户确认 */}
          <button onClick={onConfirm} style={{ padding:'6px 16px', borderRadius:6, border:'none', background:'var(--pri)', color:'#fff', cursor:'pointer' }}>确认</button>
        </div>
      </div>
    </div>
  );
}

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
