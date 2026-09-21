// 客户详情·企微侧边栏上下文卡（URL 带 corpid/external_userid 且上下文客户与当前详情一致时展示）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）。§八-6 D 块：JS-SDK 装配结果以一行小字呈现，失败不阻断功能
import { STAGE_LABELS, splitTags, type ChannelContext } from './shared'

// ChannelCard 顾问工作台「渠道接入」卡片：展示渠道上下文与 JS-SDK 就绪状态。
export default function ChannelCard({ chanCtx, chanKey, jsSdkOk, customerId }: {
  chanCtx: ChannelContext | null
  chanKey: { corpid: string; external_userid: string } | null
  jsSdkOk: boolean | null
  customerId?: number
}) {
  if (!chanCtx || Number(chanCtx.customer_id) !== Number(customerId)) return null
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13, border: '1px solid #eef2ff' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <b>企业微信客户</b>
        <span style={{ fontSize: 10, padding: '1px 6px', borderRadius: 10, background: '#eef2ff', color: '#4338ca' }}>侧边栏</span>
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(2,1fr)', gap: '6px 12px', marginTop: 8 }}>
        <div><span style={{ color: '#a0aec0' }}>阶段</span><div>{STAGE_LABELS[chanCtx.journey_stage || ''] || chanCtx.journey_stage || '-'}</div></div>
        <div><span style={{ color: '#a0aec0' }}>关注产品</span><div>{chanCtx.interest_model || '-'}</div></div>
        <div><span style={{ color: '#a0aec0' }}>企业微信ID</span><div style={{ wordBreak: 'break-all' }}>{chanCtx.external_userid || chanKey?.external_userid || '-'}</div></div>
        <div><span style={{ color: '#a0aec0' }}>接待成员</span><div>{chanCtx.staff_id || '-'}</div></div>
      </div>
      {/* §八-6 D 块：JS-SDK 装配状态一行小字（null=未装配完成不显示；失败不阻断侧边栏功能） */}
      {jsSdkOk !== null && (
        <div style={{ fontSize: 11, color: '#a0aec0', marginTop: 8 }}>企微上下文：{jsSdkOk ? '已就绪' : '不可用（非企微环境可忽略）'}</div>
      )}
      {splitTags(chanCtx.tags).length > 0 && (
        <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', marginTop: 8 }}>
          {splitTags(chanCtx.tags).map((x: string) => <span key={x} style={{ fontSize: 11, padding: '1px 7px', borderRadius: 10, background: '#f1f5f9', color: '#475569' }}>{x}</span>)}
        </div>
      )}
    </div>
  )
}
