// 会话存档 Tab（E8 批 · 前端批2，2026-09-24）：客户说过什么，商家侧要能查证，
// 而"查证"这件事本身也要留下痕迹。
//
// 页面按后端三条硬口径写，任何一条自己发挥都会出事：
//  1. **密钥只出公钥**。生成密钥对的接口连私钥的掩码都不返回（`private_key_echo` 恒 false），
//     页面上也就没有任何"查看私钥"的入口——不是漏做，是这条能力**不该存在**。
//     私钥只在配置时由管理员从企微后台粘进来，提交后不再可读。
//  2. **同步失败是 200 + ok=false + 稳定原因码**。官方 SDK 没编进来、私钥没配、密文轮换，
//     这些都是环境状态而不是服务崩；把它们渲染成红色 500 会让管理员去提故障单。
//  3. **列表只有 120 字摘要，全文在详情**，而每次读全文都会写一条审计。
//     所以详情弹窗要明确提示"这次查看会被记录"，而不是安静地把全文摊开。
import { useCallback, useEffect, useState } from 'react'
import { Button, Dialog, Input, MessagePlugin, Select, Table, Tag } from 'tdesign-react'
import type { I18nKey } from '../../i18n/context'
import { useI18n } from '../../i18n/useI18n'
import { AUTH } from '../../lib/api'
import type {
  ArchiveKeyResp, ArchiveRecordDetailResp, ArchiveRecordListResp, ArchiveRecordRow,
  ArchiveStatus, ArchiveSyncResp,
} from '../../types'

// ApiResp 本文件接口调用的标准信封：code/message 为状态位，reason 为稳定原因码，data 为业务负载。
type ApiResp<T> = { code: number; message?: string; reason?: string; data?: T }
// ChannelView 会话存档 Tab 的通道下拉项：仅企微自建应用具备存档协议，其余通道类型不进选项。
type ChannelView = { id: number; type: string; name: string; status: string }

// P2-14：模块级常量拿不到 t()，原因码表只存「稳定码 → I18nKey」。
/** 同步/配置拒绝的稳定原因码 → 文案键（码不改，话可改）。 */
const ARCHIVE_REASON_KEYS: Record<string, I18nKey> = {
  archive_disabled: 'archive.reasonDisabled',
  archive_sdk_not_built: 'archive.reasonSdkNotBuilt',
  archive_key_missing: 'archive.reasonKeyMissing',
  archive_secret_rekey_required: 'archive.reasonRekey',
  archive_key_format: 'archive.reasonKeyFormat',
  archive_record_not_found: 'archive.reasonRecordNotFound',
  channel_not_found: 'archive.reasonChannelNotFound',
  param_error: 'archive.reasonParamError',
}

/** 原因码 → 文案键；未登记返回 null（由调用方回落到服务端消息）。 */
function archiveKey(reason: string | undefined): I18nKey | null {
  return (reason && ARCHIVE_REASON_KEYS[reason]) || null
}

/**
 * 存档原因码 → 文案；未知码原样带出服务端消息（后端加了新码而前端没跟，也要看得见）。
 * 翻译函数由调用方注入——本函数是模块级纯函数，拿不到 hook。
 */
function archiveText(reason: string | undefined, fallback: string, t: (k: I18nKey) => string): string {
  const k = archiveKey(reason)
  return k ? t(k) : fallback
}

