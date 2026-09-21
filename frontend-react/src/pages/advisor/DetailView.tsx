// 客户详情·全屏覆盖层（P0-3：详情是 fixed 覆盖层，任意底部 Tab 下点开客户都应显示，返回只清 detailId）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）：本文件只做骨架与分区编排，各区块已拆为独立卡片组件
import { Button, Input, Tag } from 'tdesign-react'
import type { RefObject } from 'react'
import type { Customer, Detail, Msg } from '../../types'
import { H, STAGE_LABELS, type ChannelContext, type Recommend, type TestDrive } from './shared'
import ChannelCard from './ChannelCard'
import RecommendCard from './RecommendCard'
import TestDriveCard from './TestDriveCard'
import ChatCard from './ChatCard'
import HistoryTimeline from './HistoryTimeline'
import RatingCard from './RatingCard'

export default function DetailView({ detail, chanCtx, chanKey, jsSdkOk, onBack, onEdit, onEditTags,
  onNewFollowup, onStage, onNewTestDrive, rec, onFillInput,
  testDrives, onEditTestDrive, onSetTDStatus,
  msgs, chatRef, convId, convMode, aiOn, onClearDelay, onTakeover, onTransferBackAI, onToggleAI,
  tlConv, tlMsgs, onToggleTimeline, rate, onRate, onSubmitRating,
  input, onInput, onSend }: {
  detail: Detail | null
  chanCtx: ChannelContext | null
  chanKey: { corpid: string; external_userid: string } | null
  jsSdkOk: boolean | null
  onBack: () => void
  onEdit: () => void
  onEditTags: () => void
  onNewFollowup: () => void
  onStage: () => void
  onNewTestDrive: () => void
  rec: Recommend | null
  onFillInput: (s: string) => void
  testDrives: TestDrive[]
  onEditTestDrive: (td: TestDrive) => void
  onSetTDStatus: (td: TestDrive, s: string) => void
  msgs: Msg[]
  chatRef: RefObject<HTMLDivElement>
  convId: number | null
  convMode: string
  aiOn: boolean
  onClearDelay: () => void
  onTakeover: () => void
  onTransferBackAI: () => void
  onToggleAI: () => void
  tlConv: number | null
  tlMsgs: Msg[]
  onToggleTimeline: (cid: number) => void
  rate: { score: number; comment: string }
  onRate: (r: { score: number; comment: string }) => void
  onSubmitRating: () => void
  input: string
  onInput: (v: string) => void
  onSend: () => void
}) {
  const c: Customer | undefined = detail?.customer
  return (
    <div style={{ position: 'fixed', inset: 0, background: '#f5f7fa', zIndex: 20, maxWidth: 480, margin: '0 auto' }}>
      <header style={{ background: 'var(--pri)', color: '#fff', padding: '14px 16px', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        {/* G-20：aria-label 标注返回按钮，辅助技术可识别导航操作 */}
        <button onClick={onBack} aria-label="返回客户列表" style={{ background: 'none', border: 'none', color: '#fff', fontSize: 16 }}>←</button>
        <span style={{ fontWeight: 600 }}>{H(c?.name)}</span>
        {/* G-20：aria-label 标注编辑按钮，辅助技术可识别操作意图 */}
        <button onClick={onEdit} aria-label="编辑客户资料" style={{ background: 'none', border: 'none', color: '#fff', fontSize: 13 }}>编辑</button>
      </header>
      <div style={{ padding: 12, overflowY: 'auto', height: 'calc(100vh - 110px)' }}>
        <ChannelCard chanCtx={chanCtx} chanKey={chanKey} jsSdkOk={jsSdkOk} customerId={c?.id} />
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap', marginBottom: 10 }}>
          {(detail?.tags || []).map((t, i) => <Tag key={i} theme="primary" variant="light">{t.tag_name}</Tag>)}
          <Tag theme="default" style={{ cursor: 'pointer' }} onClick={onEditTags}>+ 标签</Tag>
        </div>
        {/* E2 修复(2026-09-14)：动作条——后端早就绪但旧工作台只有聊天+标签，跟进/阶段/试驾/接管全靠口头或后台 */}
        <div style={{ display: 'flex', gap: 8, marginBottom: 12, flexWrap: 'wrap' }}>
          <Button size="small" variant="outline" onClick={onNewFollowup}>新建跟进</Button>
          <Button size="small" variant="outline" onClick={onStage}>调整阶段</Button>
          <Button size="small" variant="outline" onClick={onNewTestDrive}>建试驾单</Button>
          {c?.journey_stage && <span style={{ alignSelf: 'center', fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#eef2ff', color: '#4338ca' }}>{STAGE_LABELS[c.journey_stage] || c.journey_stage}</span>}
        </div>
        {/* E2：AI 策略推荐卡片（intent/紧迫度 + 推荐话术一键填入输入框） */}
        <RecommendCard rec={rec} onFill={onFillInput} />
        <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between' }}><span style={{ color: '#a0aec0' }}>手机</span><span>{c?.phone || '-'}{c?.phone && <a href={'tel:' + c.phone} style={{ marginLeft: 8, color: 'var(--pri)' }}>📞</a>}</span></div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>兴趣车型</span><span>{c?.interest_model || '-'}</span></div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>预算</span><span>{c?.budget > 0 ? c.budget + '万' : '-'}</span></div>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 6 }}><span style={{ color: '#a0aec0' }}>备注</span><span style={{ maxWidth: 200, textAlign: 'right' }}>{c?.remark || '-'}</span></div>
        </div>
        <TestDriveCard testDrives={testDrives} onNew={onNewTestDrive} onEdit={onEditTestDrive} onSetStatus={onSetTDStatus} />
        <ChatCard msgs={msgs} chatRef={chatRef} convId={convId} convMode={convMode} aiOn={aiOn}
          onClearDelay={onClearDelay} onTakeover={onTakeover} onTransferBackAI={onTransferBackAI} onToggleAI={onToggleAI} />
        <HistoryTimeline conversations={detail?.conversations || []} tlConv={tlConv} tlMsgs={tlMsgs} onToggle={onToggleTimeline} />
        <RatingCard rate={rate} onRate={onRate} onSubmit={onSubmitRating} />
      </div>
      <div style={{ position: 'fixed', bottom: 0, left: '50%', transform: 'translateX(-50%)', width: '100%', maxWidth: 480, background: '#fff', borderTop: '1px solid #e5e7eb', padding: 10, display: 'flex', gap: 8 }}>
        {/* G-20：顾问消息输入框 aria-label 供屏幕阅读器识别 */}
        <Input value={input} onChange={(v) => onInput(v)} placeholder="输入消息…" aria-label="顾问消息输入框" onEnter={onSend} style={{ flex: 1 }} />
        {/* G-20：发送按钮 aria-label 标注操作意图 */}
        <Button theme="primary" onClick={onSend} aria-label="发送消息">发送</Button>
      </div>
    </div>
  )
}
