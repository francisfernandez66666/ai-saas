// F6 行业包/租户绑定管理：查看当前绑定、两级行业/企业包绑定、部门包绑定。
// G-22c(2026-09-24)：可选包列表带档位标注——不够档的包照常在列表里，但置灰并写明
// 需要升到哪一档。判据完全由后端下发（tier_locked/tier_reason），前端不比较档位，
// 因此不会出现"界面说可绑、接口回 403"的错位。
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, MessagePlugin, Select, Table, Tag } from 'tdesign-react'
import { AUTH } from '../../lib/api'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
// P2-15(2026-09-20 批三)：原生弹窗 12 处统一替换为 MessagePlugin/confirmDialog，
// 与管理台其余破坏性操作确认口径一致（原生弹窗样式割裂且阻塞主线程）
import { confirmDialog } from '../../lib/confirm'
import type { CellProps, TableRowData } from '../../types'

// Pack 行业包列表行：包的层级/版本/状态与档位门槛字段（档位不够时下拉置灰并提示升级）。
type Pack = {
  id: number
  code: string
  name: string
  industry: string
  version: string
  pack_level: string
  parent_code: string
  status: string
  file_size?: number
  updated_at?: string
  /** 最低可绑档位（personal/enterprise/custom），空串=不设门槛 */
  min_tier?: string
  /** 当前租户档位不够：下拉里置灰并提示升级 */
  tier_locked?: boolean
  /** 稳定原因码：pack_tier_required / pack_min_tier_invalid / tenant_tier_unknown */
  tier_reason?: string
}

/** Select 下拉项：档位不够时 disabled=true，值仍在（用户看得见这个包存在）。 */
type PackOption = { label: string; value: number; disabled?: boolean }

// DeptBinding 部门包绑定行：部门当前绑定的包及其已生效版本。
type DeptBinding = {
  department_id: number
  pack_id: number
  pack_code: string
  applied_version: string
}

// Current 当前租户的绑定总览：行业包/企业包两级绑定加各部门包绑定列表。
type Current = {
  bound: boolean
  industry?: Pack | null
  enterprise?: Pack | null
  departments?: DeptBinding[]
}

// DeptNode 部门树节点（/org/departments/tree）：部门包绑定选择时按层级展开。
type DeptNode = {
  id: number
  name: string
  children?: DeptNode[]
}

// PackTemplateStat 单个模板的包效果统计（/admin/packs/stats 下发）：样本量与钩子/留资/评分等比率。
type PackTemplateStat = {
  template_id: string
  pack_code: string
  pack_version: string
  sample_count: number
  hook_rate: number
  lead_rate: number
  pending_human_rate?: number
  avg_intent_delta?: number
  avg_eval_score?: number | null
}

// PackEffectRow：包效果行类型。原先定义于此但全文件无人引用，
// 已于 2026-10-13 P2-10 收口删除；若后续启用"效果对比表格"，此处按需重新定义。

// 判优层行动建议卡片（批五 C·L1，只读展示）：与后端 strategy.TemplateSuggestion 对齐
type PackSuggestionView = {
  pack_code: string
  pack_version: string
  template_id: string
  anchor_type: number
  status: string
  sample_count: number
  reward_rate: number
  anchor_median_rate: number
  metric: string
  min_samples: number
  confidence: number
  reason: string
}

// P2-14：建议状态 → 徽标 I18nKey 与 TDesign Tag 主题（走语义主题色，不硬编码色值）。
// status 是后端契约值（不翻译），只有 label 走词典，渲染时翻。
const SUGGESTION_UI: Record<string, { key: I18nKey; theme: 'danger' | 'success' | 'warning' | 'default' }> = {
  suggest_review: { key: 'industryPack.sugReview', theme: 'danger' },
  leading: { key: 'industryPack.sugLeading', theme: 'success' },
  keep_watching: { key: 'industryPack.sugWatch', theme: 'default' },
  insufficient_samples: { key: 'industryPack.sugInsufficient', theme: 'warning' },
  no_peer: { key: 'industryPack.sugNoPeer', theme: 'default' },
}

