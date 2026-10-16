// F4 标签体系增强：标签、自动打标规则、T向量权重映射全部 CRUD，并保留只读规则说明。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, MessagePlugin, Tag, Tabs } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import type { CrudRow } from '../../hooks/useCrud'
import type { CellProps } from '../../types'
import {
  EntityCrud,
  defaultRowToForm,
  parseJsonArray,
  StatusTag,
  type FieldSpec,
  type Option,
} from './EntityCrud'

// P2-14：模块级常量拿不到 t()（hook 只能在组件内调用），因此这些表只存
// 「后端契约值 → I18nKey」的映射，翻译推迟到组件渲染时做。
// 左侧的 intent/source/keyword/up… 全是后端持久化值，属于数据不是文案，永不翻译。
/** 标签分类：key 为后端持久化值，value 为展示名 I18nKey。 */
const TAG_CAT_KEYS: Record<string, I18nKey> = {
  intent: 'tagSystem.catIntent',
  source: 'tagSystem.catSource',
  car: 'tagSystem.catCar',
  type: 'tagSystem.catType',
  status: 'tagSystem.catStatus',
}
/** 打标规则类型：keyword/intent/resistance 为后端值。 */
const RULE_TYPE_KEYS: Record<string, I18nKey> = {
  keyword: 'tagSystem.ruleKeyword',
  intent: 'tagSystem.ruleIntent',
  resistance: 'tagSystem.ruleResistance',
}
/** 权重方向：up/down 为后端值。 */
const DIRECTION_KEYS: Record<string, I18nKey> = {
  up: 'tagSystem.dirUp',
  down: 'tagSystem.dirDown',
}
/** 启用/停用下拉（1/0 为后端值），复用通用词表。 */
const STATUS_KEYS: { key: I18nKey; value: number }[] = [
  { key: 'common.enable', value: 1 },
  { key: 'common.disable', value: 0 },
]
/** T 向量维度下拉（0~3 为后端索引值）。 */
const T_VECTOR_KEYS: { key: I18nKey; value: number }[] = [
  { key: 'tagSystem.tvecTrust', value: 0 },
  { key: 'tagSystem.tvecValue', value: 1 },
  { key: 'tagSystem.tvecPressure', value: 2 },
  { key: 'tagSystem.tvecAction', value: 3 },
]
/** 常见自动打标示例：纯展示的运行时规则样例，实际以「打标规则」列表为准。 */
const AUTO_TAG_RULE_KEYS: { trigger: I18nKey; tag: I18nKey; category: I18nKey }[] = [
  { trigger: 'tagSystem.rulePhoneTrigger', tag: 'tagSystem.rulePhoneTag', category: 'tagSystem.catStatus' },
  { trigger: 'tagSystem.ruleDriveTrigger', tag: 'tagSystem.ruleDriveTag', category: 'tagSystem.catIntent' },
  { trigger: 'tagSystem.rulePriceTrigger', tag: 'tagSystem.rulePriceTag', category: 'tagSystem.catIntent' },
  { trigger: 'tagSystem.ruleVisitTrigger', tag: 'tagSystem.ruleVisitTag', category: 'tagSystem.catStatus' },
]

// 以下三组只服务 defaultRowToForm（该函数只读 key/type，不读 label），
// 因此不携带任何可渲染文案；真正渲染表单的 filters/columns/createFields 全部用 t()。
const TAG_ALL_FIELDS: FieldSpec[] = [
  { key: 'name' },
  { key: 'code' },
  { key: 'category' },
  { key: 'weight', type: 'number' },
  { key: 'description' },
  { key: 'status', type: 'number' },
]
const TAG_RULE_ALL_FIELDS: FieldSpec[] = [
  { key: 'name' },
  { key: 'rule_type' },
  { key: 'match_pattern', type: 'list' },
  { key: 'target_tag_id' },
  { key: 'weight_bonus', type: 'number' },
  { key: 'status', type: 'number' },
]
const TAG_WEIGHT_ALL_FIELDS: FieldSpec[] = [
  { key: 'tag_id' },
  { key: 'tag_code' },
  { key: 't_vector_index', type: 'number' },
  { key: 'weight_delta', type: 'number' },
  { key: 'direction' },
  { key: 'status', type: 'number' },
]

