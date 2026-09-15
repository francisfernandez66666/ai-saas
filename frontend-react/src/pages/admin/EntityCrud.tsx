// F1/F4 通用后台 CRUD 面板：列表、筛选、弹窗表单、启停/删除，复用于知识库和标签体系。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { Button, Dialog, Input, InputNumber, MessagePlugin, Select, Switch, Table, Tag, Textarea } from 'tdesign-react'
import { useCrud, type CrudRow } from '../../hooks/useCrud'
import type { CellProps, TableRowData } from '../../types'

export type Option = { label: string; value: string | number }

export type FieldSpec = {
  key: string
  label: string
  type?: 'text' | 'textarea' | 'number' | 'bool' | 'select' | 'list' | 'json'
  required?: boolean
  options?: Option[]
  placeholder?: string
  defaultValue?: any
}

export type FilterSpec = {
  key: string
  label: string
  options?: Option[]
}

export type EntityCrudProps = {
  base: string
  title: string
  columns: any[]
  createFields: FieldSpec[]
  editFields?: FieldSpec[]
  filters?: FilterSpec[]
  rowToForm: (row: CrudRow) => Record<string, any>
  emptyForm?: Record<string, any>
  canCreate?: boolean
  canUpdate?: boolean
  canDelete?: boolean
  hasStatusToggle?: boolean
  pageSize?: number
  headerExtra?: React.ReactNode
}

/** 解析 JSON 数组字段，失败时返回兜底列表。 */
export function parseJsonArray(value: unknown, fallback: string[] = []): string[] {
  if (Array.isArray(value)) return value.map((x) => String(x))
  if (typeof value !== 'string' || !value.trim()) return fallback
  try {
    const parsed = JSON.parse(value)
    return Array.isArray(parsed) ? parsed.map((x) => String(x)) : fallback
  } catch {
    return value.split(/[,，]/).map((x) => x.trim()).filter(Boolean)
  }
}

/** 把数组或 JSON 字符串统一转换为字符串数组。 */
export function toArray(value: unknown): string[] {
  if (Array.isArray(value)) return value.map((x) => String(x)).filter(Boolean)
  return String(value || '').split(/[,，]/).map((x) => x.trim()).filter(Boolean)
}

/** 把布尔/数字表单值转换为后端期望的 0/1。 */
export function boolToInt(v: unknown): number {
  if (typeof v === 'boolean') return v ? 1 : 0
  return ['true', '1', 'yes', '是'].includes(String(v).toLowerCase()) ? 1 : 0
}

/** 把启用/停用状态渲染为彩色标签。 */
export function statusTag(row: CrudRow) {
  const active = Number(row.status) === 1 || row.status === true || row.status === 'active'
  return <Tag theme={active ? 'success' : 'default'}>{active ? '启用' : '停用'}</Tag>
}

/** 根据字段定义把表格行转换为表单初始值。 */
export function defaultRowToForm(fields: FieldSpec[], row: CrudRow): Record<string, any> {
  const form: Record<string, any> = {}
  fields.forEach((f) => {
    const raw = row[f.key]
    if (f.type === 'list') form[f.key] = parseJsonArray(raw).join('，')
    else if (f.type === 'bool') form[f.key] = raw === true || raw === 1 || raw === '1' || raw === 'true'
    else if (f.type === 'number') form[f.key] = raw === null || raw === undefined || raw === '' ? 0 : Number(raw)
    else if (f.type === 'json') {
      if (typeof raw === 'string') {
        try { form[f.key] = JSON.stringify(JSON.parse(raw), null, 2) } catch { form[f.key] = raw }
      } else if (raw !== undefined && raw !== null) form[f.key] = JSON.stringify(raw, null, 2)
      else form[f.key] = ''
    } else form[f.key] = raw ?? ''
  })
  return form
}

/** 按字段定义把表单值组装为 API 请求体。 */
export function buildBody(fields: FieldSpec[], form: Record<string, any>) {
  const body: Record<string, any> = {}
  fields.forEach((f) => {
    const v = form[f.key]
    if (f.type === 'list') body[f.key] = toArray(v)
    else if (f.type === 'bool') body[f.key] = v ? 1 : 0
    else if (f.type === 'number') body[f.key] = v === '' || v === undefined || v === null ? 0 : Number(v)
    else if (f.type === 'json') {
      if (typeof v === 'string' && v.trim()) {
        try { body[f.key] = JSON.parse(v) } catch { body[f.key] = v }
      } else body[f.key] = undefined
    } else body[f.key] = typeof v === 'string' ? v.trim() : v ?? ''
  })
  return body
}

