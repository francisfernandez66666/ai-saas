// 超管·品牌定制（白标）视图：按租户设置域名/品牌名/Logo/主题色/外链，A5 补 custom_css·custom_js 编辑位
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）；P2-14 文案迁移至 i18n
import { Button, Select } from 'tdesign-react'
import { useI18n } from '../../i18n/useI18n'
import { Field, Section, type BdForm, type Tenant } from './shared'

// BrandingTab 平台后台「品牌设置」页：按租户加载与保存品牌、落地页配置。
export default function BrandingTab({ tenants, tenant, onTenant, bd, onBd, msg, onLoad, onSave }: {
  tenants: Tenant[]
  tenant: number | ''
  onTenant: (v: number) => void
  bd: BdForm
  onBd: (f: BdForm) => void
  msg: string
  onLoad: () => void
  onSave: () => void
}) {
  const { t } = useI18n()
  return (
    <Section title={t('super.brand.title')} desc={t('super.brand.desc')}>
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, alignItems: 'center' }}>
        <Select value={tenant} onChange={(v) => onTenant(v as number)} options={tenants.map((x) => ({ label: `#${x.id} ${x.name}（${x.code}）`, value: x.id }))} placeholder={t('super.brand.phTenant')} style={{ width: 280 }} />
        <Button theme="primary" onClick={onLoad}>{t('super.brand.load')}</Button>
      </div>
      <div style={{ maxWidth: 640 }} className="grid grid-cols-1 gap-3">
        <Field label={t('super.brand.fDomain')} v={bd.custom_domain} set={(x) => onBd({ ...bd, custom_domain: x })} ph={t('super.brand.phDomain')} />
        <Field label={t('super.brand.fBrandName')} v={bd.brand_name} set={(x) => onBd({ ...bd, brand_name: x })} ph={t('super.brand.phBrandName')} />
        <Field label={t('super.brand.fLink')} v={bd.brand_link} set={(x) => onBd({ ...bd, brand_link: x })} ph="https://www.your-company.com" />
        <Field label={t('super.brand.fLogo')} v={bd.logo_url} set={(x) => onBd({ ...bd, logo_url: x })} ph="https://.../logo.png" />
        <Field label="Favicon" v={bd.favicon_url} set={(x) => onBd({ ...bd, favicon_url: x })} ph="https://.../favicon.ico" />
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field label={t('super.brand.fPrimary')} v={bd.primary_color} set={(x) => onBd({ ...bd, primary_color: x })} ph="#4f46e5" />
          <Field label={t('super.brand.fSecondary')} v={bd.secondary_color} set={(x) => onBd({ ...bd, secondary_color: x })} ph="#6366f1" />
        </div>
        {/* A5 修复(2026-09-14)：custom_css/custom_js 有库列有注入执行链但全站无编辑入口；
            A3：旧表单缺这两字段时后端按零值覆盖，超管每次保存即抹掉既有 CSS/JS——后端已指针化，此处补编辑位 */}
        <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>{t('super.brand.cssLabel')}</label>
          <textarea value={bd.custom_css} onChange={(e) => onBd({ ...bd, custom_css: e.target.value })} placeholder={t('super.brand.cssExample')} style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
        <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>{t('super.brand.jsLabel')}</label>
          <textarea value={bd.custom_js} onChange={(e) => onBd({ ...bd, custom_js: e.target.value })} placeholder={t('super.brand.jsPh')} style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Button theme="primary" onClick={onSave}>{t('super.brand.save')}</Button>
          <span style={{ fontSize: 13, color: msg.includes('✅') ? '#16a34a' : '#dc2626' }}>{msg}</span>
        </div>
      </div>
    </Section>
  )
}