/** 把部门树扁平化为级联选择器可用的 options（无名部门的占位文案由调用方注入）。 */
function flattenDepts(nodes: DeptNode[], fallback: (id: number) => string): { label: string; value: number }[] {
  const out: { label: string; value: number }[] = []
  const walk = (list: DeptNode[]) => {
    list.forEach((n) => {
      out.push({ label: n.name || fallback(Number(n.id)), value: Number(n.id) })
      if (n.children?.length) walk(n.children)
    })
  }
  walk(nodes || [])
  return out
}

/** 生成行业包展示标签，包含包名、版本和层级。 */
function packLabel(p: Pack) {
  return `${p.name} ${p.version}（${p.code}）`
}

// P2-14：档位名（与 tenants.tier / industry_packs.min_tier 同一词表，值不翻译）
// 与后端稳定原因码 → 界面话术。前端不自己猜语义：码是契约，文案随时可改。
const TIER_NAME_KEYS: Record<string, I18nKey> = {
  personal: 'industryPack.tierPersonal', enterprise: 'industryPack.tierEnterprise', custom: 'industryPack.tierCustom',
}
const TIER_DENY_KEYS: Record<string, I18nKey> = {
  pack_tier_required: 'industryPack.denyTierRequired',
  pack_min_tier_invalid: 'industryPack.denyTierInvalid',
  tenant_tier_unknown: 'industryPack.denyTierUnknown',
}
// 包层级展示名（industry/enterprise/department 为后端值）
const LEVEL_KEYS: Record<string, I18nKey> = {
  industry: 'industryPack.levelIndustry',
  enterprise: 'industryPack.levelEnterprise',
  department: 'industryPack.levelDepartment',
}

/**
 * 生成下拉项。档位不够的包**照常列出但置灰**（后端刻意只标注不隐藏），
 * 标签里直接说清要升到哪一档——从下拉里悄悄消失只会让人以为产品没这个能力。
 */
function packOption(p: Pack, t: (k: I18nKey, v?: Record<string, string | number>) => string): PackOption {
  if (!p.tier_locked) return { label: packLabel(p), value: p.id }
  const tierKey = TIER_NAME_KEYS[p.min_tier || '']
  const need = tierKey ? t(tierKey) : t('industryPack.tierHigher')
  const denyKey = TIER_DENY_KEYS[p.tier_reason || ''] || TIER_DENY_KEYS.pack_tier_required
  const text = t(denyKey)
  return { label: `${packLabel(p)}（${t('industryPack.tierExclusive', { tier: need })}，${text}）`, value: p.id, disabled: true }
}

/** 被档位拦住的包数，用于在绑定卡片下方给一句升级引导。 */
function lockedCount(packs: Pack[]) {
  return packs.filter((p) => p.tier_locked).length
}

/** 把行业包发布状态转换为本地化标签（level 是后端契约值，未登记时原样回落）。 */
function statusLabel(level: string, t: (k: I18nKey) => string) {
  const k = LEVEL_KEYS[level]
  return k ? t(k) : (level || t('industryPack.levelPack'))
}

/** 把比例值格式化为百分比字符串。 */
function formatPercent(v: number) {
  return `${(Number(v || 0) * 100).toFixed(1)}%`
}

/** 拆分语义化版本号数字段。 */
function parseVersionParts(v: string): number[] {
  return String(v || '').split('.').map((x) => {
    const n = Number(x)
    return Number.isFinite(n) ? n : 0
  })
}

/** 比较两个行业包版本号大小。 */
function compareVersion(a: string, b: string): number {
  const pa = parseVersionParts(a)
  const pb = parseVersionParts(b)
  const len = Math.max(pa.length, pb.length)
  for (let i = 0; i < len; i++) {
    const x = pa[i] || 0
    const y = pb[i] || 0
    if (x !== y) return x - y
  }
  return String(a || '').localeCompare(String(b || ''))
}

