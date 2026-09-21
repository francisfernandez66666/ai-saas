// 行业包管理 Tab（§八-6 平台运营 UI 批，2026-09-18）：超管上传 .aipack、上架/下架、跨部门共享开关。
// 依赖接口（仅 super_admin，平台路径）：
//   GET  /api/v1/super/packs?level=industry|enterprise|department（数组）
//   POST /api/v1/super/packs（multipart 字段 file，≤20MB；新入库 status=disabled）
//   PUT  /api/v1/super/packs/:id/status {status:'active'|'disabled'}
//   PUT  /api/v1/super/packs/:id/share {share:0|1}（KB 继承链部门包 opt-out，仅 department 级有意义）
import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Select, Switch, Table, Tag, MessagePlugin } from 'tdesign-react'
import { AUTH, authHeaders } from '../../lib/api'
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
  file_name: string
  file_size: number
  status: string
  created_at: string
}
type PackListResp = { code: number; message?: string; data?: SuperPack[] }

// 层级/状态展示映射
const LEVEL_LABELS: Record<string, string> = { industry: '行业包', enterprise: '企业包', department: '部门包' }
const LEVEL_THEME: Record<string, 'primary' | 'success' | 'default'> = { industry: 'primary', enterprise: 'success', department: 'default' }

/** 包上架/共享管理：level 筛选 + 表格 + 行内上下架/共享 Switch + .aipack 上传区。 */
export function PackTab() {
  // level 筛选（''=全部）
  const [level, setLevel] = useState('')
  const [rows, setRows] = useState<SuperPack[]>([])
  const [loading, setLoading] = useState(false)
  // 待上传文件（选中后点「上传」提交；上传中禁用按钮）
  const [file, setFile] = useState<File | null>(null)
  const [uploading, setUploading] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)

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
    if (!file) { MessagePlugin.warning('请先选择 .aipack 文件'); return }
    if (!file.name.toLowerCase().endsWith('.aipack')) { MessagePlugin.warning('仅接受 .aipack 文件'); return }
    // 前端预校验 20MB（与后端硬上限一致），避免大包白跑一趟
    if (file.size > 20 * 1024 * 1024) { MessagePlugin.warning('包体超过 20MB 上限'); return }
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
        MessagePlugin.success(j.message || '上传成功（默认下架态）')
        setFile(null)
        if (fileRef.current) fileRef.current.value = ''
        load()
      } else {
        MessagePlugin.error((j && j.message) || '上传失败')
      }
    } catch {
      MessagePlugin.error('网络异常，上传失败')
    } finally {
      setUploading(false)
    }
  }

  // 上架/下架（后端仅接受 active/disabled）
  // 契约护栏：路径写单行模板串且 method 就近内联——拆段拼接的尾段字面量会被 PATH_RE 误判成独立路由
  async function setPackStatus(id: number, st: 'active' | 'disabled') {
    const j = await AUTH(`/api/v1/super/packs/${id}/status`, { method: 'PUT', body: { status: st } })
    if (j?.code === 0) { MessagePlugin.success(st === 'active' ? '已上架' : '已下架'); load() }
  }
  // 跨部门共享开关（仅 department 级有意义）；失败也刷新列表把 Switch 复位到库值
  async function setShare(id: number, share: number) {
    const j = await AUTH(`/api/v1/super/packs/${id}/share`, { method: 'PUT', body: { share } })
    if (j?.code === 0) MessagePlugin.success(share === 1 ? '已开启跨部门共享' : '已退出跨部门共享')
    load()
  }

  const cols = [
    { colKey: 'id', title: 'ID', width: 60 },
    { colKey: 'code', title: '标识', width: 130 },
    { colKey: 'name', title: '名称', width: 150, ellipsis: true },
    { colKey: 'version', title: '版本', width: 90, cell: (p: CellProps) => <Tag>{p.row.version}</Tag> },
    { colKey: 'pack_level', title: '层级', width: 90, cell: (p: CellProps) => <Tag theme={LEVEL_THEME[p.row.pack_level] || 'default'}>{LEVEL_LABELS[p.row.pack_level] || p.row.pack_level}</Tag> },
    { colKey: 'industry', title: '行业', width: 100, cell: (p: CellProps) => p.row.industry || '-' },
    { colKey: 'parent_code', title: '上级包', width: 110, cell: (p: CellProps) => p.row.parent_code || '-' },
    { colKey: 'file_name', title: '文件', width: 180, ellipsis: true, cell: (p: CellProps) => `${p.row.file_name || '-'}${p.row.file_size ? ` (${Math.round(Number(p.row.file_size) / 1024)}KB)` : ''}` },
    { colKey: 'status', title: '状态', width: 80, cell: (p: CellProps) => <Tag theme={p.row.status === 'active' ? 'success' : 'default'}>{p.row.status === 'active' ? '上架' : '下架'}</Tag> },
    { colKey: 'share', title: '跨部门共享', width: 110, cell: (p: CellProps) => (
      // 共享开关仅部门包有意义；非部门行禁用置灰（后端不改其行为）
      <Switch
        size="small"
        disabled={p.row.pack_level !== 'department'}
        value={Number(p.row.share_cross_dept) === 1}
        onChange={(checked) => setShare(Number(p.row.id), checked ? 1 : 0)}
      />
    ) },
    { colKey: 'created_at', title: '入库时间', width: 150, cell: (p: CellProps) => (p.row.created_at || '').replace('T', ' ').slice(0, 16) },
    { colKey: 'op', title: '操作', width: 100, cell: (p: CellProps) => (
      <Button size="small" theme={p.row.status === 'active' ? 'danger' : 'success'} variant="outline"
        onClick={() => setPackStatus(Number(p.row.id), p.row.status === 'active' ? 'disabled' : 'active')}>
        {p.row.status === 'active' ? '下架' : '上架'}
      </Button>
    ) },
  ]

  return (
    <div>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 16, gap: 10, flexWrap: 'wrap' }}>
        <h2 style={{ margin: 0 }}>行业包管理</h2>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, flexWrap: 'wrap' }}>
          <Select value={level} onChange={(v) => setLevel(String(v))} options={[
            { label: '全部层级', value: '' }, { label: '行业包', value: 'industry' },
            { label: '企业包', value: 'enterprise' }, { label: '部门包', value: 'department' },
          ]} style={{ width: 130 }} />
          <Button theme="primary" variant="outline" onClick={load} loading={loading}>刷新</Button>
        </div>
      </div>
      {/* 上传区：选 .aipack 文件 → 提交（新入库默认下架，需手动上架） */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 14, flexWrap: 'wrap', background: '#fff', borderRadius: 8, padding: '10px 14px', boxShadow: '0 1px 3px rgba(0,0,0,.08)' }}>
        <input ref={fileRef} type="file" accept=".aipack" onChange={(e) => setFile(e.target.files?.[0] || null)} style={{ fontSize: 13 }} />
        {file && <span style={{ fontSize: 12, color: '#718096' }}>{file.name}（{Math.round(file.size / 1024)}KB）</span>}
        <Button theme="primary" onClick={doUpload} loading={uploading} disabled={!file || uploading}>上传</Button>
        <span style={{ fontSize: 12, color: '#718096' }}>仅 .aipack，≤20MB；上传后默认下架，确认无误再点上架</span>
      </div>
      <Table rowKey="id" data={rows as unknown as TableRowData[]} columns={cols} size="small" loading={loading}
        empty="暂无行业包" pagination={{ defaultPageSize: 20 }} />
      <p style={{ fontSize: 12, color: '#718096', marginTop: 12 }}>三级树形：行业包 → 企业包 → 部门包（parent_code 挂链）。上架前需其上级包已 active；下架不解绑已物化租户，但新绑定与重放包会被拦截。</p>
    </div>
  )
}
