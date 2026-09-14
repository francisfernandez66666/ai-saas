// 品牌定制 Tab（F1 从 Admin.tsx 拆出）：租户白标配置。
import { useEffect, useState } from 'react'
import { Button, Input } from 'tdesign-react'
import { getToken } from '../../lib/api'

type BrandForm = {
  custom_domain: string; brand_name: string; brand_link: string; logo_url: string;
  favicon_url: string; primary_color: string; secondary_color: string
}

/** 品牌白标配置 Tab：编辑租户展示名、Logo 和主题色。 */
export function BrandingTab() {
  const [f, setF] = useState<BrandForm>({ custom_domain: '', brand_name: '', brand_link: '', logo_url: '', favicon_url: '', primary_color: '', secondary_color: '' })
  const [msg, setMsg] = useState('')
  useEffect(() => {
    fetch('/api/v1/public/branding').then((r) => r.json()).then((b) => {
      setF({
        custom_domain: b.custom_domain || '',
        brand_name: b.brand_name && b.brand_name !== '跨山 LexCross' ? b.brand_name : '',
        brand_link: b.brand_link || '',
        logo_url: b.logo_url || '',
        favicon_url: b.favicon_url || '',
        primary_color: b.primary_color || '',
        secondary_color: b.secondary_color || '',
      })
    }).catch(() => {})
  }, [])
  async function save() {
    setMsg('保存中...')
    const payload = {
      custom_domain: f.custom_domain.trim() || null,
      brand_name: f.brand_name.trim(),
      brand_link: f.brand_link.trim(),
      logo_url: f.logo_url.trim(),
      favicon_url: f.favicon_url.trim(),
      primary_color: f.primary_color.trim(),
      secondary_color: f.secondary_color.trim(),
    }
    const res = await fetch('/api/v1/admin/tenant/branding', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + getToken() },
      body: JSON.stringify(payload),
    })
    const j = await res.json()
    setMsg(j.code === 0 ? '✅ 已保存，刷新页面即可看到效果' : '❌ ' + (j.message || '保存失败'))
  }
  const field = (k: keyof BrandForm, label: string, ph: string) => (
    <div style={{ marginBottom: 12 }}>
      <label style={{ display: 'block', fontSize: 13, marginBottom: 4, color: '#475569' }}>{label}</label>
      <Input value={f[k]} onChange={(v) => setF({ ...f, [k]: v })} placeholder={ph} style={{ width: '100%' }} />
    </div>
  )
  return (
    <div className="bg-white rounded-lg shadow-sm p-6 max-w-2xl">
      <h3 style={{ fontSize: 16, fontWeight: 600, marginBottom: 4 }}>品牌定制（白标）</h3>
      <p style={{ fontSize: 13, color: '#6b7280', marginBottom: 16 }}>自定义访问域名、显示品牌名、Logo、主题色与外链。</p>
      {field('custom_domain', '自定义访问域名', '如 crm.your-company.com（留空则用平台子域名）')}
      {field('brand_name', '显示品牌名', '如 极石汽车')}
      {field('brand_link', '品牌外链', 'https://www.your-company.com')}
      {field('logo_url', 'Logo 图片地址', 'https://.../logo.png')}
      {field('favicon_url', 'Favicon', 'https://.../favicon.ico')}
      <div style={{ display: 'flex', gap: 12 }}>
        {field('primary_color', '主题主色', '#4f46e5')}
        {field('secondary_color', '主题辅色', '#6366f1')}
      </div>
      <Button theme="primary" onClick={save}>保存品牌配置</Button>
      <span style={{ marginLeft: 12, fontSize: 13, color: '#16a34a' }}>{msg}</span>
    </div>
  )
}
