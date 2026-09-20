// 品牌定制 Tab（F1 从 Admin.tsx 拆出）：租户白标配置。
// P1-7 迁移(2026-09-20 审计批)：裸 fetch → lib/api AUTH（超时/401 登出/断网兜底统一）。
import { useEffect, useState } from 'react'
import { Button, Input } from 'tdesign-react'
import { AUTH } from '../../lib/api'

type BrandForm = {
  custom_domain: string; brand_name: string; brand_link: string; logo_url: string;
  favicon_url: string; primary_color: string; secondary_color: string
}

/** 品牌白标配置 Tab：编辑租户展示名、Logo 和主题色。 */
export function BrandingTab() {
  const [f, setF] = useState<BrandForm>({ custom_domain: '', brand_name: '', brand_link: '', logo_url: '', favicon_url: '', primary_color: '', secondary_color: '' })
  const [loaded, setLoaded] = useState<BrandForm | null>(null) // A2：记录初始值，保存只提交改动字段（PATCH 语义，防缺字段误清）
  const [msg, setMsg] = useState('')
  useEffect(() => {
    // A2 修复(2026-09-14)：旧实现读 public 接口——按 Host 解析租户，管理员在平台域操作时
    // 拿到平台默认（恒空表单），保存把空串写回 → 白标配置被清。改走登录态本租户读取端点。
    AUTH('/api/v1/admin/tenant/branding')
      .then((j) => {
        const b: Record<string, unknown> = (j && j.code === 0 && j.data) ? j.data : {}
        const v: BrandForm = {
          custom_domain: (b.custom_domain as string) || '',
          brand_name: b.brand_name && b.brand_name !== '跨山 LexCross' ? String(b.brand_name) : '',
          brand_link: (b.brand_link as string) || '',
          logo_url: (b.logo_url as string) || '',
          favicon_url: (b.favicon_url as string) || '',
          primary_color: (b.primary_color as string) || '',
          secondary_color: (b.secondary_color as string) || '',
        }
        setF(v); setLoaded(v)
      })
  }, [])
  async function save() {
    setMsg('保存中...')
    // 仅提交相对初始值有变化的字段（后端 A2 起为指针语义：缺省=不改；custom_domain 空串=清空）
    const payload: Record<string, string | null> = {}
    const keys: (keyof BrandForm)[] = ['brand_name', 'brand_link', 'logo_url', 'favicon_url', 'primary_color', 'secondary_color', 'custom_domain']
    for (const k of keys) {
      const cur = (f[k] || '').trim()
      const old = loaded ? (loaded[k] || '').trim() : ''
      if (k === 'custom_domain') {
        if (cur !== old) payload[k] = cur || null
        if (cur === '' && old !== '') payload[k] = '' // 显式清空域名
        continue
      }
      if (cur !== old) payload[k] = cur
    }
    if (Object.keys(payload).length === 0) { setMsg('无改动'); return }
    // AUTH 失败已 toastError；保存结果仍写回页内 msg 横幅（成功/失败都可见）
    const j = await AUTH('/api/v1/admin/tenant/branding', { method: 'PUT', body: payload })
    setMsg(j?.code === 0 ? '✅ 已保存，刷新页面即可看到效果' : '❌ ' + (j?.message || '保存失败'))
  }
  // 渲染一个白标配置文本输入项（label + 受控 Input，绑定到 form 的某字段）
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
