// 超管·AI 商业包管理视图：新增表单（非受控 DOM 读取）+ 包列表上下架
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Button, Table, Tag } from 'tdesign-react'
import type { CellProps } from '../../types'
import { useI18n } from '../../i18n/useI18n'
import { Section, TYPE_NAMES, type Pkg } from './shared'
// FIX-8(2026-09-27)：售价展示走单点（整元省掉小数，有分才带两位）。
import { fenToYuanCompact } from '../../lib/money'

// PackagesTab 平台后台「套餐管理」页：套餐列表、上下架与新建入口。
export default function PackagesTab({ pkgs, onToggle, onCreate }: {
  pkgs: Pkg[]
  onToggle: (id: number, enabled: boolean) => void
  onCreate: () => void
}) {
  const { t } = useI18n()
  // AI 商业包列定义（类型/售价/有效期/上下架切换）
  const pkgCols = [
    { colKey: 'id', title: 'ID', width: 50 }, { colKey: 'code', title: t('super.pkg.colCode'), width: 120 }, { colKey: 'name', title: t('common.name'), width: 120 },
    { colKey: 'p_type', title: t('common.type'), width: 90, cell: (p: CellProps) => { const k = TYPE_NAMES[p.row.p_type]; return k ? t(k) : p.row.p_type } },
    { colKey: 'ai_calls', title: t('super.pkg.colAiCalls'), width: 80 },
    { colKey: 'price_cents', title: t('super.pkg.colPrice'), width: 90, cell: (p: CellProps) => '¥' + fenToYuanCompact(p.row.price_cents) },
    { colKey: 'duration_days', title: t('super.pkg.colDuration'), width: 90, cell: (p: CellProps) => p.row.duration_days ? p.row.duration_days + t('common.dayUnit') : '—' },
    { colKey: 'enabled', title: t('common.status'), width: 80, cell: (p: CellProps) => <Tag theme={p.row.enabled ? 'success' : 'default'}>{p.row.enabled ? t('super.pkg.onShelf') : t('super.pkg.offShelf')}</Tag> },
    { colKey: 'op', title: t('common.op'), width: 90, cell: (p: CellProps) => <Button size="small" theme={p.row.enabled ? 'danger' : 'success'} variant="outline" onClick={() => onToggle(p.row.id, !p.row.enabled)}>{p.row.enabled ? t('super.pkg.offShelf') : t('super.pkg.onShelf')}</Button> },
  ]

  return (
    <Section title={t('super.pkg.title')} desc={t('super.pkg.desc')}>
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, flexWrap: 'wrap', alignItems: 'center' }}>
        <input id="pCode" placeholder={t('super.pkg.phCode')} style={{ width: 130, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <input id="pName" placeholder={t('super.pkg.phName')} style={{ width: 110, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <select id="pType" defaultValue="paid" style={{ width: 120, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }}>
          <option value="paid">{t('super.shared.typePaid')}</option><option value="increment">{t('super.shared.typeIncrement')}</option><option value="free">{t('super.shared.typeFree')}</option>
        </select>
        <input id="pCalls" placeholder={t('super.pkg.phCalls')} style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        {/* FIX-11(2026-09-29)：这里从前收"分"，绕开了 lib/money.ts「界面是元、提交体是分」
            的单点——列表展示那半边(:20)早走 fenToYuanCompact，输入这半边却让人肉算分，
            同一个页面两个单位口径。改元后提交处经 yuanToFen 换算并拦三位小数。 */}
        <input id="pPrice" placeholder={t('super.pkg.phPrice')} style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <input id="pDays" placeholder={t('super.pkg.phDays')} style={{ width: 90, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6 }} />
        <Button theme="primary" onClick={onCreate}>{t('super.pkg.create')}</Button>
      </div>
      <Table rowKey="id" data={pkgs} columns={pkgCols} size="small" />
    </Section>
  )
}
