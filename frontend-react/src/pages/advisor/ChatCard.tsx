// 客户详情·聊天记录卡：消息气泡列表 + 顶部动作条（立即回复/接管/转回 AI/自动回复开关）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）
// P2-84：消息由父组件按「已展示 ID 集合」增量追加后整体下发，此处只负责渲染
import type { RefObject } from 'react'
import type { Msg } from '../../types'

export default function ChatCard({ msgs, chatRef, convId, convMode, aiOn, onClearDelay, onTakeover, onTransferBackAI, onToggleAI }: {
  msgs: Msg[]
  chatRef: RefObject<HTMLDivElement>
  convId: number | null
  convMode: string
  aiOn: boolean
  onClearDelay: () => void
  onTakeover: () => void
  onTransferBackAI: () => void
  onToggleAI: () => void
}) {
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
        <b style={{ fontSize: 13 }}>聊天记录</b>
        <div style={{ display: 'flex', gap: 6, alignItems: 'center' }}>
          {/* §八-6 C 块：立即回复——解除合并窗口延迟，积压消息马上发出（有会话时常驻的轻量按钮） */}
          {convId != null && <button onClick={onClearDelay} title="解除等待，让积压消息立即发出" style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', border: '1px solid #e2e8f0', background: '#fff', color: '#4a5568' }}>立即回复</button>}
          {/* E2：一键接管对话（AI 暂停），与自动回复开关联动 */}
          <button onClick={onTakeover} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', border: '1px solid var(--pri)', background: '#fff', color: 'var(--pri)' }}>接管</button>
          {/* §八-6 C 块：转回 AI——仅人工接管态显示，恢复自动回复并刷新聊天 */}
          {convMode === 'human' && <button onClick={onTransferBackAI} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', border: '1px solid var(--pri)', background: 'var(--pri)', color: '#fff' }}>转回 AI</button>}
          {/* G-20：AI回复开关 aria-label 动态切换文案，role="button" + tabIndex + onKeyDown 支持键盘操作 */}
          <span onClick={onToggleAI} role="button" tabIndex={0} aria-label={aiOn ? '关闭自动回复' : '开启自动回复'} onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') onToggleAI() }} style={{ fontSize: 12, padding: '4px 10px', borderRadius: 8, cursor: 'pointer', background: aiOn ? '#d1fae5' : '#f3f4f6', color: aiOn ? '#047857' : '#6b7280' }}>自动回复{aiOn ? '开' : '关'}</span>
        </div>
      </div>
      <div ref={chatRef} style={{ maxHeight: 320, overflowY: 'auto', display: 'flex', flexDirection: 'column', gap: 8 }}>
        {msgs.map((m, i) => <div key={i} style={{ alignSelf: m.sender_type === 'human' ? 'flex-end' : 'flex-start', background: m.sender_type === 'human' ? 'var(--pri)' : m.sender_type === 'ai' ? '#ecfdf5' : '#f1f5f9', color: m.sender_type === 'human' ? '#fff' : '#1f2937', padding: '8px 12px', borderRadius: 10, maxWidth: '80%', fontSize: 13 }}>{m.content}</div>)}
      </div>
    </div>
  )
}