// IndustryPackTab 后台「行业包」管理页：行业包列表、内容项维护与上下架。
export default function IndustryPackTab() {
  const { t } = useI18n()
  const [loading, setLoading] = useState(false)
  const [saving, setSaving] = useState(false)
  const [current, setCurrent] = useState<Current | null>(null)
  const [industryPacks, setIndustryPacks] = useState<Pack[]>([])
  const [enterprisePacks, setEnterprisePacks] = useState<Pack[]>([])
  const [departmentPacks, setDepartmentPacks] = useState<Pack[]>([])
  const [depts, setDepts] = useState<{ label: string; value: number }[]>([])
  const [packStats, setPackStats] = useState<PackTemplateStat[]>([])
  const [packSuggestions, setPackSuggestions] = useState<PackSuggestionView[]>([])
  const [packCode, setPackCode] = useState('')
  const [packVersion, setPackVersion] = useState('')
  const [industryPackId, setIndustryPackId] = useState<number | ''>('')
  const [enterprisePackId, setEnterprisePackId] = useState<number | ''>('')
  const [deptId, setDeptId] = useState<number | ''>('')
  const [deptPackId, setDeptPackId] = useState<number | ''>('')

  const loadCurrent = useCallback(async () => {
    const j = await AUTH('/api/v1/admin/packs/current')
    if (j?.code === 0) {
      const data = j.data || { bound: false }
      setCurrent(data)
      if (data.industry?.id) setIndustryPackId(Number(data.industry.id))
    }
  }, [])

  const loadIndustries = useCallback(async () => {
    const j = await AUTH('/api/v1/admin/packs?level=industry')
    if (j?.code === 0) setIndustryPacks(j.data || [])
  }, [])

  const loadEnterprises = useCallback(async (industryCode: string) => {
    if (!industryCode) { setEnterprisePacks([]); return }
    const j = await AUTH(`/api/v1/admin/packs?level=enterprise&parent_code=${encodeURIComponent(industryCode)}`)
    if (j?.code === 0) setEnterprisePacks(j.data || [])
  }, [])

  const loadDeptPacks = useCallback(async (enterpriseCode: string) => {
    if (!enterpriseCode) { setDepartmentPacks([]); return }
    const j = await AUTH(`/api/v1/admin/packs?level=department&parent_code=${encodeURIComponent(enterpriseCode)}`)
    if (j?.code === 0) setDepartmentPacks(j.data || [])
  }, [])

  const loadDepts = useCallback(async () => {
    const j = await AUTH('/api/v1/org/departments/tree')
    if (j?.code === 0) setDepts(flattenDepts(j.data || [], (id) => t('industryPack.deptHash', { id })))
  }, [t])

  const loadAll = useCallback(async () => {
    setLoading(true)
    await Promise.all([loadCurrent(), loadIndustries(), loadDepts()])
    setLoading(false)
  }, [loadCurrent, loadDepts, loadIndustries])

  useEffect(() => { void loadAll() }, [loadAll])

  useEffect(() => {
    const selected = industryPacks.find((p) => p.id === Number(industryPackId))
    void loadEnterprises(selected?.code || '')
    if (!current?.enterprise || selected?.code !== current?.industry?.code) setEnterprisePackId('')
  }, [current, industryPackId, industryPacks, loadEnterprises])

  useEffect(() => {
    void loadDeptPacks(current?.enterprise?.code || '')
  }, [current, loadDeptPacks])

  useEffect(() => {
    const code = current?.enterprise?.code || current?.industry?.code || ''
    const version = current?.enterprise?.version || current?.industry?.version || ''
    setPackCode(code)
    setPackVersion(version)
    if (!code) {
      setPackStats([])
      setPackSuggestions([])
      return
    }
    ;(async () => {
      const q = new URLSearchParams({ days: '90', pack_code: code })
      const j = await AUTH(`/api/v1/admin/packs/stats?${q.toString()}`)
      if (j?.code === 0) {
        setPackStats((j.data?.list || []) as PackTemplateStat[])
        setPackSuggestions((j.data?.suggestions || []) as PackSuggestionView[])
      }
    })()
  }, [current])

  const selectedIndustry = useMemo(() => industryPacks.find((p) => p.id === Number(industryPackId)), [industryPackId, industryPacks])
  // 原先此处还有一个 selectedEnterprise 派生变量，但 enterprisePackId 本体在提交体
  // 与下拉框处直接使用、派生值无人读取，属纯冗余计算，已删（2026-10-13 P2-10 收口）。

  // 被档位拦住的包总数：企业包/部门包是选中行业后才加载的，所以三级一起数。
  const lockedSummary = useMemo(
    () => lockedCount(industryPacks) + lockedCount(enterprisePacks) + lockedCount(departmentPacks),
    [departmentPacks, enterprisePacks, industryPacks],
  )

  const currentRows = useMemo(() => packStats.filter((r) => !packVersion || r.pack_version === packVersion), [packStats, packVersion])
  const historyRows = useMemo(() => packStats.filter((r) => packVersion ? r.pack_version !== packVersion : true), [packStats, packVersion])
  const historyVersion = useMemo(() => {
    const versions = Array.from(new Set(historyRows.map((r) => r.pack_version).filter(Boolean)))
    return versions.sort(compareVersion).reverse()[0] || ''
  }, [historyRows])
  const displayRows = useMemo(() => {
    const rows = (currentRows.length ? currentRows : packStats).map((r) => {
      const history = historyVersion ? historyRows.find((h) => h.template_id === r.template_id && h.pack_version === historyVersion) : undefined
      return {
        ...r,
        history_version: history?.pack_version || '',
        history_sample_count: history?.sample_count || 0,
        history_hook_rate: history?.hook_rate,
        history_lead_rate: history?.lead_rate,
        hook_delta: history ? Number(r.hook_rate || 0) - Number(history.hook_rate || 0) : undefined,
        lead_delta: history ? Number(r.lead_rate || 0) - Number(history.lead_rate || 0) : undefined,
      }
    })
    return rows.sort((a, b) => Number(b.sample_count || 0) - Number(a.sample_count || 0) || String(a.template_id || '').localeCompare(String(b.template_id || '')))
  }, [currentRows, historyRows, historyVersion, packStats])

  const packSummary = useMemo(() => {
    const total = displayRows.reduce((s, r) => s + Number(r.sample_count || 0), 0)
    if (!total) return { total: 0, hookRate: 0, leadRate: 0, pendingRate: 0, score: 0 }
    const scored = displayRows.filter((r) => typeof r.avg_eval_score === 'number')
    const scoreTotal = scored.reduce((s, r) => s + Number(r.sample_count || 0), 0)
    return {
      total,
      hookRate: displayRows.reduce((s, r) => s + Number(r.hook_rate || 0) * Number(r.sample_count || 0), 0) / total,
      leadRate: displayRows.reduce((s, r) => s + Number(r.lead_rate || 0) * Number(r.sample_count || 0), 0) / total,
      pendingRate: displayRows.reduce((s, r) => s + Number(r.pending_human_rate || 0) * Number(r.sample_count || 0), 0) / total,
      score: scoreTotal ? scored.reduce((s, r) => s + Number(r.avg_eval_score || 0) * Number(r.sample_count || 0), 0) / scoreTotal : 0,
    }
  }, [displayRows])

  const bind = async () => {
    if (!industryPackId) { MessagePlugin.warning(t('industryPack.selectIndustryWarn')); return }
    setSaving(true)
    // P2-10：两个字段均为数字 ID，从 Record<string, any> 收敛
    const body: Record<string, number> = { industry_pack_id: Number(industryPackId) }
    if (enterprisePackId) body.enterprise_pack_id = Number(enterprisePackId)
    const j = await AUTH('/api/v1/admin/packs/bind', { method: 'POST', body })
    setSaving(false)
    if (j?.code === 0) {
      await Promise.all([loadCurrent(), loadIndustries(), loadDepts()])
      MessagePlugin.success(j.message || t('industryPack.boundOk'))
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const unbind = async () => {
    if (!(await confirmDialog(t('industryPack.unbindConfirmMsg'), t('industryPack.unbindConfirmTitle')))) return
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/unbind', { method: 'POST' })
    setSaving(false)
    if (j?.code === 0) {
      setEnterprisePackId('')
      setDeptPackId('')
      await loadAll()
      MessagePlugin.success(j.message || t('industryPack.unboundOk'))
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const bindDept = async () => {
    if (!deptId || !deptPackId) { MessagePlugin.warning(t('industryPack.selectDeptWarn')); return }
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/bind-dept', {
      method: 'POST',
      body: { department_id: Number(deptId), pack_id: Number(deptPackId) },
    })
    setSaving(false)
    if (j?.code === 0) {
      setDeptPackId('')
      await loadCurrent()
      MessagePlugin.success(j.message || t('industryPack.deptBoundOk'))
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  const unbindDept = async (departmentId: number) => {
    if (!(await confirmDialog(t('industryPack.unbindDeptConfirmMsg'), t('industryPack.unbindDeptConfirmTitle')))) return
    setSaving(true)
    const j = await AUTH('/api/v1/admin/packs/unbind-dept', {
      method: 'POST',
      body: { department_id: departmentId },
    })
    setSaving(false)
    if (j?.code === 0) {
      await loadCurrent()
      MessagePlugin.success(j.message || t('industryPack.unboundOk'))
    }        // 失败提示由 AUTH toastError 统一处理（P2-15/P1-7：防双弹）
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">{t('industryPack.title')}</h3>
            <p className="text-xs text-gray-400 mt-0.5">{t('industryPack.desc')}</p>
          </div>
          <div className="flex gap-2">
            <Button theme="default" variant="outline" loading={loading} disabled={loading} onClick={() => void loadAll()}>{t('crud.refresh')}</Button>
            <Button theme="primary" loading={saving} disabled={saving || !industryPackId} onClick={() => void bind()}>{t('industryPack.bind')}</Button>
            <Button theme="danger" variant="outline" loading={saving} disabled={saving || !current?.bound} onClick={() => void unbind()}>{t('industryPack.unbind')}</Button>
          </div>
        </div>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-gray-500 mb-1">{t('industryPack.industryLabel')}</div>
            <Select
              value={industryPackId}
              onChange={(v) => setIndustryPackId(v as number)}
              options={industryPacks.map((p) => packOption(p, t))}
              filterable
              placeholder={t('industryPack.selectIndustry')}
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">{t('industryPack.enterpriseLabel')}</div>
            <Select
              value={enterprisePackId}
              onChange={(v) => setEnterprisePackId(v as number)}
              options={[{ label: t('industryPack.noEnterprise'), value: '' }, ...enterprisePacks.map((p) => packOption(p, t))]}
              filterable
              disabled={!selectedIndustry}
              placeholder={t('industryPack.selectEnterprise')}
              style={{ width: '100%' }}
            />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">{t('industryPack.currentBind')}</div>
            <div className="border border-gray-200 rounded-md p-3 min-h-[58px] bg-gray-50 text-sm">
              {current?.bound ? (
                <div>
                  <div><span className="text-gray-500">{t('industryPack.industryField')}</span>{current.industry?.name || '-'} <Tag theme="primary">{current.industry?.version}</Tag></div>
                  <div><span className="text-gray-500">{t('industryPack.enterpriseField')}</span>{current.enterprise?.name || t('industryPack.notBound')} {current.enterprise ? <Tag theme="success">{current.enterprise.version}</Tag> : null}</div>
                </div>
              ) : <span className="text-gray-400">{t('industryPack.notBound')}</span>}
            </div>
          </div>
        </div>
        {lockedSummary > 0 && (
          <p className="text-xs text-gray-400 mt-3">
            {t('industryPack.lockedHint', { n: lockedSummary })}
          </p>
        )}
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">{t('industryPack.deptTitle')}</h3>
        <p className="text-xs text-gray-400 mt-0.5">{t('industryPack.deptDesc')}</p>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div>
            <div className="text-xs text-gray-500 mb-1">{t('industryPack.dept')}</div>
            <Select value={deptId} onChange={(v) => setDeptId(v as number)} options={depts} filterable placeholder={t('industryPack.selectDept')} style={{ width: '100%' }} />
          </div>
          <div>
            <div className="text-xs text-gray-500 mb-1">{t('industryPack.deptPack')}</div>
            <Select
              value={deptPackId}
              onChange={(v) => setDeptPackId(v as number)}
              options={departmentPacks.map((p) => packOption(p, t))}
              filterable
              disabled={!current?.enterprise}
              placeholder={t('industryPack.selectDeptPack')}
              style={{ width: '100%' }}
            />
          </div>
          <div className="flex items-end">
            <Button theme="primary" loading={saving} disabled={saving || !current?.enterprise || !deptId || !deptPackId} onClick={() => void bindDept()}>{t('industryPack.bindDept')}</Button>
          </div>
        </div>
        <div className="mt-4">
          <Table
            rowKey="department_id"
            loading={loading}
            data={(current?.departments || []) as TableRowData[]}
            size="small"
            empty={t('industryPack.deptEmpty')}
            columns={[
              { colKey: 'department_id', title: t('industryPack.deptIdCol'), width: 120 },
              { colKey: 'dept_name', title: t('industryPack.dept'), width: 180, cell: (p: CellProps) => depts.find((d) => d.value === Number(p.row.department_id))?.label || t('industryPack.deptHash', { id: Number(p.row.department_id) }) },
              { colKey: 'pack_code', title: t('industryPack.packCodeCol'), width: 180 },
              { colKey: 'applied_version', title: t('industryPack.versionCol'), width: 120, cell: (p: CellProps) => <Tag>{p.row.applied_version}</Tag> },
              { colKey: 'pack_level', title: t('industryPack.typeCol'), width: 100, cell: () => <Tag theme="success">{statusLabel('department', t)}</Tag> },
              { colKey: 'actions', title: t('crud.actions'), width: 120, fixed: 'right', cell: (p: CellProps) => <Button size="small" variant="text" theme="danger" onClick={() => void unbindDept(Number(p.row.department_id))}>{t('industryPack.unbindDept')}</Button> },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div>
            <h3 className="text-base font-bold text-gray-800">{t('industryPack.attribution')}</h3>
            <p className="text-xs text-gray-400 mt-0.5">{t('industryPack.attributionDesc')}</p>
          </div>
          {packCode ? <div className="text-xs text-gray-500">{packCode}{packVersion ? ` · ${packVersion}` : ''}</div> : null}
        </div>
        {packSummary.total > 0 && (
          <div className="mt-4 grid grid-cols-2 md:grid-cols-4 gap-3">
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">{t('industryPack.samples')}</div><div className="text-lg font-bold text-gray-800">{packSummary.total}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">{t('industryPack.hookRate')}</div><div className="text-lg font-bold text-blue-600">{formatPercent(packSummary.hookRate)}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">{t('industryPack.leadRate')}</div><div className="text-lg font-bold text-emerald-600">{formatPercent(packSummary.leadRate)}</div></div>
            <div className="rounded-md border border-gray-200 p-3"><div className="text-xs text-gray-500">{t('industryPack.pendingRate')}</div><div className="text-lg font-bold text-amber-600">{formatPercent(packSummary.pendingRate)}</div></div>
          </div>
        )}
        {packSuggestions.length > 0 && (
          <div className="mt-4 rounded-md border border-gray-200 p-3">
            <div className="text-xs font-bold text-gray-700 mb-2">{t('industryPack.suggestionTitle')}</div>
            <div className="space-y-1.5">
              {packSuggestions.slice(0, 8).map((s) => {
                const ui = SUGGESTION_UI[s.status]
                const uiLabel = ui ? t(ui.key) : s.status
                return (
                  <div key={`${s.pack_code}-${s.pack_version}-${s.template_id}`} className="flex flex-wrap items-center gap-2 text-xs">
                    <Tag theme={ui?.theme || 'default'}>{uiLabel}</Tag>
                    <span className="text-gray-700 font-medium">{s.template_id}</span>
                    <span className="text-gray-500">{t('industryPack.sugLine', { anchor: s.anchor_type, metric: s.metric, rate: formatPercent(s.reward_rate), median: formatPercent(s.anchor_median_rate), n: s.sample_count })}</span>
                    <span className="text-gray-400">{s.reason}</span>
                  </div>
                )
              })}
            </div>
          </div>
        )}
        <div className="mt-4 overflow-x-auto">
          <Table
            rowKey="template_id"
            data={displayRows as TableRowData[]}
            size="small"
            empty={packCode ? t('industryPack.attrEmptyNoSample') : t('industryPack.attrEmptyNoPack')}
            pagination={{ defaultPageSize: 10 }}
            columns={[
              { colKey: 'template_id', title: t('industryPack.templateCol'), width: 220 },
              { colKey: 'sample_count', title: t('industryPack.samples'), width: 100, cell: (p: CellProps) => Number(p.row.sample_count || 0) < 50 ? <Tag theme="warning">{t('industryPack.accumulating', { n: Number(p.row.sample_count || 0) })}</Tag> : p.row.sample_count },
              { colKey: 'hook_rate', title: t('industryPack.hookRate'), width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.hook_rate || 0)) },
              { colKey: 'lead_rate', title: t('industryPack.leadRate'), width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.lead_rate || 0)) },
              { colKey: 'pending_human_rate', title: t('industryPack.pendingHumanCol'), width: 110, cell: (p: CellProps) => formatPercent(Number(p.row.pending_human_rate || 0)) },
              { colKey: 'avg_intent_delta', title: t('industryPack.intentDeltaCol'), width: 120, cell: (p: CellProps) => `${(Number(p.row.avg_intent_delta || 0) * 100).toFixed(1)}%` },
              { colKey: 'avg_eval_score', title: t('industryPack.scoreCol'), width: 100, cell: (p: CellProps) => typeof p.row.avg_eval_score === 'number' ? p.row.avg_eval_score.toFixed(0) : '-' },
              {
                colKey: 'history_compare',
                title: historyVersion ? t('industryPack.historyCol', { version: historyVersion }) : t('industryPack.historyColNoVer'),
                width: 190,
                cell: (p: CellProps) => {
                  if (!p.row.history_version) return <span className="text-xs text-gray-400">-</span>
                  return (
                    <div className="text-xs leading-5">
                      <div>{t('industryPack.historyHook', { rate: formatPercent(Number(p.row.history_hook_rate || 0)), delta: `${Number(p.row.hook_delta || 0) >= 0 ? '+' : ''}${(Number(p.row.hook_delta || 0) * 100).toFixed(1)}` })}</div>
                      <div>{t('industryPack.historyLead', { rate: formatPercent(Number(p.row.history_lead_rate || 0)), delta: `${Number(p.row.lead_delta || 0) >= 0 ? '+' : ''}${(Number(p.row.lead_delta || 0) * 100).toFixed(1)}` })}</div>
                    </div>
                  )
                },
              },
            ]}
          />
        </div>
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">{t('industryPack.catalog')}</h3>
        <div className="mt-3 overflow-x-auto">
          <Table
            rowKey="id"
            loading={loading}
            data={[...industryPacks, ...enterprisePacks, ...departmentPacks] as TableRowData[]}
            size="small"
            empty={t('industryPack.catalogEmpty')}
            pagination={{ defaultPageSize: 10 }}
            columns={[
              { colKey: 'code', title: t('industryPack.codeCol'), width: 150 },
              { colKey: 'name', title: t('industryPack.nameCol'), width: 220 },
              { colKey: 'pack_level', title: t('industryPack.levelCol'), width: 120, cell: (p: CellProps) => <Tag theme={p.row.pack_level === 'industry' ? 'primary' : p.row.pack_level === 'enterprise' ? 'success' : 'default'}>{statusLabel(String(p.row.pack_level), t)}</Tag> },
              { colKey: 'parent_code', title: t('industryPack.parentCol'), width: 140 },
              { colKey: 'version', title: t('industryPack.versionCol'), width: 120 },
              { colKey: 'industry', title: t('industryPack.industryCol'), width: 120 },
              { colKey: 'status', title: t('industryPack.statusCol'), width: 100, cell: (p: CellProps) => <Tag theme={p.row.status === 'active' ? 'success' : 'default'}>{p.row.status}</Tag> },
            ]}
          />
        </div>
      </div>
    </div>
  )
}