/** 会话存档 Tab：选通道 → 看配置与状态 → 手动同步 → 翻留痕名单 → 读全文。 */
export function ChatArchiveTab() {
  const { t } = useI18n()
  const [channels, setChannels] = useState<ChannelView[]>([])
  const [channelId, setChannelId] = useState('')
  const [status, setStatus] = useState<ArchiveStatus | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [syncNote, setSyncNote] = useState('')
  const [keyOpen, setKeyOpen] = useState(false)
  const [cfgOpen, setCfgOpen] = useState(false)

  // 通道列表只用于挑选，存档只对企微自建应用有意义（其余通道没有 msgaudit 这条协议）
  const loadChannels = useCallback(async () => {
    const j = (await AUTH('/api/v1/admin/channels')) as ApiResp<{ list: ChannelView[] }> | null
    const list = (j?.data?.list || []).filter((c) => c.type === 'wecom_app')
    setChannels(list)
    // 已有选择就留着（同步/改配置后重拉不应把用户选中的通道跳回第一个）
    setChannelId((prev) => (prev && list.some((c) => String(c.id) === prev) ? prev : (list[0] ? String(list[0].id) : '')))
  }, [])

  useEffect(() => { void (async () => { await loadChannels() })() }, [loadChannels])

  const loadStatus = useCallback(async () => {
    if (!channelId) { setStatus(null); return }
    const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive`)) as ApiResp<{ status: ArchiveStatus }> | null
    if (j?.code === 0) setStatus(j.data?.status ?? null)
    else { setStatus(null); MessagePlugin.error(j?.message || t('archive.statusLoadFailed')) }
  }, [channelId, t])

  useEffect(() => { void (async () => { await loadStatus() })() }, [loadStatus])

  /** 手动同步一轮：永远回 200，成功与否看 data.ok，失败按 reason 给话术。 */
  async function syncNow() {
    if (!channelId) return
    setSyncing(true)
    setSyncNote('')
    const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive/sync`, { method: 'POST' })) as ApiResp<ArchiveSyncResp> | null
    setSyncing(false)
    if (!j || j.code !== 0) { setSyncNote(archiveText(j?.reason, j?.message || t('archive.syncUnfinished'), t)); return }
    const d = j.data
    if (!d?.ok) { setSyncNote(archiveText(d?.reason, t('archive.syncEmpty'), t)); void loadStatus(); return }
    const r = d.result
    setSyncNote(t('archive.syncResult', {
      fetched: r.fetched, stored: r.stored, dup: r.dup_skipped,
      stale: r.stale_skipped, failed: r.decrypt_failed, maxSeq: r.max_seq,
    }))
    void loadStatus()
  }

  const ch = channels.find((c) => String(c.id) === channelId)

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
        <div className="text-[13px] text-gray-500">
          {t('archive.introA')}{' '}<strong>{t('archive.introStrong')}</strong>{t('archive.introB')}
        </div>
        {channels.length === 0 ? (
          <div className="text-[13px] text-gray-400">{t('archive.noChannel')}</div>
        ) : (
          <div className="flex flex-wrap gap-2 items-center">
            <span className="text-xs text-gray-500">{t('archive.channel')}</span>
            <Select value={channelId} onChange={(v) => { setChannelId(String(v)); setSyncNote('') }} style={{ width: 240 }} size="small"
              placeholder={t('common.pleaseSelect')}
              options={channels.map((c) => ({ label: `${c.name}（#${c.id}）`, value: String(c.id) }))} />
            <Button theme="default" variant="outline" size="small" disabled={!channelId} onClick={() => setCfgOpen(true)}>{t('archive.cfgKeySecret')}</Button>
            <Button theme="default" variant="outline" size="small" disabled={!channelId} onClick={() => setKeyOpen(true)}>{t('archive.genKey')}</Button>
            <Button theme="primary" size="small" loading={syncing} disabled={!channelId} onClick={() => void syncNow()}>
              {syncing ? t('archive.syncing') : t('archive.syncNow')}
            </Button>
          </div>
        )}
        {syncNote && (
          <div className="text-[13px] bg-gray-50 border border-gray-200 rounded px-3 py-2 whitespace-pre-wrap">{syncNote}</div>
        )}
      </div>

      {status && <StatusPanel st={status} channelName={ch?.name || ''} channelId={Number(channelId)} />}
      {/* key=通道号：换通道整块重挂载，页码自然回第 1 页，比在 effect 里同步 setState 少一轮级联渲染 */}
      {channelId && <RecordsPanel key={channelId} channelId={Number(channelId)} status={status} />}

      {keyOpen && <KeyGenDialog channelId={Number(channelId)} onClose={() => setKeyOpen(false)} onGenerated={() => { void loadStatus() }} />}
      {cfgOpen && status && (
        <ArchiveConfigDialog channelId={Number(channelId)} st={status}
          onClose={() => setCfgOpen(false)}
          onDone={() => { setCfgOpen(false); void loadStatus(); void loadChannels() }} />
      )}
    </div>
  )
}

