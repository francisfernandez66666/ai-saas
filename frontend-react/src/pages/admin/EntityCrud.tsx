// F1/F4 通用后台 CRUD 面板：列表、筛选、弹窗表单、启停/删除，复用于知识库和标签体系。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { confirmDialog } from '../../lib/confirm'
import { BOOL_TRUTHY_TOKENS } from '../../lib/parse'
import { Button, Dialog, Input, InputNumber, MessagePlugin, Select, Switch, Table, Tag, Textarea } from 'tdesign-react'
import { useCrud, type CrudRow } from '../../hooks/useCrud'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps, TableRowData } from '../../types'

/** 下拉选项（Select/筛选器共用形态） */
export type Option = { label: string; value: string | number }

/** 弹窗表单字段 spec：type 决定控件形态，list/json 为多行文本录入的序列化类型 */
export type FieldSpec = {
  key: string
  // label 可选：*_ALL_FIELDS 这类只服务 defaultRowToForm（只读 key/type）的内部数组
  // 不必携带可渲染文案；真正渲染表单的 createFields/editFields 必填 label。
  label?: string
  type?: 'text' | 'textarea' | 'number' | 'bool' | 'select' | 'list' | 'json'
  required?: boolean
  options?: Option[]
  placeholder?: string
  // P2-10：默认值按字段类型各异（string/number/boolean）， unknown 足够表达
  defaultValue?: unknown
}

/** 列表筛选器 spec：key 对应查询参数名，options 缺省时由调用方注入枚举 */
export type FilterSpec = {
  key: string
  label: string
  options?: Option[]
}

/** EntityCrud 面板入参：base 为 REST 前缀（列表/详情/增删改共用），createFields/editFields 决定弹窗表单 */
export type EntityCrudProps = {
  base: string
  title: string
  // P2-10：有意保留 any[]——列 spec 的 cell 回调需与 TDesign 各表格的行泛型双向匹配
  // （同 types.ts CellProps=PrimaryTableCellParams<any> 的 P2-8 判据：any 行泛型
  // 才能让同一份列定义同时适配 TableRowData 与各页自定义行类型）。列定义由各页
  // 字面量给出、本组件只透传给 Table，收窄类型无编译期收益。
  columns: any[]
  createFields: FieldSpec[]
  editFields?: FieldSpec[]
  filters?: FilterSpec[]
  rowToForm: (row: CrudRow) => Record<string, unknown>
  emptyForm?: Record<string, unknown>
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
  return BOOL_TRUTHY_TOKENS.includes(String(v).toLowerCase()) ? 1 : 0
}

/** 把启用/停用状态渲染为彩色标签。
 *  P2-14：从纯函数改为组件——标签文案（草稿/启用/停用）经 t() 下发；
 *  调用方从 `cell: (p) => statusTag(p.row)` 改为 `cell: (p) => <StatusTag row={p.row} />`。
 *  同时消掉一个 react-refresh 混出导出警告（组件导出不计）。 */
export function StatusTag({ row }: { row: CrudRow }) {
  const { t } = useI18n()
  // E4（2026-09-19）：话术模板 status=2 为草稿（不进召回池），其余实体数值 2 无既有语义
  if (Number(row.status) === 2 && typeof row.status === 'number') return <Tag theme="warning">{t('crud.draft')}</Tag>
  const active = Number(row.status) === 1 || row.status === true || row.status === 'active'
  return <Tag theme={active ? 'success' : 'default'}>{active ? t('common.enable') : t('common.disable')}</Tag>
}

