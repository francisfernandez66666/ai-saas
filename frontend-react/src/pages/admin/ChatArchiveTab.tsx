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
import { AUTH } from '../../lib/api'
import type {
  ArchiveKeyResp, ArchiveRecordDetailResp, ArchiveRecordListResp, ArchiveRecordRow,
  ArchiveStatus, ArchiveSyncResp,
} from '../../types'

type ApiResp<T> = { code: number; message?: string; reason?: string; data?: T }
type ChannelView = { id: number; type: string; name: string; status: string }

/** 同步/配置拒绝的稳定原因码 → 管理员看得懂的一句话（码不改，话可改）。 */
const ARCHIVE_REASON_TEXT: Record<string, string> = {
  archive_disabled: '这个通道还没开启会话存档。请先在企微后台完成存档开通与成员授权，再回来打开下面的开关。',
  archive_sdk_not_built: '取数组件（企微官方 C SDK）尚未编入本程序——存档链路已就绪，等接入即自动生效，这不是故障。',
  archive_key_missing: '还没有配置解密私钥。请先生成密钥对并把私钥粘进来（私钥提交后不可再读）。',
  archive_secret_rekey_required: '企微后台的存档 Secret 已变更，存量密文解不开。需要在下方重新提交当前 Secret。',
  archive_key_format: '私钥格式不对：必须是 PEM 形态的 RSA 私钥，且与本通道配置的公钥版本配对。',
  archive_record_not_found: '这条存档记录不存在，或不属于当前租户。',
  channel_not_found: '通道不存在或已被删除。',
  param_error: '参数不合法，请检查输入。',
}

/** 存档原因码 → 文案；未知码原样带出服务端消息（后端加了新码而前端没跟，也要看得见）。 */
function archiveText(reason: string | undefined, fallback: string): string {
  return (reason && ARCHIVE_REASON_TEXT[reason]) || fallback
}