/** 状态摘要面板：全部是计数与布尔，**没有任何密钥材料**（连掩码都不显示）。 */
function StatusPanel({ st, channelName, channelId }: { st: ArchiveStatus; channelName: string; channelId: number }) {
  const { t } = useI18n()
  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium">{channelName || t('archive.channelHash', { id: channelId })}</span>
        {st.enabled ? <Tag theme="success">{t('archive.enabledOn')}</Tag> : <Tag theme="default">{t('archive.enabledOff')}</Tag>}
        {st.key_configured ? <Tag theme="success">{t('archive.keyOn')}</Tag> : <Tag theme="danger">{t('archive.keyOff')}</Tag>}
        {st.secret_configured ? <Tag theme="success">{t('archive.secretOn')}</Tag> : <Tag theme="danger">{t('archive.secretOff')}</Tag>}
        {st.fetcher_ready
          ? <Tag theme="success">{t('archive.fetcherOn')}</Tag>
          : <Tag theme="warning">{t('archive.fetcherOff')}</Tag>}
      </div>
      {st.sdk_reason && (
        <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
          {archiveText(st.sdk_reason, t('archive.sdkGap', { reason: st.sdk_reason }), t)}
        </div>
      )}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-[13px]">
        <Cell label={t('archive.pubKeyVer')} value={st.public_key_ver ? `v${st.public_key_ver}` : t('archive.pubKeyNotGen')} />
        <Cell label={t('archive.fingerprint')} value={st.fingerprint || '—'} mono />
        <Cell label={t('archive.cursorSeq')} value={String(st.cursor_seq)} />
        <Cell label={t('archive.lastMsg')} value={st.last_msg_at || t('archive.lastMsgNone')} />
        <Cell label={t('archive.storedTotal')} value={t('archive.countUnit', { n: st.stored_total })} />
        <Cell label={t('archive.failedTotal')} value={t('archive.countUnit', { n: st.failed_total })} warn={st.failed_total > 0} />
        <Cell label={t('archive.verMismatch')} value={t('archive.countUnit', { n: st.ver_mismatch_total })} warn={st.ver_mismatch_total > 0} />
      </div>
      {st.ver_mismatch_total > 0 && (
        <div className="text-xs text-orange-700">
          {t('archive.mismatchA', { n: st.ver_mismatch_total })}<strong>{t('archive.mismatchStrong')}</strong>{t('archive.mismatchB')}
        </div>
      )}
    </div>
  )
}

/** 一格只读统计。 */
function Cell({ label, value, warn, mono }: { label: string; value: string; warn?: boolean; mono?: boolean }) {
  return (
    <div>
      <div className="text-[11px] text-gray-400">{label}</div>
      <div className={`text-[13px] break-all ${warn ? 'text-orange-700 font-medium' : ''} ${mono ? 'font-mono text-[12px]' : ''}`}>{value}</div>
    </div>
  )
}

