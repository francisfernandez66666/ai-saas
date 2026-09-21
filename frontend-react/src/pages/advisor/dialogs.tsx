// 顾问工作台弹窗集合（C2 拆分）：编辑资料 / 编辑标签 / 产品反馈 / 新建跟进 / 调整阶段 / 建改试驾单
// 从原 Advisor.tsx 原样搬迁，JSX 与交互未改；只把内部 state 提升为 props，由父组件持有
import { Dialog, Input, Textarea } from 'tdesign-react'
import type { Customer } from '../../types'
import { FU_METHODS, STAGE_LABELS, type TestDrive } from './shared'

// FuForm 新建跟进表单（method=phone/wechat/store/email；next_follow_at 留空=仅记录不提醒）
export type FuForm = { method: string; content: string; next_follow_at: string }
// StageForm 调整旅程阶段表单（journey_stage 必填；仅 arrived 时子状态有效）
export type StageForm = { journey_stage: string; journey_sub_stage: string }
// TdForm 试驾单表单（scheduled_at 必填；提交时由父组件转 ISO）
export type TdForm = Record<string, string>

// INPUT_STYLE 弹窗内原生 input/select 的统一样式（原内联重复 5 处）
const INPUT_STYLE = { display: 'block', marginTop: 4, padding: '6px 10px', border: '1px solid #e2e8f0', borderRadius: 6, fontSize: 14, width: '100%' }
// LABEL_STYLE 弹窗内 label 文案样式
const LABEL_STYLE = { fontSize: 13, color: '#475569' }

// EditForm 客户资料编辑表单：通过 DOM id 直接读取输入框值（非受控），提交由父组件 saveEdit 读 DOM 组装
export function EditForm({ cur }: { cur: Customer }) {
  const lab = { display: 'block', fontSize: 13, color: '#475569', margin: '8px 0 4px' }
  const inp = { width: '100%', padding: '8px 12px', border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14 }
  return (<div style={{ display: 'grid', gap: 0 }}>
    <label style={lab}>姓名</label><input id="eName" defaultValue={cur.name || ''} style={inp} />
    <label style={lab}>手机号</label><input id="ePhone" defaultValue={cur.phone || ''} style={inp} />
    <label style={lab}>兴趣车型</label><input id="eModel" defaultValue={cur.interest_model || ''} style={inp} />
    <label style={lab}>预算(万)</label><input id="eBudget" type="number" defaultValue={cur.budget || ''} style={inp} />
    <label style={lab}>备注</label><textarea id="eRemark" defaultValue={cur.remark || ''} style={{ ...inp, minHeight: 60 }} />
  </div>)
}

// EditDialog 编辑客户资料弹窗
export function EditDialog({ visible, customer, onClose, onConfirm }: {
  visible: boolean; customer?: Customer; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header="编辑客户资料" visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="保存">
      {customer && <EditForm cur={customer} />}
    </Dialog>
  )
}

// TagDialog 编辑标签弹窗（全量标签复选；保存语义为「提交列表即最终态」）
export function TagDialog({ visible, allTags, checkedTags, onToggle, onClose, onConfirm }: {
  visible: boolean; allTags: string[]; checkedTags: string[]
  onToggle: (t: string, on: boolean) => void; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header="编辑标签" visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="保存">
      <div style={{ display: 'flex', flexWrap: 'wrap', gap: 8 }}>
        {allTags.map((t) => <label key={t} style={{ fontSize: 13, display: 'flex', alignItems: 'center', gap: 4 }}><input type="checkbox" checked={checkedTags.includes(t)} onChange={(e) => { const el = e.target as HTMLInputElement; onToggle(t, el.checked) }} />{t}</label>)}
      </div>
    </Dialog>
  )
}

// FeedbackDialog 产品反馈弹窗（走 /api/v1/feedback，target_type=feature）
export function FeedbackDialog({ visible, value, onChange, onClose, onConfirm }: {
  visible: boolean; value: string; onChange: (v: string) => void; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header="产品反馈" visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="提交">
      <Textarea value={value} onChange={(v) => onChange(v)} placeholder="说说你的建议…" autosize={{ minRows: 3 }} />
    </Dialog>
  )
}

// FollowupDialog 新建跟进弹窗（E2）
export function FollowupDialog({ visible, form, onForm, onClose, onConfirm }: {
  visible: boolean; form: FuForm; onForm: (f: FuForm) => void; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header="新建跟进" visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="保存">
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {FU_METHODS.map((m) => (
            <button key={m.k} onClick={() => onForm({ ...form, method: m.k })} style={{ padding: '4px 12px', borderRadius: 16, fontSize: 13, border: 'none', background: form.method === m.k ? 'var(--pri)' : '#f1f5f9', color: form.method === m.k ? '#fff' : '#475569', cursor: 'pointer' }}>{m.t}</button>
          ))}
        </div>
        <Textarea value={form.content} onChange={(v) => onForm({ ...form, content: v })} placeholder="跟进内容…" autosize={{ minRows: 2 }} />
        <label style={LABEL_STYLE}>下次跟进时间（可选）<input type="datetime-local" value={form.next_follow_at} onChange={(e) => onForm({ ...form, next_follow_at: e.target.value })} style={INPUT_STYLE} /></label>
      </div>
    </Dialog>
  )
}

// StageDialog 调整客户旅程阶段弹窗（E2）
export function StageDialog({ visible, form, onForm, onClose, onConfirm }: {
  visible: boolean; form: StageForm; onForm: (f: StageForm) => void; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header="调整客户阶段" visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="保存">
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>目标阶段
          <select value={form.journey_stage} onChange={(e) => onForm({ ...form, journey_stage: e.target.value })} style={INPUT_STYLE}>
            <option value="">请选择…</option>
            {Object.entries(STAGE_LABELS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </label>
        {form.journey_stage === 'arrived' && <label style={LABEL_STYLE}>到店子状态
          <select value={form.journey_sub_stage} onChange={(e) => onForm({ ...form, journey_sub_stage: e.target.value })} style={INPUT_STYLE}>
            <option value="">无</option><option value="test_driven">已试驾</option><option value="quoted">已报价</option>
          </select>
        </label>}
      </div>
    </Dialog>
  )
}

// TestDriveDialog 建/改试驾单弹窗（E2；editing 非空=编辑已有单）
export function TestDriveDialog({ visible, editing, form, onForm, onClose, onConfirm }: {
  visible: boolean; editing: TestDrive | null; form: TdForm
  onForm: (f: TdForm) => void; onClose: () => void; onConfirm: () => void
}) {
  return (
    <Dialog header={editing ? '编辑试驾单' : '新建试驾单'} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn="保存">
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>预约时间 *<input type="datetime-local" value={form.scheduled_at} onChange={(e) => onForm({ ...form, scheduled_at: e.target.value })} style={INPUT_STYLE} /></label>
        <Input value={form.model_name} onChange={(v) => onForm({ ...form, model_name: v })} placeholder="试驾车型" />
        <div style={{ display: 'flex', gap: 8 }}>
          <Input value={form.contact_name} onChange={(v) => onForm({ ...form, contact_name: v })} placeholder="联系人" style={{ flex: 1 }} />
          <Input value={form.contact_phone} onChange={(v) => onForm({ ...form, contact_phone: v })} placeholder="联系电话" style={{ flex: 1 }} />
        </div>
        <Input value={form.location} onChange={(v) => onForm({ ...form, location: v })} placeholder="试驾地点" />
        <Textarea value={form.note} onChange={(v) => onForm({ ...form, note: v })} placeholder="备注" autosize={{ minRows: 2 }} />
      </div>
    </Dialog>
  )
}