/** 会话存档 Tab：选通道 → 看配置与状态 → 手动同步 → 翻留痕名单 → 读全文。 */
export function ChatArchiveTab() {
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
    else { setStatus(null); MessagePlugin.error(j?.message || '存档状态读取失败') }
  }, [channelId])

  useEffect(() => { void (async () => { await loadStatus() })() }, [loadStatus])

  /** 手动同步一轮：永远回 200，成功与否看 data.ok，失败按 reason 给话术。 */
  async function syncNow() {
    if (!channelId) return
    setSyncing(true)
    setSyncNote('')
    const j = (await AUTH(`/api/v1/admin/channels/${channelId}/archive/sync`, { method: 'POST' })) as ApiResp<ArchiveSyncResp> | null
    setSyncing(false)
    if (!j || j.code !== 0) { setSyncNote(archiveText(j?.reason, j?.message || '同步请求未完成')); return }
    const d = j.data
    if (!d?.ok) { setSyncNote(archiveText(d?.reason, '本轮未拉取')); void loadStatus(); return }
    const r = d.result
    setSyncNote(`本轮取回 ${r.fetched} 条，新入库 ${r.stored} 条；重复跳过 ${r.dup_skipped} 条、过旧跳过 ${r.stale_skipped} 条、解不开 ${r.decrypt_failed} 条，游标推进到 ${r.max_seq}`)
    void loadStatus()
  }

  const ch = channels.find((c) => String(c.id) === channelId)

  return (
    <div className="space-y-4">
      <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
        <div className="text-[13px] text-gray-500">
          会话存档是**合规取证**能力，不是聊天工具：留痕里的正文只在点开详情时才会取回，
          且<strong>每次读取全文都会记一条审计</strong>（谁、看了哪一条）。企微侧的开通与成员授权必须在企微管理后台先做完。
        </div>
        {channels.length === 0 ? (
          <div className="text-[13px] text-gray-400">还没有企业微信自建应用通道。请先到「通道接入」创建一个。</div>
        ) : (
          <div className="flex flex-wrap gap-2 items-center">
            <span className="text-xs text-gray-500">通道</span>
            <Select value={channelId} onChange={(v) => { setChannelId(String(v)); setSyncNote('') }} style={{ width: 240 }} size="small"
              options={channels.map((c) => ({ label: `${c.name}（#${c.id}）`, value: String(c.id) }))} />
            <Button theme="default" variant="outline" size="small" disabled={!channelId} onClick={() => setCfgOpen(true)}>配置密钥 / Secret</Button>
            <Button theme="default" variant="outline" size="small" disabled={!channelId} onClick={() => setKeyOpen(true)}>生成密钥对</Button>
            <Button theme="primary" size="small" loading={syncing} disabled={!channelId} onClick={() => void syncNow()}>
              {syncing ? '同步中…' : '立即同步一轮'}
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
  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium">{channelName || `通道 #${channelId}`}</span>
        {st.enabled ? <Tag theme="success">存档已开启</Tag> : <Tag theme="default">存档未开启</Tag>}
        {st.key_configured ? <Tag theme="success">私钥已配置</Tag> : <Tag theme="danger">私钥未配置</Tag>}
        {st.secret_configured ? <Tag theme="success">Secret 已配置</Tag> : <Tag theme="danger">Secret 未配置</Tag>}
        {st.fetcher_ready
          ? <Tag theme="success">取数组件已接入</Tag>
          : <Tag theme="warning">取数组件未接入（等 SDK 编译，非故障）</Tag>}
      </div>
      {st.sdk_reason && (
        <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
          {archiveText(st.sdk_reason, `取数能力缺口：${st.sdk_reason}`)}
        </div>
      )}
      <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-[13px]">
        <Cell label="公钥版本" value={st.public_key_ver ? `v${st.public_key_ver}` : '未生成'} />
        <Cell label="公钥指纹" value={st.fingerprint || '—'} mono />
        <Cell label="拉取游标 seq" value={String(st.cursor_seq)} />
        <Cell label="最近一条消息" value={st.last_msg_at || '尚无'} />
        <Cell label="已入库" value={`${st.stored_total} 条`} />
        <Cell label="解不开" value={`${st.failed_total} 条`} warn={st.failed_total > 0} />
        <Cell label="公钥版本不符" value={`${st.ver_mismatch_total} 条`} warn={st.ver_mismatch_total > 0} />
      </div>
      {st.ver_mismatch_total > 0 && (
        <div className="text-xs text-orange-700">
          有 {st.ver_mismatch_total} 条是按<strong>旧版公钥</strong>加密的：仍然可读，但说明企微后台换过密钥版本，
          请把上面的「公钥版本」对准当前生效的那一版。
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
    if (j?.code !== 0) { setErr(archiveText(j?.reason, j?.message || '密钥生成失败')); return }
    if (j.data?.private_key_echo) {
      // 后端承诺恒 false；万一哪天变了，这里宁可拒绝显示也不把私钥摊到界面上
      setErr('服务端返回了私钥内容，本页拒绝显示——请立即核查后端改动')
      return
    }
    setRes(j.data ?? null)
    // 生成即改了公钥版本：状态要立刻回读，但**不关窗**——管理员还要复制这段公钥去企微后台贴
    onGenerated()
  }

  async function copyPem() {
    if (!res?.public_key_pem) return
    try { await navigator.clipboard.writeText(res.public_key_pem); MessagePlugin.success('公钥已复制') }
    catch { MessagePlugin.warning('浏览器拒绝了剪贴板，请手动选中复制') }
  }

  return (
    <Dialog header="生成存档密钥对" visible onClose={onClose} width="640px"
      footer={
        <div className="flex justify-end gap-2">
          <Button theme="default" variant="outline" onClick={onClose}>{res ? '完成' : '取消'}</Button>
          {!res && <Button theme="primary" loading={busy} onClick={() => void gen()}>生成</Button>}
        </div>
      }>
      {!res && (
        <div className="space-y-3">
          <p className="text-[13px] text-gray-500">
            生成后<strong>私钥直接加密入库、页面不显示也再也读不出来</strong>；你只需要把公钥贴到企微后台
            「会话内容存档 → 公钥」，并在后台把密钥版本记在这里的 ver 上。
          </p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs text-gray-500 mb-1">密钥长度</label>
              <Select value={bits} onChange={(v) => setBits(String(v))} style={{ width: '100%' }}
                options={[{ label: '2048（推荐）', value: '2048' }, { label: '4096', value: '4096' }]} />
            </div>
            <div>
              <label className="block text-xs text-gray-500 mb-1">公钥版本（留空=当前 +1）</label>
              <Input value={ver} onChange={(v) => setVer(String(v))} placeholder="企微后台显示的那一版" />
            </div>
          </div>
          {err && <div className="text-[13px] text-red-600">{err}</div>}
        </div>
      )}
      {res && (
        <div className="space-y-3">
          <div className="text-[13px] text-green-700 bg-green-50 border border-green-200 rounded px-3 py-2">
            密钥对已生成（第 v{res.public_key_ver} 版，指纹 {res.fingerprint}）。私钥已加密入库，页面不会显示它。
          </div>
          <div>
            <div className="flex items-center justify-between mb-1">
              <span className="text-xs text-gray-500">公钥 PEM（贴到企微后台）</span>
              <Button size="small" variant="text" onClick={() => void copyPem()}>复制</Button>
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
    if (j?.code !== 0) { setErr(archiveText(j?.reason, j?.message || '配置保存失败')); return }
    // 「被拒不半落库」：任何一路失败整笔回滚，这里回读到的就是最终态
    if (!enabled) MessagePlugin.success('已关闭存档（只改开关，已有留痕不删）')
    else MessagePlugin.success('存档配置已保存')
    // 关窗与刷新都由父层的 onDone 负责（它同时回读状态与通道列表），此处不再重复关一次
    onDone()
  }

  return (
    <Dialog header="存档配置" visible onClose={onClose} width="600px"
      footer={
        <div className="flex justify-end gap-2">
          <Button theme="default" variant="outline" onClick={onClose}>取消</Button>
          <Button theme="primary" loading={busy} onClick={() => void save()}>保存</Button>
        </div>
      }>
      <div className="space-y-3">
        <label className="flex items-center gap-2 text-[13px]">
          <input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} />
          开启本通道会话存档
        </label>
        <div className="text-xs text-gray-500 -mt-1">
          当前：{st.key_configured ? '私钥已配置' : '私钥未配置'} / {st.secret_configured ? 'Secret 已配置' : 'Secret 未配置'}。
          两项不齐时后端会拒绝开启开关，且<strong>不会半落库</strong>（不会出现"开关开了但密钥没收下"的中间态）。
        </div>
        <div>
          <label className="block text-xs text-gray-500 mb-1">企微存档 Secret（留空=不改动）</label>
          <Input value={secret} onChange={(v) => setSecret(String(v))} placeholder="提交后不可再读取" autocomplete="off" />
        </div>
        <div>
          <label className="block text-xs text-gray-500 mb-1">解密私钥 PEM（从企微后台下载的那份，留空=不改动）</label>
          <textarea value={pem} onChange={(e) => setPem(e.target.value)} rows={5} placeholder="-----BEGIN RSA PRIVATE KEY-----"
            className="w-full text-[11px] font-mono border rounded p-2" />
        </div>
        <div style={{ maxWidth: 220 }}>
          <label className="block text-xs text-gray-500 mb-1">公钥版本 ver</label>
          <Input value={ver} onChange={(v) => setVer(String(v))} />
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
      </div>
    </Dialog>
  )
}

/** 留痕名单：列表**不含正文**，只有 120 字摘要；筛选/翻页都带着通道走。 */
function RecordsPanel({ channelId, status }: { channelId: number; status: ArchiveStatus | null }) {
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
      else MessagePlugin.error(j?.message || '存档名单读取失败')
      setLoading(false)
    })()
  }, [channelId, page, filters])

  const setF = (k: keyof typeof filters, v: string) => { setLoading(true); setPage(1); setFilters({ ...filters, [k]: v }) }
  const goto = (p: number) => { setLoading(true); setPage(p) }

  const cols = [
    { colKey: 'seq', title: 'seq', width: 90, cell: (p: { row: ArchiveRecordRow }) => <span className="tabular-nums text-[12px]">{p.row.seq}</span> },
    { colKey: 'msg_time', title: '消息时间', width: 165, cell: (p: { row: ArchiveRecordRow }) => (
      <span className="text-[12px]">{p.row.msg_time || <span className="text-gray-400">无时间戳</span>}</span>
    ) },
    { colKey: 'from', title: '发送方', width: 150, cell: (p: { row: ArchiveRecordRow }) => (
      <div>
        <div className="text-[13px]">{p.row.sender_name || p.row.from_user || '—'}</div>
        <div className="text-[11px] text-gray-400">{p.row.chat_type === 'external' ? '外部群/客户' : p.row.chat_type === 'single' ? '单聊' : p.row.chat_type || ''}</div>
      </div>
    ) },
    { colKey: 'msg_type', title: '类型', width: 80, cell: (p: { row: ArchiveRecordRow }) => p.row.msg_type || '—' },
    { colKey: 'text_preview', title: '正文摘要（120 字）', width: 320, cell: (p: { row: ArchiveRecordRow }) => (
      p.row.decrypt_error
        ? <span className="text-[12px] text-orange-700" title={p.row.decrypt_error}>解不开：{p.row.decrypt_error}（仍占位留痕，游标不会卡在这条）</span>
        : <span className="text-[13px] whitespace-pre-wrap break-all">{p.row.text_preview || <span className="text-gray-400">（无文本内容）</span>}</span>
    ) },
    { colKey: 'op', title: '操作', width: 90, cell: (p: { row: ArchiveRecordRow }) => (
      <Button size="small" variant="text" disabled={!p.row.has_full_text} onClick={() => setDetail({ id: p.row.id, seen: false })}>读全文</Button>
    ) },
  ]

  return (
    <div className="bg-white rounded-lg shadow-sm p-4 space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[13px] font-medium">存档留痕</span>
        <Input placeholder="发送方 ID" size="small" style={{ width: 150 }} value={filters.from_user} onChange={(v) => setF('from_user', String(v))} />
        <Input placeholder="关键词（搜摘要与元数据）" size="small" style={{ width: 190 }} value={filters.keyword} onChange={(v) => setF('keyword', String(v))} />
        <Select placeholder="消息类型" size="small" style={{ width: 120 }} clearable value={filters.msg_type}
          onChange={(v) => setF('msg_type', String(v ?? ''))}
          options={['text', 'image', 'voice', 'video', 'file', 'link', 'revokelist'].map((t) => ({ label: t, value: t }))} />
        <Select placeholder="解密状态" size="small" style={{ width: 120 }} clearable value={filters.failed}
          onChange={(v) => setF('failed', String(v ?? ''))}
          options={[{ label: '只看解不开', value: 'true' }, { label: '只看解得开', value: 'false' }]} />
        {status && !status.enabled && <span className="text-xs text-gray-400">该通道存档未开启，以下为历史留痕（若有）</span>}
      </div>
      <Table rowKey="id" data={data?.list || []} columns={cols} size="small" loading={loading} empty="还没有存档留痕" />
      {data && (
        <div className="flex items-center gap-3 text-xs text-gray-500">
          <Button size="small" variant="text" disabled={page <= 1} onClick={() => goto(page - 1)}>上一页</Button>
          <span>第 {data.page} 页 / 共 {Math.max(1, Math.ceil(data.total / data.page_size))} 页 · 每页 {data.page_size} 条（硬顶 {data.page_size_cap}）</span>
          <Button size="small" variant="text" disabled={page * data.page_size >= data.total} onClick={() => goto(page + 1)}>下一页</Button>
          <span className="text-gray-400">{data.note}</span>
        </div>
      )}
      {detail && <RecordDetailDialog id={detail.id} onClose={() => setDetail(null)} />}
    </div>
  )
}