/** 密钥对生成弹窗：只回公钥，私钥直接加密入库，页面上永不出现。 */
function KeyGenDialog({ channelId, onClose, onGenerated }: { channelId: number; onClose: () => void; onGenerated: () => void }) {
  const { t } = useI18n()
  const [bits, setBits] = useState('2048')
  const [ver, setVer] = useState('')
  const [busy, setBusy] = useState(false)
  const [res, setRes] = useState<ArchiveKeyResp | null>(null)
  const [err, setErr] = useState('')

  async function gen() {
    setBusy(true)
    setErr('')
    const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive/key`, {
      method: 'POST',
      body: { bits: Number(bits), public_key_ver: ver.trim() === '' ? 0 : Number(ver) },
    })) as ApiResp<ArchiveKeyResp> | null
    setBusy(false)
    if (j?.code !== 0) { setErr(archiveText(j?.reason, j?.message || t('archive.keyGenFailed'), t)); return }
    if (j.data?.private_key_echo) {
      // 后端承诺恒 false；万一哪天变了，这里宁可拒绝显示也不把私钥摊到界面上
      setErr(t('archive.refusePrivateKey'))
      return
    }
    setRes(j.data ?? null)
    // 生成即改了公钥版本：状态要立刻回读，但**不关窗**——管理员还要复制这段公钥去企微后台贴
    onGenerated()
  }

  async function copyPem() {
    if (!res?.public_key_pem) return
    try { await navigator.clipboard.writeText(res.public_key_pem); MessagePlugin.success(t('archive.pubCopied')) }
    catch { MessagePlugin.warning(t('archive.clipboardDenied')) }
  }

  return (
    <Dialog header={t('archive.keyGenTitle')} visible onClose={onClose} width="640px"
      footer={
        <div className="flex justify-end gap-2">
          <Button theme="default" variant="outline" onClick={onClose}>{res ? t('archive.done') : t('common.cancel')}</Button>
          {!res && <Button theme="primary" loading={busy} onClick={() => void gen()}>{t('archive.gen')}</Button>}
        </div>
      }>
      {!res && (
        <div className="space-y-3">
          <p className="text-[13px] text-gray-500">
            {t('archive.keyGenIntroA')}<strong>{t('archive.keyGenIntroStrong')}</strong>{t('archive.keyGenIntroB')}
          </p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('archive.keyBits')}</label>
              <Select value={bits} onChange={(v) => setBits(String(v))} style={{ width: '100%' }}
                options={[{ label: t('archive.bits2048'), value: '2048' }, { label: '4096', value: '4096' }]} />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">{t('archive.keyVerPh')}</label>
              <Input value={ver} onChange={(v) => setVer(String(v))} placeholder={t('archive.wecomVerPh')} />
            </div>
          </div>
          {err && <div className="text-[13px] text-red-600">{err}</div>}
        </div>
      )}
      {res && (
        <div className="space-y-3">
          <div className="text-[13px] text-green-700 bg-green-50 border border-green-200 rounded px-3 py-2">
            {t('archive.keyGenOk', { ver: res.public_key_ver, fp: res.fingerprint })}
          </div>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-gray-500">{t('archive.pubPemLabel')}</span>
              <Button size="small" variant="text" onClick={() => void copyPem()}>{t('archive.copy')}</Button>
            </div>
            <textarea readOnly value={res.public_key_pem} rows={8} className="w-full text-[11px] font-mono border rounded p-2 bg-gray-50" />
          </div>
        </div>
      )}
    </Dialog>
  )
}

/** 存档配置弹窗：开关 + 存档 Secret + 私钥粘贴 + 公钥版本。密钥字段只写不读。 */
function ArchiveConfigDialog({ channelId, st, onClose, onDone }: {
  channelId: number; st: ArchiveStatus; onClose: () => void; onDone: () => void
}) {
  const { t } = useI18n()
  const [enabled, setEnabled] = useState(st.enabled)
  const [secret, setSecret] = useState('')
  const [pem, setPem] = useState('')
  const [ver, setVer] = useState(String(st.public_key_ver || ''))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  async function save() {
    setBusy(true)
    setErr('')
    const body: Record<string, unknown> = { enabled, public_key_ver: Number(ver || 0) }
    // 空串=不改动，而不是"清空"：密钥与 Secret 没有"清空"这条业务动作
    if (secret.trim() !== '') body.archive_secret = secret.trim()
    if (pem.trim() !== '') body.private_key_pem = pem.trim()
    const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive`, { method: 'PUT', body })) as ApiResp<{ status: ArchiveStatus }> | null
    setBusy(false)
    if (j?.code !== 0) { setErr(archiveText(j?.reason, j?.message || t('archive.cfgSaveFailed'), t)); return }
    // 「被拒不半落库」：任何一路失败整笔回滚，这里回读到的就是最终态
    if (!enabled) MessagePlugin.success(t('archive.cfgDisabledOk'))
    else MessagePlugin.success(t('archive.cfgSavedOk'))
    // 关窗与刷新都由父层的 onDone 负责（它同时回读状态与通道列表），此处不再重复关一次
    onDone()
  }

  return (
    <Dialog header={t('archive.cfgTitle')} visible onClose={onClose} width="600px"
      footer={
        <div className="flex justify-end gap-2">
          <Button theme="default" variant="outline" onClick={onClose}>{t('common.cancel')}</Button>
          <Button theme="primary" loading={busy} onClick={() => void save()}>{t('crud.save')}</Button>
        </div>
      }>
      <div className="space-y-3">
        <label className="flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          {t('archive.enableChannel')}
        </label>
        <div className="text-xs text-gray-500 -mt-1">
          {t('archive.cfgNowA')}{st.key_configured ? t('archive.keyOn') : t('archive.keyOff')} / {st.secret_configured ? t('archive.secretOn') : t('archive.secretOff')}{t('archive.cfgNowB')}<strong>{t('archive.cfgNowStrong')}</strong>{t('archive.cfgNowC')}
        </div>
        <div>
          <label className="block text-xs text-gray-500 mb-1">{t('archive.wecomSecret')}</label>
          <Input value={secret} onChange={(v) => setSecret(String(v))} placeholder={t('archive.secretPh')} autocomplete="off" />
        </div>
        <div>
          <label className="block text-xs text-gray-500 mb-1">{t('archive.privatePem')}</label>
          <textarea value={pem} onChange={(e) => setPem(e.target.value)} rows={5} placeholder={t('archive.pemPh')}
            className="w-full text-[11px] font-mono border rounded p-2" />
        </div>
        <div style={{ maxWidth: 220 }}>
          <label className="block text-xs text-gray-500 mb-1">{t('archive.pubVerLabel')}</label>
          <Input value={ver} onChange={(v) => setVer(String(v))} />
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
      </div>
    </Dialog>
  )
}

