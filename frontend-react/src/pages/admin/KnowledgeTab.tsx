// F2 知识库管理页：品牌/车型/参数/竞品/片段五类后台 CRUD，并展示向量状态与缓存重载。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, MessagePlugin, Tabs, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CrudRow } from '../../hooks/useCrud'
import type { CellProps } from '../../types'
import {
  EntityCrud,
  boolToInt,
  defaultRowToForm,
  parseJsonArray,
  statusTag,
  type FieldSpec,
  type Option,
} from './EntityCrud'

function BrandTab() {
  return <EntityCrud
    base="/api/v1/admin/knowledge/brands"
    title="品牌"
    filters={[{ key: 'keyword', label: '名称/编码' }, { key: 'status', label: '状态', options: [{ label: '启用', value: 1 }, { label: '停用', value: 0 }] }]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'name', title: '名称', width: 140 },
      { colKey: 'code', title: '编码', width: 120 },
      { colKey: 'country', title: '国家', width: 100 },
      { colKey: 'founded_year', title: '成立年', width: 90 },
      { colKey: 'description', title: '描述', ellipsis: true },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }: CellProps) => statusTag(row) },
    ]}
    createFields={[
      { key: 'name', label: '品牌名称', required: true },
      { key: 'code', label: '品牌编码', required: true, placeholder: '如 rox' },
      { key: 'country', label: '国家' },
      { key: 'founded_year', label: '成立年份', type: 'number' },
      { key: 'logo', label: 'Logo URL' },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
      { key: 'sort', label: '排序', type: 'number' },
      { key: 'description', label: '描述', type: 'textarea' },
    ]}
    editFields={[
      { key: 'name', label: '品牌名称', required: true },
      { key: 'country', label: '国家' },
      { key: 'founded_year', label: '成立年份', type: 'number' },
      { key: 'logo', label: 'Logo URL' },
      { key: 'sort', label: '排序', type: 'number' },
      { key: 'description', label: '描述', type: 'textarea' },
    ]}
    rowToForm={(row) => defaultRowToForm(BRAND_ALL_FIELDS, row)}
  />
}
const BRAND_ALL_FIELDS: FieldSpec[] = [
  { key: 'name', label: '品牌名称' },
  { key: 'code', label: '品牌编码' },
  { key: 'country', label: '国家' },
  { key: 'founded_year', label: '成立年份', type: 'number' },
  { key: 'logo', label: 'Logo URL' },
  { key: 'status', label: '状态', type: 'number' },
  { key: 'sort', label: '排序', type: 'number' },
  { key: 'description', label: '描述' },
]

