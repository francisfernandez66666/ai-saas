// 客户详情·试驾单卡（E2）：列表 + 快捷完成/取消 + 新建/编辑入口（旧版只读，状态全靠后台改）
// 从原 Advisor.tsx 原样搬迁（C2 拆分）；表单填充逻辑留在父组件，本卡只发意图
import { TD_STATUS_LABELS, fmtShort, type TestDrive } from './shared'

export default function TestDriveCard({ testDrives, onNew, onEdit, onSetStatus }: {
  testDrives: TestDrive[]
  onNew: () => void
  onEdit: (td: TestDrive) => void
  onSetStatus: (td: TestDrive, status: string) => void
}) {
  return (
    <div style={{ background: '#fff', borderRadius: 10, padding: 12, marginBottom: 12, fontSize: 13 }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <b style={{ fontSize: 13 }}>试驾单</b>
        <button onClick={onNew} style={{ background: 'none', border: 'none', color: 'var(--pri)', fontSize: 12, cursor: 'pointer' }}>+ 新建</button>
      </div>
      {testDrives.length === 0 && <div style={{ color: '#a0aec0', marginTop: 6, fontSize: 12 }}>暂无试驾单</div>}
      {testDrives.map((td, i) => <div key={i} style={{ marginTop: 6, paddingTop: 6, borderTop: '1px solid #f0f0f0', display: 'flex', gap: 8, alignItems: 'center' }}>
        <span style={{ flex: 1 }}>{td.model_name || '试驾'} <span style={{ color: '#a0aec0' }}>· {TD_STATUS_LABELS[td.status || ''] || '已取消'}</span>{td.scheduled_at ? <span style={{ color: '#a0aec0' }}> · {fmtShort(td.scheduled_at)}</span> : null}</span>
        {td.status === 'pending' && <><button onClick={() => onSetStatus(td, 'completed')} style={{ background: 'none', border: '1px solid #c6f6d5', color: '#276749', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>完成</button>
          <button onClick={() => onSetStatus(td, 'cancelled')} style={{ background: 'none', border: '1px solid #fed7d7', color: '#9b2c2c', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>取消</button></>}
        <button onClick={() => onEdit(td)} style={{ background: 'none', border: '1px solid #e2e8f0', borderRadius: 4, padding: '1px 6px', fontSize: 11, cursor: 'pointer' }}>编辑</button>
      </div>)}
    </div>
  )
}
