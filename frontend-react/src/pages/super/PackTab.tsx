// 行业包管理 Tab（§八-6 平台运营 UI 批，2026-09-18）：超管上传 .aipack、上架/下架、跨部门共享开关。
// 依赖接口（仅 super_admin，平台路径）：
//   GET  /api/v1/super/packs?level=industry|enterprise|department（数组）
//   POST /api/v1/super/packs（multipart 字段 file，≤20MB；新入库 status=disabled）
//   PUT  /api/v1/super/packs/:id/status {status:'active'|'disabled'}
//   PUT  /api/v1/super/packs/:id/share {share:0|1}（KB 继承链部门包 opt-out，仅 department 级有意义）
//   PUT  /api/v1/super/packs/:id/tier  {min_tier:''|personal|enterprise|custom}（G-22c 档位门槛，空串=不设门槛）
import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Select, Switch, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH, authHeaders } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, TableRowData } from '../../types'

// SuperPack 行（对齐 model.IndustryPack json tag；file_path 后端不出网）
type SuperPack = {
  id: number
  code: string
  name: string
  industry: string
  version: string
  pack_level: string
  parent_code: string
  share_cross_dept: number
  /** 最低可绑档位（G-22c）：'' = 不设门槛 */
  min_tier: string
  file_name: string
  file_size: number
  status: string
  created_at: string
}
// PackListResp 行业包列表接口的响应信封：data 为按层级筛选后的包行数组。
type PackListResp = { code: number; message?: string; data?: SuperPack[] }

// 层级/状态展示映射（模块级拿不到 t()，只存「后端值 → I18nKey」，渲染时再 t()）
const LEVEL_KEYS: Record<string, I18nKey> = {
  industry: 'super.pack.levelIndustry',
  enterprise: 'super.pack.levelEnterprise',
  department: 'super.pack.levelDepartment',
}
const LEVEL_THEME: Record<string, 'primary' | 'success' | 'default'> = { industry: 'primary', enterprise: 'success', department: 'default' }

// 档位门槛词表（G-22c）：与 tenants.tier / industry_packs.min_tier 同一套码。
// '' 是"不设门槛"而不是"个人版"——存量包全靠这个空值保持现状零变化。
const TIER_KEYS: Record<string, I18nKey> = {
  '': 'super.pack.tierAll',
  personal: 'super.pack.tierPersonal',
  enterprise: 'super.pack.tierEnterprise',
  custom: 'super.pack.tierCustom',
}