/** 根据字段定义把表格行转换为表单初始值。 */
export function defaultRowToForm(fields: FieldSpec[], row: CrudRow): Record<string, unknown> {
  const form: Record<string, unknown> = {}
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
export function buildBody(fields: FieldSpec[], form: Record<string, unknown>) {
  const body: Record<string, unknown> = {}
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

/** 校验必填字段，返回第一条可展示错误。
 *  P2-14：改返回结构化结果（kind+label）而非拼好的中文句子——多语言文案由持有 t()
 *  的调用方组织，纯函数 helper 不背语言。仅 EntityCrud 自身消费（无外部调用方）。 */
export function validateRequired(
  fields: FieldSpec[],
  form: Record<string, unknown>,
): { kind: 'required' | 'json'; label: string } | null {
  for (const f of fields) {
    const v = form[f.key]
    if (f.required) {
      if (v === '' || v === undefined || v === null) return { kind: 'required', label: f.label ?? f.key }
      if (f.type === 'list' && toArray(v).length === 0) return { kind: 'required', label: f.label ?? f.key }
    }
    if (f.type === 'json' && typeof v === 'string' && v.trim()) {
      try { JSON.parse(v) } catch { return { kind: 'json', label: f.label ?? f.key } }
    }
  }
  return null
}

/** 根据字段类型渲染通用表单控件。
 *  P2-10：value/onChange 从 any 收敛为 unknown——表单值本就按字段类型动态，
 *  交给具体控件前按需断言（值来源是 defaultRowToForm/buildBody 的未知形态）。 */
function FieldControl({ field, value, onChange }: { field: FieldSpec; value: unknown; onChange: (v: unknown) => void }) {
  const { t } = useI18n()
  if (field.type === 'textarea') return <Textarea value={value as string} onChange={(v) => onChange(v)} placeholder={field.placeholder} />
  if (field.type === 'number') return <InputNumber value={Number(value || 0)} onChange={(v) => onChange(v)} style={{ width: '100%' }} />
  if (field.type === 'bool') return <Switch value={!!value} onChange={(v) => onChange(v)} />
  if (field.type === 'select') return <Select value={value as string} onChange={(v) => onChange(v)} options={field.options || []} filterable style={{ width: '100%' }} />
  if (field.type === 'json') return <Textarea value={value as string} onChange={(v) => onChange(v)} autosize={{ minRows: 4, maxRows: 14 }} placeholder={field.placeholder || t('crud.jsonPh')} />
  return <Input value={value as string} onChange={(v) => onChange(v)} placeholder={field.placeholder} />
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
  // P2-14：面板自身文案（按钮/确认/提示/空态/计数）走 t()；字段标签由各 Tab 传入（已翻译）
  const { t } = useI18n()
  const filterKey = useMemo(() => filters.map((f) => f.key).join('|'), [filters])
  const initialFilters = useMemo(() => Object.fromEntries(filterKey ? filterKey.split('|').map((k) => [k, '']) : []), [filterKey])
  const crud = useCrud(base, { pageSize, initialFilters })
  const [dialogVisible, setDialogVisible] = useState(false)
  const [editing, setEditing] = useState<CrudRow | null>(null)
  // P2-10：表单/筛选草稿值按字段动态（string/number/boolean 混排），收敛为 unknown
  const [form, setForm] = useState<Record<string, unknown>>({})
  const [draftFilters, setDraftFilters] = useState<Record<string, unknown>>(initialFilters)

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
    const f: Record<string, unknown> = {}
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
    const err = validateRequired(fields, form)
    if (err) {
      MessagePlugin.warning(err.kind === 'json' ? t('crud.invalidJson', { label: err.label }) : t('crud.fillRequired', { field: err.label }))
      return
    }
    const body = buildBody(fields, form)
    const j = editing ? await crud.update(editing.id, body) : await crud.create(body)
    if (j?.code === 0) {
      MessagePlugin.success(editing ? t('crud.updated') : t('crud.created'))
      setDialogVisible(false)
      setEditing(null)
    }
  }
  // 删除一行（二次确认，取行名作提示主体）
  const remove = async (row: CrudRow) => {
    if (!(await confirmDialog(t('crud.confirmDelete', { name: row.name || row.title || row.param_name || row.id })))) return
    const j = await crud.remove(row.id)
    if (j?.code === 0) MessagePlugin.success(t('crud.deleted'))
  }
  // 启用/停用一行（按当前 status 反向调 disable/enable）
  const toggle = async (row: CrudRow) => {
    const active = Number(row.status) === 1
    const j = await crud.action(`${base}/${row.id}/${active ? 'disable' : 'enable'}`)
    if (j?.code === 0) MessagePlugin.success(active ? t('crud.disabled') : t('crud.enabled'))
  }

  const actionCol = {
    colKey: 'actions',
    title: t('crud.actions'),
    width: canUpdate || canDelete || hasStatusToggle ? 220 : 90,
    fixed: 'right' as const,
    cell: ({ row }: CellProps) => (
      <div className="flex flex-wrap gap-2">
        {hasStatusToggle && <Button size="small" variant="text" theme={Number(row.status) === 1 ? 'warning' : 'success'} onClick={() => toggle(row)}>{Number(row.status) === 1 ? t('common.disable') : t('common.enable')}</Button>}
        {canUpdate && <Button size="small" variant="text" onClick={() => openEdit(row)}>{t('crud.edit')}</Button>}
        {canDelete && <Button size="small" variant="text" theme="danger" onClick={() => remove(row)}>{t('crud.delete')}</Button>}
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
                value={String(draftFilters[f.key] ?? '')}
                onChange={(v) => { setDraftFilters((s) => ({ ...s, [f.key]: v })); crud.setFilter(f.key, v) }}
                options={[{ label: t('common.all'), value: '' }, ...(f.options || [])]}
                style={{ width: 180, marginRight: 8 }}
              />
            ) : (
              <Input
                key={f.key}
                value={String(draftFilters[f.key] ?? '')}
                onChange={(v) => setDraftFilters((s) => ({ ...s, [f.key]: v }))}
                placeholder={f.label}
                style={{ width: 220, marginRight: 8 }}
              />
            )
          ))}
        </div>
        <Button theme="primary" variant="outline" onClick={applyFilters}>{t('crud.query')}</Button>
        <Button theme="default" variant="outline" onClick={() => crud.reload()}>{t('crud.refresh')}</Button>
        {canCreate && <Button theme="primary" onClick={openCreate}>{t('crud.create')}</Button>}
        {headerExtra}
        <span className="text-xs text-gray-400 ml-auto">{t('crud.total', { count: crud.total })}</span>
      </div>

      <Table
        rowKey="id"
        data={crud.rows as TableRowData[]}
        columns={[...columns, actionCol]}
        loading={crud.loading}
        size="small"
        empty={t('crud.empty')}
        pagination={{
          current: crud.page,
          pageSize: crud.pageSize,
          total: crud.total,
          onChange: ({ current, pageSize: nextSize }: { current: number; pageSize: number }) => crud.changePage(current, nextSize),
        }}
      />

      <Dialog
        header={`${editing ? t('crud.editTitle') : t('crud.createTitle')}${title}`}
        visible={dialogVisible}
        onClose={() => setDialogVisible(false)}
        onConfirm={submit}
        confirmBtn={crud.saving ? t('crud.saving') : t('crud.save')}
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