function ModelTab({ brandOptions }: { brandOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/knowledge/models"
    title="车型"
    filters={[{ key: 'keyword', label: '名称/编码' }, { key: 'brand_id', label: '品牌', options: brandOptions }]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'name', title: '车型', width: 160 },
      { colKey: 'brand_name', title: '品牌', width: 120 },
      { colKey: 'code', title: '编码', width: 120 },
      { colKey: 'price_range', title: '价格', width: 130 },
      { colKey: 'level', title: '级别', width: 90 },
      { colKey: 'body_type', title: '车身', width: 90 },
      { colKey: 'fuel_type', title: '能源', width: 90 },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }: CellProps) => statusTag(row) },
    ]}
    createFields={[
      { key: 'brand_id', label: '所属品牌', type: 'select', required: true, options: brandOptions },
      { key: 'name', label: '车型名称', required: true },
      { key: 'code', label: '车型编码', required: true },
      { key: 'price_range', label: '价格区间' },
      { key: 'level', label: '级别', placeholder: '紧凑型/中型/中大型' },
      { key: 'body_type', label: '车身形式', placeholder: 'SUV/轿车' },
      { key: 'fuel_type', label: '能源类型', placeholder: '增程式/插混/纯电' },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    editFields={[
      { key: 'name', label: '车型名称', required: true },
      { key: 'price_range', label: '价格区间' },
      { key: 'level', label: '级别' },
      { key: 'body_type', label: '车身形式' },
      { key: 'fuel_type', label: '能源类型' },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    rowToForm={(row) => defaultRowToForm(MODEL_ALL_FIELDS, row)}
  />
}
const MODEL_ALL_FIELDS: FieldSpec[] = [
  { key: 'brand_id', label: '所属品牌' },
  { key: 'name', label: '车型名称' },
  { key: 'code', label: '车型编码' },
  { key: 'price_range', label: '价格区间' },
  { key: 'level', label: '级别' },
  { key: 'body_type', label: '车身形式' },
  { key: 'fuel_type', label: '能源类型' },
  { key: 'status', label: '状态', type: 'number' },
  { key: 'sort', label: '排序', type: 'number' },
]

function SpecTab({ modelOptions }: { modelOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/knowledge/specs"
    title="车型参数"
    filters={[
      { key: 'keyword', label: '参数名/值' },
      { key: 'model_id', label: '车型', options: modelOptions },
      { key: 'category', label: '分类' },
      { key: 'status', label: '状态', options: [{ label: '启用', value: 1 }, { label: '停用', value: 0 }] },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'model_id', title: '车型ID', width: 90 },
      { colKey: 'param_name', title: '参数', width: 160 },
      { colKey: 'param_value', title: '值', width: 160 },
      { colKey: 'param_unit', title: '单位', width: 80 },
      { colKey: 'category', title: '分类', width: 120 },
      { colKey: 'is_price', title: '价格项', width: 90, cell: ({ row }: CellProps) => <Tag theme={row.is_price ? 'primary' : 'default'}>{row.is_price ? '是' : '否'}</Tag> },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }: CellProps) => statusTag(row) },
    ]}
    createFields={[
      { key: 'model_id', label: '所属车型', type: 'select', required: true, options: modelOptions },
      { key: 'param_name', label: '参数名', required: true },
      { key: 'param_value', label: '参数值' },
      { key: 'param_unit', label: '单位' },
      { key: 'category', label: '分类', placeholder: '动力/车身/智能' },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    editFields={[
      { key: 'param_name', label: '参数名', required: true },
      { key: 'param_value', label: '参数值' },
      { key: 'param_unit', label: '单位' },
      { key: 'category', label: '分类' },
      { key: 'is_price', label: '价格项', type: 'bool' },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    rowToForm={(row) => defaultRowToForm(SPEC_ALL_FIELDS, row)}
  />
}
const SPEC_ALL_FIELDS: FieldSpec[] = [
  { key: 'model_id', label: '所属车型' },
  { key: 'param_name', label: '参数名' },
  { key: 'param_value', label: '参数值' },
  { key: 'param_unit', label: '单位' },
  { key: 'category', label: '分类' },
  { key: 'is_price', label: '价格项', type: 'bool' },
  { key: 'sort', label: '排序', type: 'number' },
]

function CompareTab({ modelOptions }: { modelOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/knowledge/compares"
    title="竞品对比"
    filters={[
      { key: 'model_id', label: '车型', options: modelOptions },
      { key: 'competitor_brand', label: '竞品品牌' },
      { key: 'contains_price', label: '含价格', options: [{ label: '是', value: 'true' }, { label: '否', value: 'false' }] },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'our_model_id', title: '我方车型', width: 110 },
      { colKey: 'competitor_brand', title: '竞品品牌', width: 120 },
      { colKey: 'competitor_model', title: '竞品车型', width: 140 },
      { colKey: 'compare_type', title: '对比类型', width: 120 },
      { colKey: 'contains_price', title: '含价格', width: 90, cell: ({ row }: CellProps) => <Tag theme={row.contains_price ? 'primary' : 'default'}>{boolToInt(row.contains_price) ? '是' : '否'}</Tag> },
      { colKey: 'content', title: '对比项', width: 220, ellipsis: true, cell: ({ row }: CellProps) => <span className="text-xs text-gray-500">{parseJsonArray(row.content).length || '—'} 项</span> },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }: CellProps) => statusTag(row) },
    ]}
    createFields={[
      { key: 'our_model_id', label: '我方车型', type: 'select', required: true, options: modelOptions },
      { key: 'competitor_brand', label: '竞品品牌', required: true },
      { key: 'competitor_model', label: '竞品车型', required: true },
      { key: 'compare_type', label: '对比类型', placeholder: '同级/上下探' },
      { key: 'contains_price', label: '含价格', type: 'bool', defaultValue: false },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
      { key: 'items', label: '对比项 JSON', type: 'json', placeholder: '[{"aspect":"续航","our_value":"1200km","their_value":"1100km","conclusion":"更优"}]', defaultValue: '[\n  {"aspect":"","our_value":"","their_value":"","conclusion":""}\n]' },
    ]}
    editFields={[
      { key: 'competitor_brand', label: '竞品品牌', required: true },
      { key: 'competitor_model', label: '竞品车型', required: true },
      { key: 'compare_type', label: '对比类型' },
      { key: 'contains_price', label: '含价格', type: 'bool' },
      { key: 'items', label: '对比项 JSON', type: 'json' },
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

function FragmentTab() {
  return <EntityCrud
    base="/api/v1/admin/knowledge/fragments"
    title="知识片段"
    filters={[
      { key: 'keyword', label: '标题/内容' },
      { key: 'category', label: '分类' },
      { key: 'status', label: '状态', options: [{ label: '启用', value: 1 }, { label: '停用', value: 0 }] },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'category', title: '分类', width: 120 },
      { colKey: 'title', title: '标题', width: 180 },
      { colKey: 'content', title: '内容', ellipsis: true },
      { colKey: 'tags', title: '标签', width: 180, cell: ({ row }: CellProps) => <span className="text-xs text-gray-500">{parseJsonArray(row.tags).join('、') || '—'}</span> },
      { colKey: 'vectorized', title: '向量', width: 90, cell: ({ row }: CellProps) => <Tag theme={row.vectorized ? 'success' : 'warning'}>{row.vectorized ? '已向量化' : '待向量化'}</Tag> },
      { colKey: 'status', title: '状态', width: 90, cell: ({ row }: CellProps) => statusTag(row) },
    ]}
    createFields={[
      { key: 'title', label: '标题', required: true },
      { key: 'category', label: '分类', placeholder: '企业知识/产品知识' },
      { key: 'content', label: '内容', type: 'textarea', required: true },
      { key: 'tags', label: '标签', type: 'list', placeholder: '逗号分隔' },
      { key: 'applicable_models', label: '适用车型', type: 'list', placeholder: '逗号分隔' },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    editFields={[
      { key: 'title', label: '标题', required: true },
      { key: 'category', label: '分类' },
      { key: 'content', label: '内容', type: 'textarea', required: true },
      { key: 'tags', label: '标签', type: 'list' },
      { key: 'applicable_models', label: '适用车型', type: 'list' },
      { key: 'sort', label: '排序', type: 'number' },
    ]}
    rowToForm={(row) => defaultRowToForm(FRAGMENT_ALL_FIELDS, row)}
  />
}
const FRAGMENT_ALL_FIELDS: FieldSpec[] = [
  { key: 'title', label: '标题' },
  { key: 'category', label: '分类' },
  { key: 'content', label: '内容' },
  { key: 'tags', label: '标签', type: 'list' },
  { key: 'applicable_models', label: '适用车型', type: 'list' },
  { key: 'status', label: '状态', type: 'number' },
  { key: 'sort', label: '排序', type: 'number' },
]

export default function KnowledgeTab() {
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
    if (j?.code === 0) MessagePlugin.success('知识库缓存已重载')
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex items-center justify-between gap-3 flex-wrap">
        <div>
          <h3 className="text-base font-bold text-gray-800">知识库管理</h3>
          <p className="text-xs text-gray-400 mt-0.5">维护品牌、车型、参数、竞品与片段；保存片段会自动走向量化。</p>
        </div>
        <Button theme="default" variant="outline" onClick={reloadCache}>重载缓存</Button>
      </div>
      <div className="bg-white rounded-lg shadow-sm p-4">
        <Tabs value={tab} onChange={(v) => setTab(String(v))} placement="top">
          <Tabs.TabPanel value="fragments" label="知识片段"><FragmentTab /></Tabs.TabPanel>
          <Tabs.TabPanel value="brands" label="品牌"><BrandTab /></Tabs.TabPanel>
          <Tabs.TabPanel value="models" label="车型"><ModelTab brandOptions={brandOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="specs" label="参数"><SpecTab modelOptions={modelOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="compares" label="竞品"><CompareTab modelOptions={modelOptions} /></Tabs.TabPanel>
        </Tabs>
      </div>
    </div>
  )
}