/** 校验必填字段，返回第一条可展示错误。 */
export function validateRequired(fields: FieldSpec[], form: Record<string, any>) {
  for (const f of fields) {
    const v = form[f.key]
    if (f.required) {
      if (v === '' || v === undefined || v === null) return f.label
      if (f.type === 'list' && toArray(v).length === 0) return f.label
    }
    if (f.type === 'json' && typeof v === 'string' && v.trim()) {
      try { JSON.parse(v) } catch { return `${f.label} 不是合法 JSON` }
    }
  }
  return ''
}

/** 根据字段类型渲染通用表单控件。 */
function FieldControl({ field, value, onChange }: { field: FieldSpec; value: any; onChange: (v: any) => void }) {
  if (field.type === 'textarea') return <Textarea value={value} onChange={(v) => onChange(v)} placeholder={field.placeholder} />
  if (field.type === 'number') return <InputNumber value={Number(value || 0)} onChange={(v) => onChange(v)} style={{ width: '100%' }} />
  if (field.type === 'bool') return <Switch value={!!value} onChange={(v) => onChange(v)} />
  if (field.type === 'select') return <Select value={value} onChange={(v) => onChange(v)} options={field.options || []} filterable style={{ width: '100%' }} />
  if (field.type === 'json') return <Textarea value={value} onChange={(v) => onChange(v)} autosize={{ minRows: 4, maxRows: 14 }} placeholder={field.placeholder || 'JSON 数组/对象'} />
  return <Input value={value ?? ''} onChange={(v) => onChange(v)} placeholder={field.placeholder} />
}