/** 标签定义维护 Tab。 */
function TagTab({ categoryOptions }: { categoryOptions: Option[] }) {
  const { t } = useI18n()
  const statusOptions: Option[] = STATUS_KEYS.map((x) => ({ label: t(x.key), value: x.value }))
  return <EntityCrud
    base="/api/v1/admin/tags"
    title={t('common.tags')}
    filters={[
      { key: 'keyword', label: t('tagSystem.tagFilterKw') },
      { key: 'category', label: t('common.category'), options: categoryOptions },
      { key: 'status', label: t('common.status'), options: statusOptions },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'name', title: t('common.name'), width: 130 },
      { colKey: 'code', title: t('common.code'), width: 140 },
      { colKey: 'category', title: t('common.category'), width: 120, cell: (p: CellProps) => <span className="text-xs text-gray-500">{p.row.category || '-'}</span> },
      { colKey: 'weight', title: t('tagSystem.weight'), width: 90 },
      { colKey: 'description', title: t('common.description'), ellipsis: true },
      { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <StatusTag row={p.row} /> },
    ]}
    createFields={[
      { key: 'name', label: t('tagSystem.tagName'), required: true },
      { key: 'code', label: t('tagSystem.tagCode'), required: true, placeholder: t('tagSystem.tagCodePh') },
      { key: 'category', label: t('common.category'), type: 'select', options: categoryOptions, defaultValue: 'intent' },
      { key: 'weight', label: t('tagSystem.weight'), type: 'number', defaultValue: 1 },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
      { key: 'description', label: t('common.description'), type: 'textarea' },
    ]}
    editFields={[
      { key: 'name', label: t('tagSystem.tagName'), required: true },
      { key: 'category', label: t('common.category'), type: 'select', options: categoryOptions },
      { key: 'weight', label: t('tagSystem.weight'), type: 'number' },
      { key: 'description', label: t('common.description'), type: 'textarea' },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_ALL_FIELDS, row)}
  />
}

/** 标签规则维护 Tab。 */
function TagRuleTab({ tagOptions }: { tagOptions: Option[] }) {
  const { t } = useI18n()
  const ruleTypeOptions: Option[] = Object.entries(RULE_TYPE_KEYS).map(([value, key]) => ({ label: t(key), value }))
  const statusOptions: Option[] = STATUS_KEYS.map((x) => ({ label: t(x.key), value: x.value }))
  return <EntityCrud
    base="/api/v1/admin/tag-rules"
    title={t('tagSystem.tabRules')}
    filters={[
      { key: 'keyword', label: t('tagSystem.ruleFilterKw') },
      { key: 'rule_type', label: t('common.type'), options: ruleTypeOptions },
      { key: 'status', label: t('common.status'), options: statusOptions },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'name', title: t('tagSystem.ruleName'), width: 160 },
      { colKey: 'rule_type', title: t('common.type'), width: 100, cell: (p: CellProps) => {
        const rk = RULE_TYPE_KEYS[String(p.row.rule_type)]
        return <Tag theme="primary">{rk ? t(rk) : p.row.rule_type}</Tag>
      } },
      { colKey: 'match_pattern', title: t('tagSystem.matchPattern'), ellipsis: true, cell: (p: CellProps) => <span className="text-xs text-gray-500">{parseJsonArray(p.row.match_pattern).join('、') || '-'}</span> },
      { colKey: 'target_tag_name', title: t('tagSystem.targetTag'), width: 130 },
      { colKey: 'weight_bonus', title: t('tagSystem.bonus'), width: 90 },
      { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <StatusTag row={p.row} /> },
    ]}
    createFields={[
      { key: 'name', label: t('tagSystem.ruleName'), required: true },
      { key: 'rule_type', label: t('tagSystem.ruleType'), type: 'select', required: true, options: ruleTypeOptions, defaultValue: 'keyword' },
      { key: 'match_pattern', label: t('tagSystem.matchPatternShort'), type: 'list', required: true, placeholder: t('tagSystem.matchPatternPh') },
      { key: 'target_tag_id', label: t('tagSystem.targetTag'), type: 'select', required: true, options: tagOptions },
      { key: 'weight_bonus', label: t('tagSystem.weightBonus'), type: 'number', defaultValue: 1 },
      { key: 'status', label: t('common.status'), type: 'number', defaultValue: 1 },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_RULE_ALL_FIELDS, row)}
  />
}

/** 标签权重映射维护 Tab。 */
function TagWeightTab({ tagOptions }: { tagOptions: Option[] }) {
  const { t } = useI18n()
  const tVectorOptions: Option[] = T_VECTOR_KEYS.map((x) => ({ label: t(x.key), value: x.value }))
  const directionOptions: Option[] = Object.entries(DIRECTION_KEYS).map(([value, key]) => ({ label: t(key), value }))
  return <EntityCrud
    base="/api/v1/admin/tag-weights"
    title={t('tagSystem.tabWeights')}
    filters={[
      { key: 'tag_id', label: t('tagSystem.tag'), options: tagOptions },
      { key: 't_vector_index', label: t('tagSystem.tVectorIndex'), options: tVectorOptions },
    ]}
    columns={[
      { colKey: 'id', title: t('common.id'), width: 70 },
      { colKey: 'tag_id', title: t('tagSystem.tagId'), width: 90 },
      { colKey: 'tag_code', title: t('tagSystem.tagCode'), width: 150 },
      { colKey: 't_vector_index', title: t('tagSystem.tDim'), width: 90 },
      { colKey: 'weight_delta', title: t('tagSystem.delta'), width: 90 },
      { colKey: 'direction', title: t('tagSystem.direction'), width: 90, cell: (p: CellProps) => <Tag theme={p.row.direction === 'down' ? 'warning' : 'primary'}>{p.row.direction === 'down' ? t('tagSystem.dirDown') : t('tagSystem.dirUp')}</Tag> },
      { colKey: 'status', title: t('common.status'), width: 90, cell: (p: CellProps) => <StatusTag row={p.row} /> },
    ]}
    createFields={[
      { key: 'tag_id', label: t('tagSystem.tag'), type: 'select', required: true, options: tagOptions },
      { key: 't_vector_index', label: t('tagSystem.tVectorIndex'), type: 'select', required: true, options: tVectorOptions, defaultValue: 0 },
      { key: 'weight_delta', label: t('tagSystem.weightDelta'), type: 'number', required: true, defaultValue: 0.1 },
      { key: 'direction', label: t('tagSystem.direction'), type: 'select', options: directionOptions, defaultValue: 'up' },
    ]}
    editFields={[
      { key: 'tag_id', label: t('tagSystem.tag'), type: 'select', options: tagOptions },
      { key: 't_vector_index', label: t('tagSystem.tVectorIndex'), type: 'select', options: tVectorOptions },
      { key: 'weight_delta', label: t('tagSystem.weightDelta'), type: 'number' },
      { key: 'direction', label: t('tagSystem.direction'), type: 'select', options: directionOptions },
    ]}
    rowToForm={(row) => defaultRowToForm(TAG_WEIGHT_ALL_FIELDS, row)}
  />
}