/** 包上架/共享管理：level 筛选 + 表格 + 行内上下架/共享 Switch + .aipack 上传区。 */
export function PackTab() {
  const { t } = useI18n()
  // level 筛选（''=全部）
  const [level, setLevel] = useState('')
  const [rows, setRows] = useState<SuperPack[]>([])
  const [loading, setLoading] = useState(false)
  // 待上传文件（选中后点「上传」提交；上传中禁用按钮）
  const [file, setFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

  // 档位门槛下拉：label 需在渲染期用 t() 解成文案（模块级常量无法调用 t）
  const tierOptions = ['', 'personal', 'enterprise', 'custom'].map((v) => ({ label: t(TIER_KEYS[v]), value: v }))

  // 按层级筛选拉列表
  const load = useCallback(async () => {
    setLoading(true)
    const j = await AUTH<PackListResp>('/api/v1/super/packs' + (level ? '?level=' + level : ''))
    if (j?.code === 0) setRows(j.data || [])
    setLoading(false)
  }, [level])
  useEffect(() => { load() }, [load])

  // multipart 上传必须绕过 AUTH（其固定 JSON 头会顶掉 FormData 的 boundary）：
  // 裸 fetch + authHeaders——authHeaders 不设 Content-Type，由浏览器自动带 boundary
  async function doUpload() {
    if (!file) { MessagePlugin.warning(t('super.pack.plsSelect')); return }
    if (!file.name.toLowerCase().endsWith('.aipack')) { MessagePlugin.warning(t('super.pack.onlyAipack')); return }
    // 前端预校验 20MB（与后端硬上限一致），避免大包白跑一趟
    if (file.size > 20 * 1024 * 1024) { MessagePlugin.warning(t('super.pack.sizeExceed')); return }
    setUploading(true)
    try {
      const fd = new FormData()
      fd.append('file', file)
      // C3 豁免（有意保留裸 fetch）：FormData 上传依赖浏览器自动填充带 boundary 的
      // Content-Type；apiFetch 会默认注入 'application/json' 覆盖掉它，导致服务端
      // 解析不到 multipart。文件上传类请求不收口（已登记在 .bare_fetch_baseline 口径内）。
      const r = await fetch('/api/v1/super/packs', { method: 'POST', headers: authHeaders(), body: fd })
      const j = await r.json().catch(() => null) as { code?: number; message?: string } | null
      if (j && j.code === 0) {
        MessagePlugin.success(j.message || t('super.pack.uploadOk'))
        setFile(null)
        if (fileRef.current) fileRef.current.value = ''
        load()
      } else {
        MessagePlugin.error((j && j.message) || t('super.pack.uploadFail'))
      }
    } catch {
      MessagePlugin.error(t('super.pack.netErr'))
    } finally {
      setUploading(false)
    }
  }

  // 上架/下架（后端仅接受 active/disabled）
  // 契约护栏：路径写单行模板串且 method 就近内联——拆段拼接的尾段字面量会被 PATH_RE 误判成独立路由
  async function setPackStatus(id: number, st: 'active' | 'disabled') {
    const j = await AUTH(`/api/v1/super/packs/${id}/status`, { method: 'PUT', body: { status: st } })
    if (j?.code === 0) { MessagePlugin.success(st === 'active' ? t('super.pack.onShelf') : t('super.pack.offShelf')); load() }
  }
  // 跨部门共享开关（仅 department 级有意义）；失败也刷新列表把 Switch 复位到库值
  async function setShare(id: number, share: number) {
    const j = await AUTH(`/api/v1/super/packs/${id}/share`, { method: 'PUT', body: { share } })
    if (j?.code === 0) MessagePlugin.success(share === 1 ? t('super.pack.shareOn') : t('super.pack.shareOff'))
    load()
  }

  // 档位门槛（G-22c）：哪个包对哪一档客户开放是平台销售口径，不写进包内容——
  // 写进包就要重新打包签名，改一次门槛发一次版。失败同样回读，把下拉复位到库值，
  // 绝不让界面停在一个"看起来改了但没生效"的值上。
  async function setTier(id: number, minTier: string) {
    const j = await AUTH(`/api/v1/super/packs/${id}/tier`, { method: 'PUT', body: { min_tier: minTier } })
    if (j?.code === 0) MessagePlugin.success(minTier ? t('super.pack.tierSet', { tier: t(TIER_KEYS[minTier]) }) : t('super.pack.tierClear'))
    load()
  }

  const cols = [
    { colKey: 'id', title: 'ID', width: 60 },
    { colKey: 'code', title: t('super.pack.colCode'), width: 130 },
    { colKey: 'name', title: t('common.name'), width: 150, ellipsis: true },
    { colKey: 'version', title: t('common.version'), width: 90, cell: (p: CellProps) => <Tag>{p.row.version}</Tag> },
    { colKey: 'pack_level', title: t('super.pack.colLevel'), width: 90, cell: (p: CellProps) => <Tag theme={LEVEL_THEME[p.row.pack_level] || 'default'}>{t(LEVEL_KEYS[p.row.pack_level] || p.row.pack_level)}</Tag> },
    { colKey: 'industry', title: t('super.pack.colIndustry'), width: 100, cell: (p: CellProps) => p.row.industry || '-' },
    { colKey: 'parent_code', title: t('super.pack.colParent'), width: 110, cell: (p: CellProps) => p.row.parent_code || '-' },
    { colKey: 'file_name', title: t('super.pack.colFile'), width: 180, ellipsis: true, cell: (p: CellProps) => `${p.row.file_name || '-'}${p.row.file_size ? ` (${Math.round(Number(p.row.file_size) / 1024)}KB)` : ''}` },
    { colKey: 'status', title: t('common.status'), width: 80, cell: (p: CellProps) => <Tag theme={p.row.status === 'active' ? 'success' : 'default'}>{p.row.status === 'active' ? t('super.pack.onShelf') : t('super.pack.offShelf')}</Tag> },
    { colKey: 'min_tier', title: t('super.pack.colTier'), width: 130, cell: (p: CellProps) => (
      // 档位门槛逐行可改：租户侧列表据此置灰、写侧据此 403，改完立刻在两端生效
      <Select
        size="small"
        value={String(p.row.min_tier ?? '')}
        options={tierOptions}
        onChange={(v) => setTier(Number(p.row.id), String(v ?? ''))}
        style={{ width: 110 }}
      />
    ) },
    { colKey: 'share', title: t('super.pack.colShare'), width: 110, cell: (p: CellProps) => (
      // 共享开关仅部门包有意义；非部门行禁用置灰（后端不改其行为）
      <Switch
        size="small"
        disabled={p.row.pack_level !== 'department'}
        value={Number(p.row.share_cross_dept) === 1}
        onChange={(checked) => setShare(Number(p.row.id), checked ? 1 : 0)}
      />
    ) },
    { colKey: 'created_at', title: t('super.pack.colCreated'), width: 150, cell: (p: CellProps) => (p.row.created_at || '').replace('T', ' ').slice(0, 16) },
    { colKey: 'op', title: t('super.pack.colOp'), width: 100, cell: (p: CellProps) => (
      <Button size="small" theme={p.row.status === 'active' ? 'danger' : 'success'} variant="outline"
        onClick={() => setPackStatus(Number(p.row.id), p.row.status === 'active' ? 'disabled' : 'active')}>
        {p.row.status === 'active' ? t('super.pack.offShelf') : t('super.pack.onShelf')}
      </Button>
    ) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>{t('super.pack.title')}</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <Select value={level} onChange={(v) => setLevel(String(v))} options={[
            { label: t('super.pack.levelAll'), value: '' }, { label: t('super.pack.levelIndustry'), value: 'industry' },
            { label: t('super.pack.levelEnterprise'), value: 'enterprise' }, { label: t('super.pack.levelDepartment'), value: 'department' },
          ]} style={{ width: 130 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>{t('super.pack.refresh')}</Button>
        </div>
      </div>
      {/* 上传区：选 .aipack 文件 → 提交（新入库默认下架，需手动上架） */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14, flexWrap: 'wrap', background: '#fff', borderRadius: 8, padding: '10px 14px', boxShadow: '0 1px 3px rgba(0,0,0,.08)' }}>
        <input ref={fileRef} type="file" accept=".aipack" onChange={(e) => setFile(e.target.files?.[0] || null)} style={{ fontSize: 13 }} />
        {file && <span style={{ fontSize: 12, color: '#718096' }}>{file.name}（{Math.round(file.size / 1024)}KB）</span>}
        <Button theme="primary" onClick={doUpload} loading={uploading} disabled={!file || uploading}>{t('common.upload')}</Button>
        <span style={{ fontSize: 12, color: '#718096' }}>{t('super.pack.uploadHint')}</span>
      </div>
      <Table rowKey="id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty={t('super.pack.empty')} pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>{t('super.pack.treeHint')}</p>
      <p style={{ fontSize: 12, color: '#718096', marginTop: 6 }}>{t('super.pack.tierHint')}</p>
    </div>
  )
}
