// F4 标签体系增强：标签、自动打标规则、T向量权重映射全部 CRUD，并保留只读规则说明。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, MessagePlugin, Tag, Tabs } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { CellProps } from '../../types'
import {
  EntityCrud,
  defaultRowToForm,
  parseJsonArray,
  statusTag,
  type FieldSpec,
  type Option,
} from './EntityCrud'

const TAG_CATS = [
  { key: 'intent', name: '意向等级' },
  { key: 'source', name: '来源渠道' },
  { key: 'car', name: '兴趣车型' },
  { key: 'type', name: '客户类型' },
  { key: 'status', name: '跟进状态' },
]
const RULE_TYPE_OPTS: Option[] = [
  { label: '关键词', value: 'keyword' },
  { label: '意图', value: 'intent' },
  { label: '抗性', value: 'resistance' },
]
const DIRECTION_OPTS: Option[] = [{ label: '上抬', value: 'up' }, { label: '下压', value: 'down' }]
const STATUS_OPTS: Option[] = [{ label: '启用', value: 1 }, { label: '停用', value: 0 }]
const T_VECTOR_OPTS: Option[] = [
  { label: '0 信任', value: 0 },
  { label: '1 价值感知', value: 1 },
  { label: '2 决策压力', value: 2 },
  { label: '3 行动准备', value: 3 },
]
const AUTO_TAG_RULES = [
  { trigger: '消息中出现手机号', tag: '已留资', category: '跟进状态' },
  { trigger: '消息中提到试驾', tag: '高意向', category: '意向等级' },
  { trigger: '消息中提到价格/预算', tag: '中意向', category: '意向等级' },
  { trigger: '消息中提到到店/看车', tag: '已到店', category: '跟进状态' },
]

const TAG_ALL_FIELDS: FieldSpec[] = [
  { key: 'name', label: '标签名称' },
  { key: 'code', label: '标签编码' },
  { key: 'category', label: '分类' },
  { key: 'weight', label: '权重', type: 'number' },
  { key: 'description', label: '描述' },
  { key: 'status', label: '状态', type: 'number' },
]
const TAG_RULE_ALL_FIELDS: FieldSpec[] = [
  { key: 'name', label: '规则名称' },
  { key: 'rule_type', label: '规则类型' },
  { key: 'match_pattern', label: '匹配词', type: 'list' },
  { key: 'target_tag_id', label: '目标标签' },
  { key: 'weight_bonus', label: '权重加成', type: 'number' },
  { key: 'status', label: '状态', type: 'number' },
]
const TAG_WEIGHT_ALL_FIELDS: FieldSpec[] = [
  { key: 'tag_id', label: '标签' },
  { key: 'tag_code', label: '标签编码' },
  { key: 't_vector_index', label: 'T向量维度', type: 'number' },
  { key: 'weight_delta', label: '权重增量', type: 'number' },
  { key: 'direction', label: '方向' },
  { key: 'status', label: '状态', type: 'number' },
]

/** 标签定义维护 Tab。 */
function TagTab({ categoryOptions }: { categoryOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/tags"
    title="标签"
    filters={[
      { key: 'keyword', label: '名称/编码/描述' },
      { key: 'category', label: '分类', options: categoryOptions },
      { key: 'status', label: '状态', options: STATUS_OPTS },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'name', title: '名称', width: 130 },
      { colKey: 'code', title: '编码', width: 140 },
      { colKey: 'category', title: '分类', width: 120, cell: (p: CellProps) => <span className="text-xs text-gray-500">{p.row.category || '-'}</span> },
      { colKey: 'weight', title: '权重', width: 90 },
      { colKey: 'description', title: '描述', ellipsis: true },
      { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => statusTag(p.row) },
    ]}
    createFields={[
      { key: 'name', label: '标签名称', required: true },
      { key: 'code', label: '标签编码', required: true, placeholder: '英文小写，如 high_intent' },
      { key: 'category', label: '分类', type: 'select', options: categoryOptions, defaultValue: 'intent' },
      { key: 'weight', label: '权重', type: 'number', defaultValue: 1 },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
      { key: 'description', label: '描述', type: 'textarea' },
    ]}
    editFields={[
      { key: 'name', label: '标签名称', required: true },
      { key: 'category', label: '分类', type: 'select', options: categoryOptions },
      { key: 'weight', label: '权重', type: 'number' },
      { key: 'description', label: '描述', type: 'textarea' },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_ALL_FIELDS, row)}
  />
}

/** 标签规则维护 Tab。 */
function TagRuleTab({ tagOptions }: { tagOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/tag-rules"
    title="打标规则"
    filters={[
      { key: 'keyword', label: '规则/目标标签' },
      { key: 'rule_type', label: '类型', options: RULE_TYPE_OPTS },
      { key: 'status', label: '状态', options: STATUS_OPTS },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'name', title: '规则名称', width: 160 },
      { colKey: 'rule_type', title: '类型', width: 100, cell: (p: CellProps) => <Tag theme="primary">{RULE_TYPE_OPTS.find((x) => x.value === p.row.rule_type)?.label || p.row.rule_type}</Tag> },
      { colKey: 'match_pattern', title: '匹配词', ellipsis: true, cell: (p: CellProps) => <span className="text-xs text-gray-500">{parseJsonArray(p.row.match_pattern).join('、') || '-'}</span> },
      { colKey: 'target_tag_name', title: '目标标签', width: 130 },
      { colKey: 'weight_bonus', title: '加成', width: 90 },
      { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => statusTag(p.row) },
    ]}
    createFields={[
      { key: 'name', label: '规则名称', required: true },
      { key: 'rule_type', label: '规则类型', type: 'select', required: true, options: RULE_TYPE_OPTS, defaultValue: 'keyword' },
      { key: 'match_pattern', label: '匹配词/意图词', type: 'list', required: true, placeholder: '逗号分隔，如：太贵，价格高' },
      { key: 'target_tag_id', label: '目标标签', type: 'select', required: true, options: tagOptions },
      { key: 'weight_bonus', label: '权重加成', type: 'number', defaultValue: 1 },
      { key: 'status', label: '状态', type: 'number', defaultValue: 1 },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_RULE_ALL_FIELDS, row)}
  />
}

