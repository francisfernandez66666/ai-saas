// F2 知识库管理页：品牌/车型/参数/竞品/片段五类后台 CRUD，并展示向量状态与缓存重载。
// P2-14：全量文案走 t()（键见 i18n/dictionaries）。*_ALL_FIELDS 只保留 key+type——
// defaultRowToForm 仅消费这两个字段（label/options/placeholder 在其中从不被读），
// 详见 EntityCrud.defaultRowToForm 实现。
import { useCallback, useEffect, useState } from 'react'
import { Button, MessagePlugin, Tabs, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CrudRow } from '../../hooks/useCrud'
import { useI18n } from '../../i18n/useI18n'
import type { CellProps } from '../../types'
import {
  EntityCrud,
  boolToInt,
  defaultRowToForm,
  parseJsonArray,
  StatusTag,
  type FieldSpec,
  type Option,
} from './EntityCrud'

/** 公开可见性选项（品牌/车型/片段共用）。 */
function useVisibilityOptions(): Option[] {
  const { t } = useI18n()
  return [
    { label: t('kb.visPublic'), value: 'public' },
    { label: t('kb.visPrivate'), value: 'private' },
  ]
}

/** 知识库品牌维护 Tab。 */
function BrandTab() {
  const { t } = useI18n()
  const visibilityOptions = useVisibilityOptions()
  return <EntityCrud
    base="/api/v1/admin/knowledge/brands"
    title={t('kb.brand')}
    filters={[{ key: 'keyword', label: t('kb.filterKeyword') }, { key: 'status', label: t('common.status'), options: [{ label: t('common.enable'), value: 1 }, { label: t('common.disable'), value: 0 }] }]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'name', title: t('common.name'), width: 140 },
      { colKey: 'code', title: t('common.code'), width: 120 },
      { colKey: 'country', title: t('common.country'), width: 100 },
      { colKey: 'founded_year', title: t('kb.foundedYear'), width: 90 },
      { colKey: 'description', title: t('common.description'), ellipsis: true },
      { colKey: 'status', title: t('common.status'), width: 90, cell: ({ row }: CellProps) => <StatusTag row={row} /> },
      { colKey: 'visibility', title: t('common.visibility'), width: 110, cell: ({ row }: CellProps) => (row.visibility === 'public' ? <Tag theme="success">{t('common.public')}</Tag> : <Tag theme="default">{t('common.private')}</Tag>) },
    ]}
    createFields={[
      { key: 'name', label: t('kb.brandName'), required: true },
      { key: 'code', label: t('kb.brandCode'), required: true, placeholder: t('kb.brandCodePh') },
      { key: 'country', label: t('common.country') },
      { key: 'founded_year', label: t('kb.foundedYearFull'), type: 'number' },
      { key: 'logo', label: t('kb.logoUrl') },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'description', label: t('common.description'), type: 'textarea' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    editFields={[
      { key: 'name', label: t('kb.brandName'), required: true },
      { key: 'country', label: t('common.country') },
      { key: 'founded_year', label: t('kb.foundedYearFull'), type: 'number' },
      { key: 'logo', label: t('kb.logoUrl') },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'description', label: t('common.description'), type: 'textarea' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    rowToForm={(row) => defaultRowToForm(BRAND_ALL_FIELDS, row)}
  />
}

/** 知识库车型维护 Tab。 */
function ModelTab({ brandOptions }: { brandOptions: Option[] }) {
  const { t } = useI18n()
  const visibilityOptions = useVisibilityOptions()
  return <EntityCrud
    base="/api/v1/admin/knowledge/models"
    title={t('kb.model')}
    filters={[{ key: 'keyword', label: t('kb.filterKeyword') }, { key: 'brand_id', label: t('common.brand'), options: brandOptions }]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'name', title: t('kb.model'), width: 160 },
      { colKey: 'brand_name', title: t('common.brand'), width: 120 },
      { colKey: 'code', title: t('common.code'), width: 120 },
      { colKey: 'price_range', title: t('kb.priceRange'), width: 130 },
      { colKey: 'level', title: t('kb.level'), width: 90 },
      { colKey: 'body_type', title: t('kb.bodyType'), width: 90 },
      { colKey: 'fuel_type', title: t('kb.fuelType'), width: 90 },
      { colKey: 'status', title: t('common.status'), width: 90, cell: ({ row }: CellProps) => <StatusTag row={row} /> },
      { colKey: 'visibility', title: t('common.visibility'), width: 110, cell: ({ row }: CellProps) => (row.visibility === 'public' ? <Tag theme="success">{t('common.public')}</Tag> : <Tag theme="default">{t('common.private')}</Tag>) },
    ]}
    createFields={[
      { key: 'brand_id', label: t('kb.brandId'), type: 'select', required: true, options: brandOptions },
      { key: 'name', label: t('kb.modelName'), required: true },
      { key: 'code', label: t('kb.modelCode'), required: true },
      { key: 'price_range', label: t('kb.priceRangeField') },
      { key: 'level', label: t('kb.level'), placeholder: t('kb.levelPh') },
      { key: 'body_type', label: t('kb.bodyTypeField'), placeholder: t('kb.bodyTypePh') },
      { key: 'fuel_type', label: t('kb.fuelTypeField'), placeholder: t('kb.fuelTypePh') },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    editFields={[
      { key: 'name', label: t('kb.modelName'), required: true },
      { key: 'price_range', label: t('kb.priceRangeField') },
      { key: 'level', label: t('kb.level') },
      { key: 'body_type', label: t('kb.bodyTypeField') },
      { key: 'fuel_type', label: t('kb.fuelTypeField') },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    rowToForm={(row) => defaultRowToForm(MODEL_ALL_FIELDS, row)}
  />
}

/** 知识库车型参数维护 Tab。 */
function SpecTab({ modelOptions }: { modelOptions: Option[] }) {
  const { t } = useI18n()
  return <EntityCrud
    base="/api/v1/admin/knowledge/specs"
    title={t('kb.specTitle')}
    filters={[
      { key: 'keyword', label: t('kb.filterParam') },
      { key: 'model_id', label: t('kb.model'), options: modelOptions },
      { key: 'category', label: t('common.category') },
      { key: 'status', label: t('common.status'), options: [{ label: t('common.enable'), value: 1 }, { label: t('common.disable'), value: 0 }] },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'model_id', title: t('kb.modelIdCol'), width: 90 },
      { colKey: 'param_name', title: t('kb.param'), width: 160 },
      { colKey: 'param_value', title: t('kb.value'), width: 160 },
      { colKey: 'param_unit', title: t('kb.unit'), width: 80 },
      { colKey: 'category', title: t('common.category'), width: 120 },
      { colKey: 'is_price', title: t('kb.isPrice'), width: 90, cell: ({ row }: CellProps) => <Tag theme={row.is_price ? 'primary' : 'default'}>{row.is_price ? t('common.yes') : t('common.no')}</Tag> },
      { colKey: 'status', title: t('common.status'), width: 90, cell: ({ row }: CellProps) => <StatusTag row={row} /> },
    ]}
    createFields={[
      { key: 'model_id', label: t('kb.modelId'), type: 'select', required: true, options: modelOptions },
      { key: 'param_name', label: t('kb.paramName'), required: true },
      { key: 'param_value', label: t('kb.paramValue') },
      { key: 'param_unit', label: t('kb.unit') },
      { key: 'category', label: t('common.category'), placeholder: t('kb.specCategoryPh') },
      { key: 'sort', label: t('common.sort'), type: 'number' },
    ]}
    editFields={[
      { key: 'param_name', label: t('kb.paramName'), required: true },
      { key: 'param_value', label: t('kb.paramValue') },
      { key: 'param_unit', label: t('kb.unit') },
      { key: 'category', label: t('common.category') },
      { key: 'is_price', label: t('kb.isPrice'), type: 'bool' },
      { key: 'sort', label: t('common.sort'), type: 'number' },
    ]}
    rowToForm={(row) => defaultRowToForm(SPEC_ALL_FIELDS, row)}
  />
}

/** 知识库竞品对比维护 Tab。 */
function CompareTab({ modelOptions }: { modelOptions: Option[] }) {
  const { t } = useI18n()
  const visibilityOptions = useVisibilityOptions()
  return <EntityCrud
    base="/api/v1/admin/knowledge/compares"
    title={t('kb.compareTitle')}
    filters={[
      { key: 'model_id', label: t('kb.model'), options: modelOptions },
      { key: 'competitor_brand', label: t('kb.compareCompetitorBrand') },
      { key: 'contains_price', label: t('kb.containsPrice'), options: [{ label: t('common.yes'), value: 'true' }, { label: t('common.no'), value: 'false' }] },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'our_model_id', title: t('kb.ourModel'), width: 110 },
      { colKey: 'competitor_brand', title: t('kb.compareCompetitorBrand'), width: 120 },
      { colKey: 'competitor_model', title: t('kb.compareCompetitorModel'), width: 140 },
      { colKey: 'compare_type', title: t('kb.compareType'), width: 120 },
      { colKey: 'contains_price', title: t('kb.containsPrice'), width: 90, cell: ({ row }: CellProps) => <Tag theme={row.contains_price ? 'primary' : 'default'}>{boolToInt(row.contains_price) ? t('common.yes') : t('common.no')}</Tag> },
      { colKey: 'content', title: t('kb.compareItemsCol'), width: 220, ellipsis: true, cell: ({ row }: CellProps) => <span className="text-xs text-gray-500">{t('kb.compareItemsCount', { count: parseJsonArray(row.content).length || '—' })}</span> },
      { colKey: 'status', title: t('common.status'), width: 90, cell: ({ row }: CellProps) => <StatusTag row={row} /> },
      { colKey: 'visibility', title: t('common.visibility'), width: 110, cell: ({ row }: CellProps) => (row.visibility === 'public' ? <Tag theme="success">{t('common.public')}</Tag> : <Tag theme="default">{t('common.private')}</Tag>) },
    ]}
    createFields={[
      { key: 'our_model_id', label: t('kb.ourModel'), type: 'select', required: true, options: modelOptions },
      { key: 'competitor_brand', label: t('kb.compareCompetitorBrand'), required: true },
      { key: 'competitor_model', label: t('kb.compareCompetitorModel'), required: true },
      { key: 'compare_type', label: t('kb.compareType'), placeholder: t('kb.compareTypePh') },
      { key: 'contains_price', label: t('kb.containsPrice'), type: 'bool', defaultValue: false },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
      { key: 'items', label: t('kb.compareItemsJson'), type: 'json', placeholder: t('kb.compareItemsPh'), defaultValue: '[\n  {"aspect":"","our_value":"","their_value":"","conclusion":""}\n]' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    editFields={[
      { key: 'competitor_brand', label: t('kb.compareCompetitorBrand'), required: true },
      { key: 'competitor_model', label: t('kb.compareCompetitorModel'), required: true },
      { key: 'compare_type', label: t('kb.compareType') },
      { key: 'contains_price', label: t('kb.containsPrice'), type: 'bool' },
      { key: 'items', label: t('kb.compareItemsJson'), type: 'json' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    rowToForm={(row) => ({
      our_model_id: row.our_model_id,
      competitor_brand: row.competitor_brand || '',
      competitor_model: row.competitor_model || '',
      compare_type: row.compare_type || '',
      contains_price: boolToInt(row.contains_price) === 1,
      status: row.status ?? 1,
      items: typeof row.content === 'string' && row.content ? (() => { try { return JSON.stringify(JSON.parse(row.content), null, 2) } catch { return row.content } })() : '[]',
    })}
    emptyForm={{ contains_price: false, status: 1 }}
  />
}

/** 知识库片段维护与向量化状态 Tab。 */
function FragmentTab() {
  const { t } = useI18n()
  const visibilityOptions = useVisibilityOptions()
  return <EntityCrud
    base="/api/v1/admin/knowledge/fragments"
    title={t('kb.fragment')}
    filters={[
      { key: 'keyword', label: t('kb.filterTitleContent') },
      { key: 'category', label: t('common.category') },
      { key: 'status', label: t('common.status'), options: [{ label: t('common.enable'), value: 1 }, { label: t('common.disable'), value: 0 }] },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'category', title: t('common.category'), width: 120 },
      { colKey: 'title', title: t('common.title'), width: 180 },
      { colKey: 'content', title: t('common.content'), ellipsis: true },
      { colKey: 'tags', title: t('common.tags'), width: 180, cell: ({ row }: CellProps) => <span className="text-xs text-gray-500">{parseJsonArray(row.tags).join('、') || '—'}</span> },
      { colKey: 'vectorized', title: t('kb.vector'), width: 90, cell: ({ row }: CellProps) => <Tag theme={row.vectorized ? 'success' : 'warning'}>{row.vectorized ? t('kb.vectorized') : t('kb.pendingVector')}</Tag> },
      { colKey: 'status', title: t('common.status'), width: 90, cell: ({ row }: CellProps) => <StatusTag row={row} /> },
      { colKey: 'visibility', title: t('common.visibility'), width: 110, cell: ({ row }: CellProps) => (row.visibility === 'public' ? <Tag theme="success">{t('common.public')}</Tag> : <Tag theme="default">{t('common.private')}</Tag>) },
    ]}
    createFields={[
      { key: 'title', label: t('common.title'), required: true },
      { key: 'category', label: t('common.category'), placeholder: t('kb.fragmentCategoryPh') },
      { key: 'content', label: t('common.content'), type: 'textarea', required: true },
      { key: 'tags', label: t('common.tags'), type: 'list', placeholder: t('kb.commaSeparated') },
      { key: 'applicable_models', label: t('kb.applicableModels'), type: 'list', placeholder: t('kb.commaSeparated') },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    editFields={[
      { key: 'title', label: t('common.title'), required: true },
      { key: 'category', label: t('common.category') },
      { key: 'content', label: t('common.content'), type: 'textarea', required: true },
      { key: 'tags', label: t('common.tags'), type: 'list' },
      { key: 'applicable_models', label: t('kb.applicableModels'), type: 'list' },
      { key: 'sort', label: t('common.sort'), type: 'number' },
      { key: 'visibility', label: t('kb.visibilityField'), type: 'select', options: visibilityOptions },
    ]}
    rowToForm={(row) => defaultRowToForm(FRAGMENT_ALL_FIELDS, row)}
  />
}
// *_ALL_FIELDS 只服务 defaultRowToForm 的行→表单转换：该函数只读 key+type
// （label/options/placeholder 从不被消费），故此处不再重复维护可渲染文案——
// 表单里显示的标签以各 Tab 的 createFields/editFields 为单一事实源。
const BRAND_ALL_FIELDS: FieldSpec[] = [
  { key: 'name' },
  { key: 'code' },
  { key: 'country' },
  { key: 'founded_year', type: 'number' },
  { key: 'logo' },
  { key: 'status', type: 'number' },
  { key: 'sort', type: 'number' },
  { key: 'description' },
  { key: 'visibility', type: 'select' },
]
const MODEL_ALL_FIELDS: FieldSpec[] = [
  { key: 'brand_id' },
  { key: 'name' },
  { key: 'code' },
  { key: 'price_range' },
  { key: 'level' },
  { key: 'body_type' },
  { key: 'fuel_type' },
  { key: 'status', type: 'number' },
  { key: 'sort', type: 'number' },
  { key: 'visibility', type: 'select' },
]
const SPEC_ALL_FIELDS: FieldSpec[] = [
  { key: 'model_id' },
  { key: 'param_name' },
  { key: 'param_value' },
  { key: 'param_unit' },
  { key: 'category' },
  { key: 'is_price', type: 'bool' },
  { key: 'sort', type: 'number' },
]
const FRAGMENT_ALL_FIELDS: FieldSpec[] = [
  { key: 'title' },
  { key: 'category' },
  { key: 'content' },
  { key: 'tags', type: 'list' },
  { key: 'applicable_models', type: 'list' },
  { key: 'status', type: 'number' },
  { key: 'sort', type: 'number' },
  { key: 'visibility', type: 'select' },
]

// KnowledgeTab 后台「知识库」管理页：按分片/文档分组检索与维护知识资产。
export default function KnowledgeTab() {
  const { t } = useI18n()
  const [tab, setTab] = useState('fragments')
  const [brandOptions, setBrandOptions] = useState<Option[]>([])
  const [modelOptions, setModelOptions] = useState<Option[]>([])

  const loadLookups = useCallback(async () => {
    const [br, mo] = await Promise.all([
      AUTH('/api/v1/admin/knowledge/brands?page_size=100'),
      AUTH('/api/v1/admin/knowledge/models?page_size=100'),
    ])
    setBrandOptions((br?.data?.list || []).map((x: CrudRow) => ({ label: x.name, value: x.id })))
    setModelOptions((mo?.data?.list || []).map((x: CrudRow) => ({ label: x.name, value: x.id })))
  }, [])

  useEffect(() => {
    if (['models', 'specs', 'compares'].includes(tab) && modelOptions.length === 0) void loadLookups()
  }, [loadLookups, modelOptions.length, tab])

  const reloadCache = async () => {
    const j = await AUTH('/api/v1/admin/knowledge/reload', { method: 'POST' })
    if (j?.code === 0) MessagePlugin.success(t('kb.cacheReloaded'))
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex items-center justify-between gap-3 flex-wrap">
        <div>
          <h3 className="text-base font-bold text-gray-800">{t('kb.pageTitle')}</h3>
          <p className="text-xs text-gray-400 mt-0.5">{t('kb.pageDesc')}</p>
        </div>
        <Button theme="default" variant="outline" onClick={reloadCache}>{t('kb.reloadCache')}</Button>
      </div>
      <div className="bg-white rounded-lg shadow-sm p-4">
        <Tabs value={tab} onChange={(v) => setTab(String(v))} placement="top">
          <Tabs.TabPanel value="fragments" label={t('kb.fragment')}><FragmentTab /></Tabs.TabPanel>
          <Tabs.TabPanel value="brands" label={t('kb.brand')}><BrandTab /></Tabs.TabPanel>
          <Tabs.TabPanel value="models" label={t('kb.model')}><ModelTab brandOptions={brandOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="specs" label={t('kb.spec')}><SpecTab modelOptions={modelOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="compares" label={t('kb.compare')}><CompareTab modelOptions={modelOptions} /></Tabs.TabPanel>
        </Tabs>
      </div>
    </div>
  )
}