/** 详情弹窗：进页面即调接口取全文——**这一发就会落一条审计**，所以顶部如实写明。 */
function RecordDetailDialog({ id, onClose }: { id: number; onClose: () => void }) {
  const [rec, setRec] = useState<ArchiveRecordDetailResp['record'] | null>(null)
  const [err, setErr] = useState('')

  useEffect(() => {
    void (async () => {
      const j = (await AUTH(`/api/v1/admin/channel-archive/records/${id}`)) as ApiResp<ArchiveRecordDetailResp> | null
      if (j?.code === 0) setRec(j.data?.record ?? null)
      else setErr(archiveText(j?.reason, j?.message || '全文读取失败'))
    })()
  }, [id])

  return (
    <Dialog header={`存档原文 · #${id}`} visible onClose={onClose} width="680px"
      footer={<div className="flex justify-end"><Button theme="default" variant="outline" onClick={onClose}>关闭</Button></div>}>
      <div className="space-y-3">
        <div className="text-[13px] text-orange-700 bg-orange-50 border border-orange-200 rounded px-3 py-2">
          这次查看已记入审计日志（谁、看了哪一条）。原文属于客户个人信息，请按最小必要使用。
        </div>
        {err && <div className="text-[13px] text-red-600">{err}</div>}
        {!err && !rec && <div className="text-[13px] text-gray-400 py-6 text-center">加载中…</div>}
        {rec && (
          <>
            <div className="grid grid-cols-2 md:grid-cols-4 gap-3 text-[13px]">
              <Cell label="seq" value={String(rec.seq)} />
              <Cell label="msgid" value={rec.msgid || '—'} mono />
              <Cell label="消息时间" value={rec.msg_time || '无时间戳'} />
              <Cell label="发送方" value={rec.sender_name || rec.from_user || '—'} />
              <Cell label="类型" value={rec.msg_type || '—'} />
              <Cell label="会话" value={rec.chatid || '—'} mono />
              <Cell label="公钥版本" value={`v${rec.public_key_ver}`} />
              <Cell label="媒体状态" value={rec.media_status || '—'} />
            </div>
            <div>
              <div className="text-xs text-gray-500 mb-1">正文全文</div>
              <div className="text-[13px] whitespace-pre-wrap break-all bg-gray-50 border rounded p-3 min-h-[60px]">
                {rec.content_text || <span className="text-gray-400">这条留痕没有文本正文（图片/语音/文件等，媒体见下方原始 JSON）</span>}
              </div>
            </div>
            {rec.decrypt_error && <div className="text-[13px] text-orange-700">解密告警：{rec.decrypt_error}</div>}
            {rec.to_list && (
              <div>
                <div className="text-xs text-gray-500 mb-1">收件人 / 群成员原始 JSON</div>
                <div className="text-[11px] font-mono break-all bg-gray-50 border rounded p-2">{rec.to_list}</div>
              </div>
            )}
          </>
        )}
      </div>
    </Dialog>
  )
}