/** 标签权重映射维护 Tab。 */
function TagWeightTab({ tagOptions }: { tagOptions: Option[] }) {
  return <EntityCrud
    base="/api/v1/admin/tag-weights"
    title="权重映射"
    filters={[
      { key: 'tag_id', label: '标签', options: tagOptions },
      { key: 't_vector_index', label: 'T向量维度', options: T_VECTOR_OPTS },
    ]}
    columns={[
      { colKey: 'id', title: 'ID', width: 70 },
      { colKey: 'tag_id', title: '标签ID', width: 90 },
      { colKey: 'tag_code', title: '标签编码', width: 150 },
      { colKey: 't_vector_index', title: 'T维度', width: 90 },
      { colKey: 'weight_delta', title: '增量', width: 90 },
      { colKey: 'direction', title: '方向', width: 90, cell: (p: CellProps) => <Tag theme={p.row.direction === 'down' ? 'warning' : 'primary'}>{p.row.direction === 'down' ? '下压' : '上抬'}</Tag> },
      { colKey: 'status', title: '状态', width: 90, cell: (p: CellProps) => statusTag(p.row) },
    ]}
    createFields={[
      { key: 'tag_id', label: '标签', type: 'select', required: true, options: tagOptions },
      { key: 't_vector_index', label: 'T向量维度', type: 'select', required: true, options: T_VECTOR_OPTS, defaultValue: 0 },
      { key: 'weight_delta', label: '权重增量', type: 'number', required: true, defaultValue: 0.1 },
      { key: 'direction', label: '方向', type: 'select', options: DIRECTION_OPTS, defaultValue: 'up' },
    ]}
    editFields={[
      { key: 'tag_id', label: '标签', type: 'select', options: tagOptions },
      { key: 't_vector_index', label: 'T向量维度', type: 'select', options: T_VECTOR_OPTS },
      { key: 'weight_delta', label: '权重增量', type: 'number' },
      { key: 'direction', label: '方向', type: 'select', options: DIRECTION_OPTS },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_WEIGHT_ALL_FIELDS, row)}
  />
}

/** 标签体系 Tab：维护标签、规则、权重与 TTL。 */
export function TagSystemTab() {
  const [tab, setTab] = useState('tags')
  const [tagOptions, setTagOptions] = useState<Option[]>([])
  const categoryOptions = useMemo(() => TAG_CATS.map((x) => ({ label: x.name, value: x.key })), [])
  const loadTags = useCallback(async () => {
    const j = await AUTH('/api/v1/admin/tags?page_size=500')
    if (j?.code === 0) {
      setTagOptions((j.data?.list || []).filter((t: any) => Number(t.status) === 1).map((t: any) => ({ label: `${t.name}(${t.code})`, value: t.id })))
    }
  }, [])
  useEffect(() => {
    if (['rules', 'weights'].includes(tab) && tagOptions.length === 0) void loadTags()
  }, [loadTags, tab, tagOptions.length])

  const reloadCache = async () => {
    const j = await AUTH('/api/v1/admin/tags/reload', { method: 'POST' })
    if (j?.code === 0) MessagePlugin.success('标签缓存已重载')
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-base font-bold text-gray-800">标签体系</h3>
          <p className="text-xs text-gray-400 mt-0.5">标签、打标规则和 T 向量权重映射均可维护。</p>
        </div>
        <Button variant="outline" onClick={reloadCache}>重载标签缓存</Button>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-4">
        <Tabs value={tab} onChange={(v) => setTab(String(v))} placement="top">
          <Tabs.TabPanel value="tags" label="标签字典"><TagTab categoryOptions={categoryOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="rules" label="打标规则"><TagRuleTab tagOptions={tagOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="weights" label="权重映射"><TagWeightTab tagOptions={tagOptions} /></Tabs.TabPanel>
        </Tabs>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="mb-4">
          <h3 className="text-base font-bold text-gray-800">常见自动打标说明</h3>
          <p className="text-xs text-gray-400 mt-0.5">下面是运行时规则示例，实际以「打标规则」列表为准。</p>
        </div>
        <table className="w-full text-sm">
          <thead><tr className="border-b border-gray-200 text-left text-xs font-semibold text-gray-500"><th className="py-2 px-3 w-8">#</th><th className="py-2 px-3">触发条件</th><th className="py-2 px-3">自动打标</th><th className="py-2 px-3">所属分类</th></tr></thead>
          <tbody>
            {AUTO_TAG_RULES.map((r, i) => (
              <tr key={i} className="border-b border-gray-50"><td className="py-2.5 px-3 text-xs text-gray-400">{i + 1}</td><td className="py-2.5 px-3 text-gray-700">{r.trigger}</td><td className="py-2.5 px-3"><span className="inline-block text-xs px-2 py-0.5 rounded-full bg-indigo-50 text-indigo-600">{r.tag}</span></td><td className="py-2.5 px-3 text-xs text-gray-500">{r.category}</td></tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
