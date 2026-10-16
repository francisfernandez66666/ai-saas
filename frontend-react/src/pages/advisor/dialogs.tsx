// 顾问工作台弹窗集合（C2 拆分）：编辑资料 / 编辑标签 / 产品反馈 / 新建跟进 / 调整阶段 / 建改试驾单
// 从原 Advisor.tsx 原样搬迁，JSX 与交互未改；只把内部 state 提升为 props，由父组件持有
import { useI18n } from '../../i18n/useI18n'
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
  const { t } = useI18n()
  const lab = { display: 'block', fontSize: 13, color: '#475569', margin: '8px 0 4px' }
  const inp = { width: '100%', padding: '8px 12px', border: '1px solid #e2e8f0', borderRadius: 8, fontSize: 14 }
  return (<div style={{ display: 'grid', gap: 0 }}>
    <label style={lab}>{t('common.fullName')}</label><input id="eName" defaultValue={cur.name || ''} style={inp} />
    <label style={lab}>{t('common.mobilePhone')}</label><input id="ePhone" defaultValue={cur.phone || ''} style={inp} />
    <label style={lab}>{t('advisor.dlgModel')}</label><input id="eModel" defaultValue={cur.interest_model || ''} style={inp} />
    <label style={lab}>{t('advisor.dlgBudget')}</label><input id="eBudget" type="number" defaultValue={cur.budget || ''} style={inp} />
    <label style={lab}>{t('common.remark')}</label><textarea id="eRemark" defaultValue={cur.remark || ''} style={{ ...inp, minHeight: 60 }} />
  </div>)
}

// EditDialog 编辑客户资料弹窗
export function EditDialog({ visible, customer, onClose, onConfirm }: {
  visible: boolean; customer?: Customer; onClose: () => void; onConfirm: () => void
}) {
  const { t } = useI18n()
  return (
    <Dialog header={t('advisor.dlgEditProfile')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.save')}>
      {customer && <EditForm cur={customer} />}
    </Dialog>
  )
}

// TagDialog 编辑标签弹窗（全量标签复选；保存语义为「提交列表即最终态」）
export function TagDialog({ visible, allTags, checkedTags, onToggle, onClose, onConfirm }: {
  visible: boolean; allTags: string[]; checkedTags: string[]
  onToggle: (t: string, on: boolean) => void; onClose: () => void; onConfirm: () => void
}) {
  const { t } = useI18n()
  return (
    <Dialog header={t('advisor.dlgEditTag')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.save')}>
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
  const { t } = useI18n()
  return (
    <Dialog header={t('advisor.dlgFeedback')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.submit')}>
      <Textarea value={value} onChange={(v) => onChange(v)} placeholder={t('advisor.dlgSuggest')} autosize={{ minRows: 3 }} />
    </Dialog>
  )
}

// FollowupDialog 新建跟进弹窗（E2）
export function FollowupDialog({ visible, form, onForm, onClose, onConfirm }: {
  visible: boolean; form: FuForm; onForm: (f: FuForm) => void; onClose: () => void; onConfirm: () => void
}) {
  const { t } = useI18n()
  return (
    <Dialog header={t('advisor.dlgNewFollow')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.save')}>
      <div style={{ display: 'grid', gap: 10 }}>
        <div style={{ display: 'flex', gap: 8, flexWrap: 'wrap' }}>
          {FU_METHODS.map((m) => (
            <button key={m.k} onClick={() => onForm({ ...form, method: m.k })} style={{ padding: '4px 12px', borderRadius: 16, fontSize: 13, border: 'none', background: form.method === m.k ? 'var(--pri)' : '#f1f5f9', color: form.method === m.k ? '#fff' : '#475569', cursor: 'pointer' }}>{m.t}</button>
          ))}
        </div>
        <Textarea value={form.content} onChange={(v) => onForm({ ...form, content: v })} placeholder={t('advisor.dlgFollowContent')} autosize={{ minRows: 2 }} />
        <label style={LABEL_STYLE}>{t('advisor.dlgNextFollow')}<input type="datetime-local" value={form.next_follow_at} onChange={(e) => onForm({ ...form, next_follow_at: e.target.value })} style={INPUT_STYLE} /></label>
      </div>
    </Dialog>
  )
}

// StageDialog 调整客户旅程阶段弹窗（E2）
export function StageDialog({ visible, form, onForm, onClose, onConfirm }: {
  visible: boolean; form: StageForm; onForm: (f: StageForm) => void; onClose: () => void; onConfirm: () => void
}) {
  const { t } = useI18n()
  return (
    <Dialog header={t('advisor.dlgAdjustStage')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.save')}>
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>{t('advisor.dlgTargetStage')}
          <select value={form.journey_stage} onChange={(e) => onForm({ ...form, journey_stage: e.target.value })} style={INPUT_STYLE}>
            <option value="">{t('advisor.dlgPleaseSelect')}</option>
            {Object.entries(STAGE_LABELS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
        </label>
        {form.journey_stage === 'arrived' && <label style={LABEL_STYLE}>{t('advisor.dlgArrivedStatus')}
          <select value={form.journey_sub_stage} onChange={(e) => onForm({ ...form, journey_sub_stage: e.target.value })} style={INPUT_STYLE}>
            <option value="">{t('common.none')}</option><option value="test_driven">{t('advisor.dlgTestDriven')}</option><option value="quoted">{t('advisor.dlgQuoted')}</option>
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
  const { t } = useI18n()
  return (
    <Dialog header={editing ? t('advisor.dlgEditTestDrive') : t('advisor.dlgNewTestDrive')} visible={visible} onClose={onClose} onConfirm={onConfirm} confirmBtn={t('common.save')}>
      <div style={{ display: 'grid', gap: 10 }}>
        <label style={LABEL_STYLE}>{t('advisor.dlgAppointment')}<input type="datetime-local" value={form.scheduled_at} onChange={(e) => onForm({ ...form, scheduled_at: e.target.value })} style={INPUT_STYLE} /></label>
        <Input value={form.model_name} onChange={(v) => onForm({ ...form, model_name: v })} placeholder={t('advisor.dlgTdModel')} />
        <div style={{ display: 'flex', gap: 8 }}>
          <Input value={form.contact_name} onChange={(v) => onForm({ ...form, contact_name: v })} placeholder={t('advisor.dlgContact')} style={{ flex: 1 }} />
          <Input value={form.contact_phone} onChange={(v) => onForm({ ...form, contact_phone: v })} placeholder={t('advisor.dlgContactPhone')} style={{ flex: 1 }} />
        </div>
        <Input value={form.location} onChange={(v) => onForm({ ...form, location: v })} placeholder={t('advisor.dlgTdLocation')} />
        <Textarea value={form.note} onChange={(v) => onForm({ ...form, note: v })} placeholder={t('common.remark')} autosize={{ minRows: 2 }} />
      </div>
    </Dialog>
  )
}
