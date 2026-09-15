// E8 收口(2026-09-14)：统一确认/提示弹窗 helper。
// 全站原生 confirm()/alert()/prompt() 破坏品牌一致性与可访问性（G-20），
// 且无法样式定制。改用 TDesign DialogPlugin 实现，暴露 Promise 风格 API，
// 调用点仅需把 `if (!confirm(msg)) return` 改成 `if (!(await confirmDialog(msg))) return`。
import { DialogPlugin, MessagePlugin } from 'tdesign-react'

/** confirmDialog 弹出确认框，用户点确认返回 true，取消/关闭返回 false。 */
export function confirmDialog(message: string, title = '确认操作'): Promise<boolean> {
  return new Promise((resolve) => {
    const dlg = DialogPlugin.confirm({
      header: title,
      body: message,
      theme: 'warning',
      onConfirm: () => { dlg.hide(); resolve(true) },
      onCancel: () => { dlg.hide(); resolve(false) },
      onClose: () => { dlg.hide(); resolve(false) },
    })
  })
}

/** uiAlert 弹出提示框（替代原生 alert），点击确定后 resolve。 */
export function uiAlert(message: string, title = '提示'): Promise<void> {
  return new Promise((resolve) => {
    const dlg = DialogPlugin.alert({
      header: title,
      body: message,
      onConfirm: () => { dlg.hide(); resolve() },
      onClose: () => { dlg.hide(); resolve() },
    })
  })
}

/** toast 轻提示（成功/警告/错误），替代零散 alert 反馈。 */
export const toast = {
  success: (m: string) => MessagePlugin.success(m),
  warning: (m: string) => MessagePlugin.warning(m),
  error: (m: string) => MessagePlugin.error(m),
  info: (m: string) => MessagePlugin.info(m),
}