/** 标签体系 Tab：维护标签、规则、权重与 TTL。 */
export function TagSystemTab() {
  const { t } = useI18n()
  const [tab, setTab] = useState('tags')
  const [tagOptions, setTagOptions] = useState<Option[]>([])
  // 语言切换时 t 的引用会变（I18nProvider 按 lang 记忆化），下拉随之重算
  const categoryOptions = useMemo<Option[]>(
    () => Object.entries(TAG_CAT_KEYS).map(([value, key]) => ({ label: t(key), value })),
    [t],
  )
  const loadTags = useCallback(async () => {
    // FIX-4(2026-09-27)：下拉取数改走标签字典模式（?all=1&status=1，上限 1000）。
    // 旧写法 `page_size=500` 会被服务端硬顶 100 静默截断，标签多于 100 个时
    // 多出来的那些在"打标规则/权重映射"里再也选不到，而且不报错。
    // status=1 的筛选同时挪到服务端（单一判据），前端不再自己 filter。
    const j = await AUTH('/api/v1/admin/tags?all=1&status=1')
    if (j?.code === 0) {
      setTagOptions((j.data?.list || []).map((x: CrudRow) => ({ label: `${x.name}(${x.code})`, value: x.id })))
    }
  }, [])
  useEffect(() => {
    if (['rules', 'weights'].includes(tab) && tagOptions.length === 0) void loadTags()
  }, [loadTags, tab, tagOptions.length])

  const reloadCache = async () => {
    const j = await AUTH('/api/v1/admin/tags/reload', { method: 'POST' })
    if (j?.code === 0) MessagePlugin.success(t('tagSystem.reloadOk'))
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h3 className="text-base font-bold text-gray-800">{t('tagSystem.title')}</h3>
          <p className="text-xs text-gray-400 mt-0.5">{t('tagSystem.desc')}</p>
        </div>
        <Button variant="outline" onClick={reloadCache}>{t('tagSystem.reloadCache')}</Button>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-4">
        <Tabs value={tab} onChange={(v) => setTab(String(v))} placement="top">
          <Tabs.TabPanel value="tags" label={t('tagSystem.tabTags')}><TagTab categoryOptions={categoryOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="rules" label={t('tagSystem.tabRules')}><TagRuleTab tagOptions={tagOptions} /></Tabs.TabPanel>
          <Tabs.TabPanel value="weights" label={t('tagSystem.tabWeights')}><TagWeightTab tagOptions={tagOptions} /></Tabs.TabPanel>
        </Tabs>
      </div>

      <div className="bg-white rounded-xl border border-gray-200 p-6">
        <div className="mb-4">
          <h3 className="text-base font-bold text-gray-800">{t('tagSystem.autoTitle')}</h3>
          <p className="text-xs text-gray-400 mt-0.5">{t('tagSystem.autoDesc')}</p>
        </div>
        <table className="w-full text-sm">
          <thead><tr className="border-b border-gray-200 text-left text-xs font-semibold text-gray-500"><th className="py-2 px-3 w-8">#</th><th className="py-2 px-3">{t('tagSystem.colTrigger')}</th><th className="py-2 px-3">{t('tagSystem.colAutoTag')}</th><th className="py-2 px-3">{t('tagSystem.colCategory')}</th></tr></thead>
          <tbody>
            {AUTO_TAG_RULE_KEYS.map((r, i) => (
              <tr key={i} className="border-b border-gray-50"><td className="py-2.5 px-3 text-xs text-gray-400">{i + 1}</td><td className="py-2.5 px-3 text-gray-700">{t(r.trigger)}</td><td className="py-2.5 px-3"><span className="inline-block text-xs px-2 py-0.5 rounded-full bg-indigo-50 text-indigo-600">{t(r.tag)}</span></td><td className="py-2.5 px-3 text-xs text-gray-500">{t(r.category)}</td></tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
