// 超管·品牌定制（白标）视图：按租户设置域名/品牌名/Logo/主题色/外链，A5 补 custom_css·custom_js 编辑位
// 从原 SuperAdmin.tsx 原样搬迁（C2 拆分）
import { Button, Select } from 'tdesign-react'
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
  return (
    <Section title="品牌定制（白标）" desc="为任意租户设置自定义访问域名、显示品牌名、Logo、主题色与外链。保存后按自定义域名访问即生效。">
      <div style={{ display: 'flex', gap: 10, marginBottom: 12, alignItems: 'center' }}>
        <Select value={tenant} onChange={(v) => onTenant(v as number)} options={tenants.map((t) => ({ label: `#${t.id} ${t.name}（${t.code}）`, value: t.id }))} placeholder="选择租户" style={{ width: 280 }} />
        <Button theme="primary" onClick={onLoad}>读取</Button>
      </div>
      <div style={{ maxWidth: 640 }} className="grid grid-cols-1 gap-3">
        <Field label="自定义访问域名" v={bd.custom_domain} set={(x) => onBd({ ...bd, custom_domain: x })} ph="如 crm.your-company.com（留空用平台子域名）" />
        <Field label="显示品牌名" v={bd.brand_name} set={(x) => onBd({ ...bd, brand_name: x })} ph="如 极石汽车" />
        <Field label="品牌外链" v={bd.brand_link} set={(x) => onBd({ ...bd, brand_link: x })} ph="https://www.your-company.com" />
        <Field label="Logo 图片地址" v={bd.logo_url} set={(x) => onBd({ ...bd, logo_url: x })} ph="https://.../logo.png" />
        <Field label="Favicon" v={bd.favicon_url} set={(x) => onBd({ ...bd, favicon_url: x })} ph="https://.../favicon.ico" />
        <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
          <Field label="主题主色" v={bd.primary_color} set={(x) => onBd({ ...bd, primary_color: x })} ph="#4f46e5" />
          <Field label="主题辅色" v={bd.secondary_color} set={(x) => onBd({ ...bd, secondary_color: x })} ph="#6366f1" />
        </div>
        {/* A5 修复(2026-09-14)：custom_css/custom_js 有库列有注入执行链但全站无编辑入口；
            A3：旧表单缺这两字段时后端按零值覆盖，超管每次保存即抹掉既有 CSS/JS——后端已指针化，此处补编辑位 */}
        <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>自定义 CSS（注入该租户全部页面）</label>
          <textarea value={bd.custom_css} onChange={(e) => onBd({ ...bd, custom_css: e.target.value })} placeholder="如 .tdesign-header { display:none }" style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
        <div><label style={{ display: 'block', fontSize: 13, color: '#475569', marginBottom: 4 }}>自定义 JS（仅超管可改；空=不改）</label>
          <textarea value={bd.custom_js} onChange={(e) => onBd({ ...bd, custom_js: e.target.value })} placeholder="平台侧注入脚本（信任面：超管专属）" style={{ width: '100%', minHeight: 60, padding: 8, border: '1px solid #e2e8f0', borderRadius: 6, fontFamily: 'monospace', fontSize: 12 }} /></div>
        <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
          <Button theme="primary" onClick={onSave}>保存品牌配置</Button>
          <span style={{ fontSize: 13, color: msg.includes('✅') ? '#16a34a' : '#dc2626' }}>{msg}</span>
        </div>
      </div>
    </Section>
  )
}