/** 留痕名单：列表**不含正文**，只有 120 字摘要；筛选/翻页都带着通道走。 */
function RecordsPanel({ channelId, status }: { channelId: number; status: ArchiveStatus | null }) {
  const { t } = useI18n()
  const [data, setData] = useState<ArchiveRecordListResp | null>(null)
  const [page, setPage] = useState(1)
  const [loading, setLoading] = useState(true)
  const [filters, setFilters] = useState({ msg_type: '', chat_type: '', from_user: '', keyword: '', failed: '' })
  const [detail, setDetail] = useState<{ id: number; seen: boolean } | null>(null)

  // 换通道由父层用 key 整块重挂载（页码/筛选一起归零，见调用点），此处只跟请求
  useEffect(() => {
    void (async () => {
      const q = new URLSearchParams({ page: String(page), page_size: '20' })
      Object.entries(filters).forEach(([k, v]) => { if (v.trim() !== '') q.set(k, v.trim()) })
      const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive/records?${q.toString()}`)) as ApiResp<ArchiveRecordListResp> | null
      if (j?.code === 0) setData(j.data ?? null)
      else MessagePlugin.error(j?.message || t('archive.listLoadFailed'))
      setLoading(false)
    })()
  }, [channelId, page, filters, t])

  const setF = (k: keyof typeof filters, v: string) => { setLoading(true); setPage(1); setFilters({ ...filters, [k]: v }) }
  const goto = (p: number) => { setLoading(true); setPage(p) }

  const cols = [
    { colKey: 'seq', title: 'seq', width: 90, cell: (p: { row: ArchiveRecordRow }) => <span className="tabular-nums text-[12px]">{p.row.seq}</span> },
    { colKey: 'msg_time', title: t('archive.colMsgTime'), width: 165, cell: (p: { row: ArchiveRecordRow }) => (
      <span className="text-[12px]">{p.row.msg_time || <span className="text-gray-400">{t('archive.noTimestamp')}</span>}</span>
    ) },
    { colKey: 'from', title: t('archive.colSender'), width: 150, cell: (p: { row: ArchiveRecordRow }) => (
      <div>
        <div className="text-[13px]">{p.row.sender_name || p.row.from_user || '—'}</div>
        <div className="text-[11px] text-gray-400">{p.row.chat_type === 'external' ? t('archive.chatExternal') : p.row.chat_type === 'single' ? t('archive.chatSingle') : p.row.chat_type || ''}</div>
      </div>
    ) },
    { colKey: 'msg_type', title: t('archive.colType'), width: 80, cell: (p: { row: ArchiveRecordRow }) => p.row.msg_type || '—' },
    { colKey: 'text_preview', title: t('archive.colPreview'), width: 320, cell: (p: { row: ArchiveRecordRow }) => (
      p.row.decrypt_error
        ? <span className="text-[12px] text-orange-700" title={p.row.decrypt_error}>{t('archive.brokenPreview', { err: p.row.decrypt_error })}</span>
        : <span className="text-[13px] whitespace-pre-wrap break-all">{p.row.text_preview || <span className="text-gray-400">{t('archive.noText')}</span>}</span>
    ) },
    { colKey: 'op', title: t('crud.actions'), width: 90, cell: (p: { row: ArchiveRecordRow }) => (
      <Button size="small" variant="text" disabled={!p.row.has_full_text} onClick={() => setDetail({ id: p.row.id, seen: false })}>{t('archive.readFull')}</Button>
    ) },
  ]

  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium">{t('archive.recordsTitle')}</span>
        <Input placeholder={t('archive.fromUserPh')} size="small" style={{ width: 150 }} value={filters.from_user} onChange={(v) => setF('from_user', String(v))} />
        <Input placeholder={t('archive.keywordPh')} size="small" style={{ width: 190 }} value={filters.keyword} onChange={(v) => setF('keyword', String(v))} />
        <Select placeholder={t('archive.msgType')} size="small" style={{ width: 120 }} clearable value={filters.msg_type}
          onChange={(v) => setF('msg_type', String(v ?? ''))}
          options={['text', 'image', 'voice', 'video', 'file', 'link', 'revokelist'].map((x) => ({ label: x, value: x }))} />
        <Select placeholder={t('archive.decryptStatus')} size="small" style={{ width: 120 }} clearable value={filters.failed}
          onChange={(v) => setF('failed', String(v ?? ''))}
          options={[{ label: t('archive.onlyFailed'), value: 'true' }, { label: t('archive.onlyOk'), value: 'false' }]} />
        {status && !status.enabled && <span className="text-xs text-gray-400">{t('archive.histOnly')}</span>}
      </div>
      <Table rowKey="id" data={data?.list || []} columns={cols} size="small" loading={loading} empty={t('archive.empty')} />
      {data && (
        <div className="flex items-center gap-3 text-xs text-gray-500">
          <Button size="small" variant="text" disabled={page <= 1} onClick={() => goto(page - 1)}>{t('archive.prevPage')}</Button>
          <span>{t('archive.pager', { page: data.page, pages: Math.max(1, Math.ceil(data.total / data.page_size)), size: data.page_size, cap: data.page_size_cap })}</span>
          <Button size="small" variant="text" disabled={page * data.page_size >= data.total} onClick={() => goto(page + 1)}>{t('archive.nextPage')}</Button>
          <span className="text-gray-400">{data.note}</span>
        </div>
      )}
      {detail && <RecordDetailDialog id={detail.id} onClose={() => setDetail(null)} />}
    </div>
  )
}

/** 详情弹窗：进页面即调接口取全文——**这一发就会落一条审计**，所以顶部如实写明。 */
function RecordDetailDialog({ id, onClose }: { id: number; onClose: () => void }) {
  const { t } = useI18n()
  const [rec, setRec] = useState<ArchiveRecordDetailResp['record'] | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    void (async () => {
      const j = (await AUTH(`/api/v1/admin/channel-archive/records/${id}`)) as ApiResp<ArchiveRecordDetailResp> | null
      if (j?.code === 0) setRec(j.data?.record ?? null)
      else setErr(archiveText(j?.reason, j?.message || t('archive.detailLoadFailed'), t))
    })()
  }, [id, t])

  return (
    <Dialog header={t('archive.detailTitle', { id })} visible onClose={onClose} width="680px"
      footer={<div className="flex justify-end"><Button theme="default" variant="outline" onClick={onClose}>{t('common.close')}</Button></div>}>
      <div className="space-y-3">
        <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
          {t('archive.auditNotice')}
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
        {!err && !rec && <div className="text-[13px] text-gray-400 py-6 text-center">{t('common.loading')}</div>}
        {rec && (
          <>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-[13px]">
              <Cell label="seq" value={String(rec.seq)} />
              <Cell label={t('archive.msgid')} value={rec.msgid || '—'} mono />
              <Cell label={t('archive.colMsgTime')} value={rec.msg_time || t('archive.noTimestamp')} />
              <Cell label={t('archive.colSender')} value={rec.sender_name || rec.from_user || '—'} />
              <Cell label={t('archive.colType')} value={rec.msg_type || '—'} />
              <Cell label={t('archive.chat')} value={rec.chatid || '—'} mono />
              <Cell label={t('archive.pubKeyVer')} value={`v${rec.public_key_ver}`} />
              <Cell label={t('archive.mediaStatus')} value={rec.media_status || '—'} />
            </div>
            <div>
              <div className="text-xs text-gray-500 mb-1">{t('archive.fullTextLabel')}</div>
              <div className="text-[13px] whitespace-pre-wrap break-all bg-gray-50 border rounded p-3 min-h-[60px]">
                {rec.content_text || <span className="text-gray-400">{t('archive.noFullText')}</span>}
              </div>
            </div>
            {rec.decrypt_error && <div className="text-[13px] text-orange-700">{t('archive.decryptWarn', { err: rec.decrypt_error })}</div>}
            {rec.to_list && (
              <div>
                <div className="text-xs text-gray-500 mb-1">{t('archive.toListLabel')}</div>
                <div className="text-[11px] font-mono break-all bg-gray-50 border rounded p-2">{rec.to_list}</div>
              </div>
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
