// 客户详情·试驾单卡（E2）：列表 + 快捷完成/取消 + 新建/编辑入口（旧版只读，状态全靠后台改）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）；表单填充逻辑留在父组件，本卡只发意图
import { useI18n } from '../../i18n/useI18n'
import { TD_STATUS_LABELS, fmtShort, type TestDrive } from './shared'

// TestDriveCard 顾问工作台「试驾」卡片：试驾列表与新建/编辑/状态流转入口。
export default function TestDriveCard({ testDrives, onNew, onEdit, onSetStatus }: {
  testDrives: TestDrive[]
  onNew: () => void
  onEdit: (td: TestDrive) => void
  onSetStatus: (td: TestDrive, status: string) => void
}) {
  const { t } = useI18n()
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <b style={{ fontSize: 13 }}>{t('advisor.tdTitle')}</b>
        <button onClick={onNew} style={{ background: 'none', border: 'none', color: 'var(--pri)', fontSize: 12, cursor: 'pointer' }}>+ {t('advisor.tdNew')}</button>
      </div>
      {testDrives.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>{t('advisor.tdEmpty')}</div>}
      {testDrives.map((td, i) => <div key={i} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0', display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{ flex: 1 }}>{td.model_name || t('advisor.tdTestDrive')} <span style={{ color: '#a0aec0' }}>· {TD_STATUS_LABELS[td.status || ''] || t('common.cancelled')}</span>{td.scheduled_at ? <span style={{ color: '#a0aec0' }}> · {fmtShort(td.scheduled_at)}</span> : null}</span>
        {td.status === 'pending' && <><button onClick={() => onSetStatus(td, 'completed')} style={{ background: 'none', border: '1px solid #c6f6d5', color: '#276749', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>{t('advisor.tdComplete')}</button>
          <button onClick={() => onSetStatus(td, 'cancelled')} style={{ background: 'none', border: '1px solid #fed7d7', color: '#9b2c2c', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>{t('common.cancel')}</button></>}
        <button onClick={() => onEdit(td)} style={{ background: 'none', border: '1px solid #e2e8f0', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>{t('common.edit')}</button>
      </div>)}
    </div>
  )
}
