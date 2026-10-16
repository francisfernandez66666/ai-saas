// F8 CDP 画像管理页：标签字典、分群圈选、OneID 360° 画像查询；P2-14 文案迁移至 i18n
import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Input, Select, Table, Tag } from 'tdesign-react'
import type { CrudRow } from '../../hooks/useCrud'
import { AUTH } from '../../lib/api'
import { useI18n } from '../../i18n/useI18n'
import type { I18nKey } from '../../i18n/context'
import type { CellProps, TableRowData } from '../../types'

// ProfileView CDP OneID 360° 画像查询结果：客户标识、状态、事件总数与标签字典。
type ProfileView = {
  one_id: string
  name: string
  status: number
  event_count: number
  tags: Record<string, string>
}

const CATEGORY_LABEL: Record<string, I18nKey> = {
  intent: 'admin.cdp.catIntent',
  behavior: 'admin.cdp.catBehavior',
  preference: 'admin.cdp.catPreference',
  attribute: 'admin.cdp.catAttribute',
  attitude: 'admin.cdp.catAttitude',
  env: 'admin.cdp.catEnv',
}

// CdpTab 后台「CDP 客户数据」配置页：维护标签/画像等客户数据定义。
export default function CdpTab() {
  const { t } = useI18n()
  const [defs, setDefs] = useState<CrudRow[]>([])
  const [customers, setCustomers] = useState<CrudRow[]>([])
  const [selectedTag, setSelectedTag] = useState<string>('')
  const [segmentTag, setSegmentTag] = useState('')
  const [segmentIds, setSegmentIds] = useState<string[]>([])
  const [oneId, setOneId] = useState('')
  const [profile, setProfile] = useState<ProfileView | null>(null)
  const [loading, setLoading] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    const [d, c] = await Promise.all([
      AUTH('/api/v1/cdp/tag-defs'),
      AUTH('/api/v1/customers?page_size=50'),
    ])
    if (d?.code === 0) {
      const rows = d.data || []
      setDefs(rows)
      if (!selectedTag) setSelectedTag(rows.find((x: CrudRow) => x.is_active)?.code || rows[0]?.code || '')
    }
    if (c?.code === 0) setCustomers(c.data?.list || [])
    setLoading(false)
  }, [selectedTag])

  useEffect(() => { void load() }, [load])

  const runSegment = useCallback(async () => {
    const tag = selectedTag
    if (!tag) return
    setLoading(true)
    const j = await AUTH(`/api/v1/cdp/segments?tag=${encodeURIComponent(tag)}`)
    if (j?.code === 0) {
      setSegmentTag(j.data?.tag || tag)
      setSegmentIds(j.data?.one_ids || [])
    }
    setLoading(false)
  }, [selectedTag])

  const loadProfile = useCallback(async (id: string) => {
    const v = id.trim()
    if (!v) return
    setLoading(true)
    const j = await AUTH(`/api/v1/cdp/profiles/${encodeURIComponent(v)}`)
    if (j?.code === 0) setProfile(j.data)
    setLoading(false)
  }, [])

  const defMap = useMemo(() => new Map(defs.map((d) => [String(d.code), d])), [defs])
  const profileTags = useMemo(() => Object.entries(profile?.tags || {}).map(([code, value]) => ({
    code,
    name: defMap.get(code)?.name || code,
    category: defMap.get(code)?.category || '-',
    value,
  })), [defMap, profile])

  const onCustomer = (id: number) => {
    const v = `c:${id}`
    setOneId(v)
    void loadProfile(v)
  }

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">{t('admin.cdp.segTitle')}</h3>
        <p className="text-xs text-gray-400 mt-0.5">{t('admin.cdp.segDesc')}</p>
        <div className="mt-4 flex flex-wrap items-end gap-3">
          <div style={{ minWidth: 260 }}>
            <div className="text-xs text-gray-500 mb-1">{t('admin.cdp.lblTag')}</div>
            <Select
              value={selectedTag}
              onChange={(v) => setSelectedTag(String(v))}
              options={defs.filter((d) => d.is_active).map((d) => ({ label: `${d.name}（${d.code}）`, value: String(d.code) }))}
              filterable
              placeholder={t('admin.cdp.phTag')}
              style={{ width: '100%' }}
            />
          </div>
          <Button theme="primary" loading={loading} disabled={loading || !selectedTag} onClick={() => void runSegment()}>{t('admin.cdp.segment')}</Button>
          <span className="text-xs text-gray-400">{t('admin.cdp.hit', { n: segmentIds.length })}{segmentTag ? t('admin.cdp.hitTag', { tag: segmentTag }) : ''}</span>
        </div>
        {segmentIds.length > 0 && <div className="mt-3 flex flex-wrap gap-2">
          {segmentIds.slice(0, 80).map((id) => <Tag key={id}>{id}</Tag>)}
          {segmentIds.length > 80 && <span className="text-xs text-gray-400">{t('admin.cdp.more', { n: segmentIds.length - 80 })}</span>}
        </div>}
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800">{t('admin.cdp.profileTitle')}</h3>
        <div className="mt-4 grid grid-cols-1 md:grid-cols-3 gap-3">
          <div className="md:col-span-2">
            <div className="text-xs text-gray-500 mb-1">{t('admin.cdp.lblOneId')}</div>
            <Input value={oneId} onChange={setOneId} placeholder={t('admin.cdp.phOneIdEx')} style={{ width: '100%' }} />
          </div>
          <div className="flex items-end"><Button theme="primary" loading={loading} disabled={loading || !oneId.trim()} onClick={() => void loadProfile(oneId)}>{t('admin.cdp.queryProfile')}</Button></div>
          <div className="md:col-span-3">
            <div className="text-xs text-gray-500 mb-1">{t('admin.cdp.commonCustomers')}</div>
            <div className="flex flex-wrap gap-2">
              {customers.slice(0, 20).map((c) => <Button key={c.id} size="small" variant="outline" onClick={() => onCustomer(Number(c.id))}>{c.id} {c.name || t('admin.cdp.customer')}</Button>)}
            </div>
          </div>
        </div>
        {profile && (
          <div className="mt-4 border border-gray-100 rounded-lg p-4 bg-gray-50">
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-sm">
              <div><div className="text-xs text-gray-400">OneID</div><div className="font-semibold break-all">{profile.one_id}</div></div>
              <div><div className="text-xs text-gray-400">{t('admin.cdp.lblName')}</div><div>{profile.name || '-'}</div></div>
              <div><div className="text-xs text-gray-400">{t('common.status')}</div><Tag theme={Number(profile.status) === 1 ? 'success' : 'default'}>{Number(profile.status) === 1 ? t('admin.cdp.active') : t('admin.cdp.inactive')}</Tag></div>
              <div><div className="text-xs text-gray-400">{t('admin.cdp.lblEvents')}</div><div className="font-semibold">{profile.event_count}</div></div>
            </div>
            <div className="mt-4">
              <Table
                rowKey="code"
                data={profileTags as TableRowData[]}
                size="small"
                empty={t('admin.cdp.emptyTags')}
                columns={[
                  { colKey: 'name', title: t('admin.cdp.colTag'), width: 180 },
                  { colKey: 'code', title: t('common.code'), width: 180 },
                  { colKey: 'category', title: t('admin.cdp.colCategory'), width: 120, cell: (p: CellProps) => { const k = CATEGORY_LABEL[String(p.row.category)]; return <Tag>{k ? t(k) : p.row.category}</Tag> } },
                  { colKey: 'value', title: t('admin.cdp.colValue') },
                ]}
              />
            </div>
          </div>
        )}
      </div>

      <div className="bg-white rounded-lg shadow-sm p-5">
        <h3 className="text-base font-bold text-gray-800 mb-3">{t('admin.cdp.dictTitle')}</h3>
        <Table
          rowKey="id"
          loading={loading}
          data={defs as TableRowData[]}
          size="small"
          empty={t('admin.cdp.emptyDict')}
          pagination={{ defaultPageSize: 10 }}
          columns={[
            { colKey: 'code', title: t('common.code'), width: 180 },
            { colKey: 'name', title: t('common.name'), width: 180 },
            { colKey: 'category', title: t('admin.cdp.colCategory'), width: 120, cell: (p: CellProps) => { const k = CATEGORY_LABEL[String(p.row.category)]; return <Tag>{k ? t(k) : p.row.category}</Tag> } },
            { colKey: 'weight_default', title: t('admin.cdp.colWeight'), width: 110 },
            { colKey: 'is_active', title: t('common.status'), width: 100, cell: (p: CellProps) => <Tag theme={p.row.is_active ? 'success' : 'default'}>{p.row.is_active ? t('admin.cdp.active') : t('admin.cdp.inactive')}</Tag> },
          ]}
        />
      </div>
    </div>
  )
}