/** 通用实体 CRUD 页面组件：配置字段后可复用列表、表单和接口调用。 */
export function EntityCrud({
  base,
  title,
  columns,
  createFields,
  editFields,
  filters = [],
  rowToForm,
  emptyForm = {},
  canCreate = true,
  canUpdate = true,
  canDelete = true,
  hasStatusToggle = true,
  pageSize = 20,
  headerExtra,
}: EntityCrudProps) {
  const filterKey = useMemo(() => filters.map((f) => f.key).join('|'), [filters])
  const initialFilters = useMemo(() => Object.fromEntries(filterKey ? filterKey.split('|').map((k) => [k, '']) : []), [filterKey])
  const crud = useCrud(base, { pageSize, initialFilters })
  const [dialogVisible, setDialogVisible] = useState(false)
  const [editing, setEditing] = useState<CrudRow | null>(null)
  const [form, setForm] = useState<Record<string, any>>({})
  const [draftFilters, setDraftFilters] = useState<Record<string, any>>(initialFilters)

  useEffect(() => {
    setDraftFilters((prev) => {
      const next = { ...prev }
      Object.entries(initialFilters).forEach(([k, v]) => { if (next[k] === undefined) next[k] = v })
      return next
    })
  }, [initialFilters])

  // 把草稿筛选条件落到 crud 真实过滤器（值变化才 setFilter，避免无谓重查）
  const applyFilters = useCallback(() => {
    Object.entries(draftFilters).forEach(([k, v]) => { if (crud.filters[k] !== v) crud.setFilter(k, v) })
  }, [crud, draftFilters])

  // 打开新增表单：按字段定义预置默认值（bool→false / number→0 / 其余空）
  const openCreate = () => {
    setEditing(null)
    const f: Record<string, any> = {}
    createFields.forEach((field) => { f[field.key] = emptyForm[field.key] ?? field.defaultValue ?? (field.type === 'bool' ? false : field.type === 'number' ? 0 : '') })
    setForm(f)
    setDialogVisible(true)
  }
  // 打开编辑表单：把行数据回填为表单初值
  const openEdit = (row: CrudRow) => {
    setEditing(row)
    setForm(rowToForm(row))
    setDialogVisible(true)
  }

  // 当前生效的字段集：编辑态用 editFields（未给则回落 createFields），新增态用 createFields
  const activeFields = () => editing ? (editFields || createFields) : createFields
  // 提交新增/编辑：先必填与 JSON 校验，再 create/update，成功关窗
  const submit = async () => {
    const fields = activeFields()
    const missing = validateRequired(fields, form)
    if (missing) {
      MessagePlugin.warning(missing.includes('JSON') ? missing : `请填写${missing}`)
      return
    }
    const body = buildBody(fields, form)
    const j = editing ? await crud.update(editing.id, body) : await crud.create(body)
    if (j?.code === 0) {
      MessagePlugin.success(editing ? '已更新' : '已创建')
      setDialogVisible(false)
      setEditing(null)
    }
  }
  // 删除一行（二次确认，取行名作提示主体）
  const remove = async (row: CrudRow) => {
    if (!(await confirmDialog(`确认删除「${row.name || row.title || row.param_name || row.id}」？`))) return
    const j = await crud.remove(row.id)
    if (j?.code === 0) MessagePlugin.success('已删除')
  }
  // 启用/停用一行（按当前 status 反向调 disable/enable）
  const toggle = async (row: CrudRow) => {
    const active = Number(row.status) === 1
    const j = await crud.action(`${base}/${row.id}/${active ? 'disable' : 'enable'}`)
    if (j?.code === 0) MessagePlugin.success(active ? '已停用' : '已启用')
  }

  const actionCol = {
    colKey: 'actions',
    title: '操作',
    width: canUpdate || canDelete || hasStatusToggle ? 220 : 90,
    fixed: 'right' as const,
    cell: ({ row }: CellProps) => (
      <div className="flex flex-wrap gap-2">
        {hasStatusToggle && <Button size="small" variant="text" theme={Number(row.status) === 1 ? 'warning' : 'success'} onClick={() => toggle(row)}>{Number(row.status) === 1 ? '停用' : '启用'}</Button>}
        {canUpdate && <Button size="small" variant="text" onClick={() => openEdit(row)}>编辑</Button>}
        {canDelete && <Button size="small" variant="text" theme="danger" onClick={() => remove(row)}>删除</Button>}
      </div>
    ),
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-end gap-3">
        <div>
          <div className="text-xs text-gray-500 mb-1">{title}</div>
          {filters.map((f) => (
            f.options ? (
              <Select
                key={f.key}
                value={draftFilters[f.key] ?? ''}
                onChange={(v) => { setDraftFilters((s) => ({ ...s, [f.key]: v })); crud.setFilter(f.key, v) }}
                options={[{ label: '全部', value: '' }, ...(f.options || [])]}
                style={{ width: 180, marginRight: 8 }}
              />
            ) : (
              <Input
                key={f.key}
                value={draftFilters[f.key] ?? ''}
                onChange={(v) => setDraftFilters((s) => ({ ...s, [f.key]: v }))}
                placeholder={f.label}
                style={{ width: 220, marginRight: 8 }}
              />
            )
          ))}
        </div>
        <Button theme="primary" variant="outline" onClick={applyFilters}>查询</Button>
        <Button theme="default" variant="outline" onClick={() => crud.reload()}>刷新</Button>
        {canCreate && <Button theme="primary" onClick={openCreate}>+ 新增</Button>}
        {headerExtra}
        <span className="text-xs text-gray-400 ml-auto">共 {crud.total} 条</span>
      </div>

      <Table
        rowKey="id"
        data={crud.rows as TableRowData[]}
        columns={[...columns, actionCol]}
        loading={crud.loading}
        size="small"
        empty="暂无数据"
        pagination={{
          current: crud.page,
          pageSize: crud.pageSize,
          total: crud.total,
          onChange: ({ current, pageSize: nextSize }: { current: number; pageSize: number }) => crud.changePage(current, nextSize),
        }}
      />

      <Dialog
        header={`${editing ? '编辑' : '新增'}${title}`}
        visible={dialogVisible}
        onClose={() => setDialogVisible(false)}
        onConfirm={submit}
        confirmBtn={crud.saving ? '保存中…' : '保存'}
        width="680px"
      >
        <div className="grid grid-cols-1 md:grid-cols-2 gap-3">
          {activeFields().map((f) => (
            <div key={f.key} className={f.type === 'textarea' || f.type === 'json' ? 'md:col-span-2' : ''}>
              <label className="block text-xs text-gray-500 mb-1">{f.label}{f.required ? ' *' : ''}</label>
              <FieldControl
                field={f}
                value={form[f.key]}
                onChange={(v) => setForm((s) => ({ ...s, [f.key]: v }))}
              />
            </div>
          ))}
        </div>
      </Dialog>
    </div>
  )
}
