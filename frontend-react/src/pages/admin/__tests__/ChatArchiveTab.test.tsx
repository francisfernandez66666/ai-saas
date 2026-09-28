// 会话存档 Tab 单测（E8 批 · 前端批2，2026-09-24）。
//
// 存档是合规能力，页面出错的方式很特别——不是"崩"，而是"该沉默的时候多嘴、
// 该说清的时候吓人"。所以断言钉在三条：
//   1. **界面上永远不该出现密钥材料**：状态、列表、详情都不带私钥，连掩码都不显示；
//      后端承诺 `private_key_echo` 恒 false，一旦哪天变成 true，页面必须拒绝显示而不是照摊。
//   2. **环境缺口不能渲染成故障**：SDK 没编进来 / 私钥没配 / Secret 轮换过，都是 200+ok=false+
//      稳定码；把它们显示成红色 500 会让管理员去提故障单。
//   3. **列表只有摘要、全文在详情**：详情端点是独立父节点 `/admin/channel-archive/records/:id`
//      （gin 同层不允许静态段与 :id 并存），路径写错就是恒 404 的"读不出原文"。
// 网络层统一 mock AUTH，不发真实请求。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

vi.mock('../../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))

import { AUTH } from '../../../lib/api'
import { ChatArchiveTab } from '../ChatArchiveTab'

const authMock = AUTH as ReturnType<typeof vi.fn>

// 三种通道只有企微自建应用有会话存档这条协议；其余必须在下拉里消失
const CHANNELS = {
  list: [
    { id: 3, type: 'wecom_app', name: '门店自建应用', status: 'active' },
    { id: 4, type: 'wecom_kf', name: '微信客服', status: 'active' },
    { id: 5, type: 'wechat_mp', name: '公众号', status: 'active' },
  ],
}

const STATUS = {
  enabled: true, key_configured: true, secret_configured: true, public_key_ver: 2,
  fingerprint: '3F:A1:9B', cursor_seq: 8842, fetcher_ready: false,
  last_msg_at: '2026-09-23T18:04:00Z', stored_total: 1207, failed_total: 3,
  ver_mismatch_total: 1, sdk_reason: 'archive_sdk_not_built',
}

// 列表行**没有 content_text**：正文只以 120 字摘要下发（后端契约），长文只出现在详情
// seq 刻意与 cursor_seq 不同值：两个数字一样时"游标 8842"这条断言就说不清它在读哪一格
const RECORD = {
  id: 91, channel_id: 3, msgid: 'abcDEF123', seq: 8801, public_key_ver: 2,
  biz_type: 'chat', action: 'send', from_user: 'wx_zhang', sender_name: '张顾问',
  chat_type: 'external', chatid: 'wr_chat_1', msg_type: 'text', media_id: '', media_status: '',
  decrypt_error: '', msg_time: '2026-09-23T18:04:00Z',
  text_preview: '您好，这款车的四驱版目前落地价是……（此处为 120 字摘要，全文请点右侧）', has_full_text: true,
}
const RECORD_NO_TEXT = { ...RECORD, id: 92, msg_type: 'image', text_preview: '', has_full_text: false, content_text: '不该出现在列表里的全文' }
const RECORDS = { list: [RECORD, RECORD_NO_TEXT], total: 1207, page: 1, page_size: 20, page_size_cap: 100, note: '列表仅回显正文摘要（120 字），全文请走详情接口——每次读取全文都会写审计。' }

// FIX-H：把原先内联在 route() 里的密钥/详情默认响应提成具名 fixture，
// 断言直接引用字段（版本、指纹、全文正文），不再抄字面量——双改必红。
const KEY_OK = {
  public_key_pem: '-----BEGIN PUBLIC KEY-----\nMIIB\n-----END PUBLIC KEY-----\n',
  fingerprint: '3F:A1:9C', public_key_ver: 3, private_key_echo: false,
}
const DETAIL_OK = {
  record: { ...RECORD, content_text: '全文：四驱版落地价 239800，含三年质保。', to_list: '[{"userid":"wx_li"}]' },
}
// 解不开的留痕（原内联在"解不开"用例里）：钉整句文案与所在行时按字段引用
const RECORD_BROKEN = { ...RECORD, decrypt_error: 'decrypt_failed: key version mismatch', text_preview: '', has_full_text: false }

type RouteOpts = { channels?: unknown; status?: unknown; sync?: unknown; key?: unknown; records?: unknown; detail?: unknown }

/** 按 URL 分流并记录调用（断言"密钥字段到底有没有提交"要看真实 body）。 */
function route(opts: RouteOpts = {}) {
  const calls: Array<{ url: string; method?: string; body?: unknown }> = []
  authMock.mockImplementation(async (url: string, o?: { method?: string; body?: unknown }) => {
    calls.push({ url, body: o?.body, method: o?.method })
    if (url === '/api/v1/admin/channels') return { code: 0, data: opts.channels ?? CHANNELS }
    if (/\/archive\/sync$/.test(url)) return { code: 0, data: opts.sync ?? { ok: true, reason: '', result: { fetched: 25, stored: 21, dup_skipped: 3, stale_skipped: 1, decrypt_failed: 0, max_seq: 8867 } } }
    if (/\/archive\/key$/.test(url)) return { code: 0, data: opts.key ?? KEY_OK }
    if (/\/archive\/records/.test(url)) return { code: 0, data: opts.records ?? RECORDS }
    if (/\/channel-archive\/records\//.test(url)) return { code: 0, data: opts.detail ?? DETAIL_OK }
    if (url.endsWith('/archive') && o?.method === 'PUT') return { code: 0, data: { status: { ...STATUS, enabled: Boolean((o?.body as { enabled?: boolean })?.enabled) } } }
    if (url.endsWith('/archive')) return { code: 0, data: opts.status ?? { status: STATUS } }
    return { code: 0, data: {} }
  })
  return calls
}

/** 点开「通道」下拉并选项。
 * TDesign 的 Select 在 jsdom 下把**值**（通道号）写进 input，不是标签，所以按 placeholder 认通道下拉；
 * 选项只在下拉浮层里找，"门店自建应用"这类文案在状态面板标题上也会重复出现。 */
async function pickChannel(optionText: string) {
  const channelSelects = Array.from(document.querySelectorAll<HTMLInputElement>('input.t-input__inner'))
    .filter((i) => i.placeholder === '请选择')
  // 原来弱在哪：toBeTruthy 只说"找到了一个输入框"，
  // 要是别的 Select 丢了显式 placeholder（列表区两个写死了'消息类型'/'解密状态'，
  // 见 ChatArchiveTab.tsx:368/371），find 会静默开错浮层。
  // 现在钉什么：'请选择'必须恰好命中一个（默认 placeholder 只属于通道下拉，ChatArchiveTab.tsx:99）。
  expect(channelSelects).toHaveLength(1)
  const input = channelSelects[0]
  fireEvent.click(input)
  const opt = await waitFor(() => {
    const el = Array.from(document.querySelectorAll<HTMLElement>('.t-select-option')).find((o) => (o.textContent || '').trim() === optionText)
    // 原来弱在哪：truthy 只证明浮层里有非空元素；现在把命中元素的文本与
    // 期望的通道标签（`${name}（#${id}）`，ChatArchiveTab.tsx:100）做全等复核。
    expect((el?.textContent || '').trim()).toBe(optionText)
    return el as HTMLElement
  })
  fireEvent.click(opt)
}

/** 按文案取操作控件。TDesign 的禁用按钮渲染成 `<div type="button" disabled>`，
 *  所以不能用 getByRole('button') 去找它——那样"禁用"这一格会被当成不存在。 */
function controlsByText(text: string): HTMLElement[] {
  return Array.from(document.querySelectorAll('button, [type="button"]'))
    .filter((el) => (el.textContent || '').trim() === text) as HTMLElement[]
}

beforeEach(() => { authMock.mockReset() })

describe('ChatArchiveTab', () => {
  it('下拉只列企微自建应用，首屏就按选中的通道取状态', async () => {
    route()
    render(<ChatArchiveTab />)
    // 等的是「状态面板已按响应渲染出来」，不是「请求发出去了」。
    // 面板标题（通道名）挂在 status 之后（ChatArchiveTab.tsx:113 `status && <StatusPanel …>`），
    // 而 status 要等 /archive 响应回来才 setState——旧写法 waitFor 命中"调用已发生"就同步读 DOM，
    // 抢在渲染前读到空，本机刚跑完高负载并发抽查时实测 1/3 概率红（同批 QuotaAlertCard、
    // playwright D4 下钻是同一个形态的第三次复发）。
    // 原来弱在哪：findByText + truthy 只证明页面上出现过通道名；
    // 现在钉什么：状态面板标题（ChatArchiveTab.tsx:132 的 span）与'存档已开启'标签
    // （:133，status.enabled=true）同头相邻——名字出现在渲染了 STATUS 数据的面板里才算数，
    // 蹭到别处的重名文案会红。
    const titleEl = await screen.findByText('门店自建应用')
    expect(titleEl.parentElement).toHaveTextContent('门店自建应用存档已开启')
    await waitFor(() => expect(authMock).toHaveBeenCalledWith('/api/v1/admin/channels/3/archive'))
    // 其余通道类型不得出现在界面上（存档是企业微信自建应用专属能力）
    expect(screen.queryByText('微信客服')).toBeNull()
  })

  it('一个企微应用都没有时直说"先去建通道"，不空转一个假下拉', async () => {
    route({ channels: { list: [{ id: 4, type: 'wecom_kf', name: '微信客服', status: 'active' }] } })
    render(<ChatArchiveTab />)
    // 原来弱在哪：正则前缀 + truthy，句尾截断也能过；现在钉 ChatArchiveTab.tsx:95 的整句。
    const hint = await screen.findByText(/还没有企业微信自建应用通道/)
    expect(hint.textContent).toBe('还没有企业微信自建应用通道。请先到「通道接入」创建一个。')
    // 没有通道就不该发存档状态请求（否则会打出一个 /channels//archive 的畸形 URL）
    expect(authMock.mock.calls.some((c) => String(c[0]).includes('/archive'))).toBe(false)
  })

  it('状态面板不出现任何密钥材料，连掩码都不显示', async () => {
    route()
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    const text = document.body.textContent || ''
    expect(text).not.toContain('PRIVATE KEY')
    expect(text).not.toContain('****')
    expect(text).not.toContain('secret')
    // 计数照常摊开：游标/入库/解不开都是运维要看的。
    // 原来弱在哪：truthy 只证明"这个数字出现在页面上"，数字串了列（游标显示到指纹格）也过；
    // 现在钉什么：每个值与自己的标签同格（Cell 结构：label div + value div，ChatArchiveTab.tsx:165-171），
    // 期望值从 STATUS fixture 现取（String(st.cursor_seq)、`${st.stored_total} 条` 的渲染模板见 :148/:150/:147）。
    const cursorCell = screen.getByText(String(STATUS.cursor_seq)).parentElement as HTMLElement
    expect(cursorCell).toHaveTextContent(`拉取游标 seq${STATUS.cursor_seq}`)
    const storedCell = screen.getByText(`${STATUS.stored_total} 条`).parentElement as HTMLElement
    expect(storedCell).toHaveTextContent(`已入库${STATUS.stored_total} 条`)
    const fpCell = screen.getByText(STATUS.fingerprint).parentElement as HTMLElement
    expect(fpCell).toHaveTextContent(`公钥指纹${STATUS.fingerprint}`)
  })

  it('SDK 缺口写成"这不是故障"，而不是红色报错', async () => {
    route()
    render(<ChatArchiveTab />)
    const note = await screen.findByText(/取数组件（企微官方 C SDK）尚未编入/)
    expect(note.className).toContain('text-orange-700')
    // 原来弱在哪：truthy 只证明这串字存在；现在钉它就是状态头部的 warning 标签——
    // 缺口必须"黄色警示"而不是红色报错。注意 TDesign Tag 的实际 DOM：
    // <div class="t-tag t-tag--warning"><span title>文字</span></div>，getByText 命中的是内层 span，
    // 主题类要沿 parentElement 上跳一格才取得到（首跑按 span 直读 classList 被判红，是我推导错了 DOM 结构）。
    const fetcherTag = screen.getByText('取数组件未接入（等 SDK 编译，非故障）').parentElement as HTMLElement
    expect(fetcherTag.classList.contains('t-tag--warning')).toBe(true)
    expect(screen.queryByText(/500/)).toBeNull()
  })

  it('同步失败是 200+ok=false：按稳定码给话术，不伪装成服务崩', async () => {
    const calls = route({ sync: { ok: false, reason: 'archive_disabled', result: { fetched: 0, stored: 0, dup_skipped: 0, stale_skipped: 0, decrypt_failed: 0, max_seq: 0 } } })
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    fireEvent.click(screen.getByRole('button', { name: /立即同步一轮/ }))
    // 原来弱在哪：正则前缀 + truthy，话术被截半也过；现在钉 ARCHIVE_REASON_TEXT.archive_disabled
    // 的整句（ChatArchiveTab.tsx:25），截断/改字必红。
    const note = await screen.findByText(/这个通道还没开启会话存档/)
    expect(note.textContent).toBe('这个通道还没开启会话存档。请先在企微后台完成存档开通与成员授权，再回来打开下面的开关。')
    expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/admin/channels/3/archive/sync')).toBe(true)
  })

  it('同步成功把五个互不重叠的计数与游标都念出来', async () => {
    route()
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    fireEvent.click(screen.getByRole('button', { name: /立即同步一轮/ }))
    const note = await screen.findByText(/本轮取回 25 条/)
    expect(note.textContent).toContain('新入库 21 条')
    expect(note.textContent).toContain('重复跳过 3 条')
    expect(note.textContent).toContain('过旧跳过 1 条')
    expect(note.textContent).toContain('解不开 0 条')
    expect(note.textContent).toContain('游标推进到 8867')
    // 同步后要回读状态（游标已在后端前移）
    await waitFor(() => expect(authMock.mock.calls.filter((c) => c[0] === '/api/v1/admin/channels/3/archive').length).toBeGreaterThan(1))
  })

  it('生成密钥只回公钥；后端一旦回了私钥痕迹，页面拒绝显示', async () => {
    route()
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    fireEvent.click(screen.getByRole('button', { name: '生成密钥对' }))
    fireEvent.click(screen.getByRole('button', { name: '生成' }))
    const box = (await screen.findByDisplayValue(/BEGIN PUBLIC KEY/)) as HTMLTextAreaElement
    expect(box.value).toContain('PUBLIC KEY')
    expect(document.body.textContent).not.toContain('PRIVATE KEY')
    // 原来弱在哪：truthy 只证明"入库"字样出现；现在把成功文案与 KEY_OK 的
    // 版本/指纹逐字绑定（模板见 ChatArchiveTab.tsx:238），回显读数的断言不再成立。
    const okNote = await screen.findByText(/私钥已加密入库/)
    expect(okNote.textContent).toBe(
      `密钥对已生成（第 v${KEY_OK.public_key_ver} 版，指纹 ${KEY_OK.fingerprint}）。私钥已加密入库，页面不会显示它。`,
    )

    // 反证：private_key_echo=true 是后端契约被破坏，页面必须拒显
    route({ key: { public_key_pem: 'PUB', fingerprint: 'AA', public_key_ver: 4, private_key_echo: true } })
    fireEvent.click(screen.getByRole('button', { name: '完成' }))
    fireEvent.click(screen.getByRole('button', { name: '生成密钥对' }))
    fireEvent.click(screen.getByRole('button', { name: '生成' }))
    // 原来弱在哪：正则前缀 + truthy；现在钉拒显整句（ChatArchiveTab.tsx:193）
    const refuse = await screen.findByText(/本页拒绝显示/)
    expect(refuse.textContent).toBe('服务端返回了私钥内容，本页拒绝显示——请立即核查后端改动')
    expect(screen.queryByDisplayValue('PUB')).toBeNull()
  })

  it('配置弹窗：密钥留空=不改动，提交的 body 里不会出现空串把旧值清掉', async () => {
    const calls = route()
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    fireEvent.click(screen.getByRole('button', { name: /配置密钥 \/ Secret/ }))
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url === '/api/v1/admin/channels/3/archive')
      expect(put?.body).toEqual({ enabled: true, public_key_ver: 2 })
    })
    const put = calls.find((c) => c.method === 'PUT')
    expect(JSON.stringify(put?.body)).not.toContain('archive_secret')
    expect(JSON.stringify(put?.body)).not.toContain('private_key_pem')
  })

  it('填了 Secret 才提交，且公钥版本按输入走', async () => {
    const calls = route()
    render(<ChatArchiveTab />)
    await screen.findByText('拉取游标 seq')
    fireEvent.click(screen.getByRole('button', { name: /配置密钥 \/ Secret/ }))
    fireEvent.change(screen.getByPlaceholderText('提交后不可再读取'), { target: { value: '  secret-from-wecom  ' } })
    fireEvent.change(screen.getByDisplayValue('2'), { target: { value: '3' } })
    fireEvent.click(screen.getByRole('button', { name: /保\s*存/ }))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT')
      expect(put?.body).toMatchObject({ archive_secret: 'secret-from-wecom', public_key_ver: 3 })
    })
  })

  it('列表只渲染摘要；读全文走独立父节点端点，且没有全文的行不给点', async () => {
    const calls = route()
    render(<ChatArchiveTab />)
    await screen.findByText('存档留痕')
    // 原来弱在哪：正则前缀 + truthy——摘要被截断或串到别的行都看不出来；
    // 现在钉什么：命中文本与 RECORD.text_preview 逐字全等（getByText 精确串），
    // 并钉这条摘要落在 seq=8801 那一行（正文-行序不串台）。
    const preview = screen.getByText(RECORD.text_preview)
    expect(preview.closest('tr')).toHaveTextContent(String(RECORD.seq))
    // 后端即使在列表里多塞了 content_text，页面也不许把它摊开
    expect(screen.queryByText(/不该出现在列表里的全文/)).toBeNull()

    const btns = controlsByText('读全文')
    expect(btns.length).toBe(2)
    expect(btns[0].hasAttribute('disabled')).toBe(false)
    // 第二条只有信封没有可读正文：按钮必须是"disabled"而不是"消失了"（管理员要能看出这里有过一条）
    expect(btns[1].hasAttribute('disabled')).toBe(true)
    fireEvent.click(btns[0])
    await waitFor(() => expect(calls.some((c) => c.url === '/api/v1/admin/channel-archive/records/91')).toBe(true))
    // 原来弱在哪：两条都是正则前缀 + truthy；现在钉：
    // ① 审计提示整句（ChatArchiveTab.tsx:408，这句话是合规承诺，截半即失义）；
    // ② 全文正文逐字 = DETAIL_OK.record.content_text（fixture 引用，不抄读数），
    //    并且与'正文全文'标签同块（:424-428），确保摊开的是详情正文而不是别处文本。
    const audit = await screen.findByText(/这次查看已记入审计日志/)
    expect(audit.textContent).toBe('这次查看已记入审计日志（谁、看了哪一条）。原文属于客户个人信息，请按最小必要使用。')
    const fullText = screen.getByText(DETAIL_OK.record.content_text)
    expect(fullText.parentElement).toHaveTextContent('正文全文')
    expect(fullText.parentElement).toHaveTextContent(DETAIL_OK.record.content_text)
  })

  it('换通道要整块换数据：列表请求带上新通道号', async () => {
    const calls = route({ channels: { list: [...CHANNELS.list, { id: 9, type: 'wecom_app', name: '二号自建应用', status: 'active' }] } })
    render(<ChatArchiveTab />)
    await screen.findByText('存档留痕')
    await pickChannel('二号自建应用（#9）')
    await waitFor(() => expect(calls.some((c) => c.url.startsWith('/api/v1/admin/channels/9/archive/records'))).toBe(true))
    // 换通道 = 换一整套数据：状态也要按新通道重取
    expect(calls.some((c) => c.url === '/api/v1/admin/channels/9/archive')).toBe(true)
  })

  it('分页回显实际生效的 page_size 与硬顶，不用请求里的原值吹牛', async () => {
    const PAGE100 = { ...RECORDS, page_size: 100, page_size_cap: 100 }
    route({ records: PAGE100 })
    render(<ChatArchiveTab />)
    // 原来弱在哪：三条正则前缀 + truthy，'共 13 页'是抄读数——页数算错（如向下取整）反而测不出。
    // 现在钉什么：整条分页文案逐字全等，页数按 fixture 现算 Math.ceil(total/page_size)
    // （渲染模板见 ChatArchiveTab.tsx:380），后端回显与展示口径绑死；
    // 摘要口径 note 钉在同一分页条里（:382 与分页 span 同父），文本 = fixture note 原样。
    const pager = await screen.findByText(/每页 100 条/)
    expect(pager.textContent).toBe(
      `第 ${PAGE100.page} 页 / 共 ${Math.ceil(PAGE100.total / PAGE100.page_size)} 页 · 每页 ${PAGE100.page_size} 条（硬顶 ${PAGE100.page_size_cap}）`,
    )
    expect(pager.parentElement).toHaveTextContent(PAGE100.note)
  })

  it('解不开的留痕照样占一行并说明原因，游标不会卡在这条上', async () => {
    route({ records: { ...RECORDS, list: [RECORD_BROKEN], total: 1 } })
    render(<ChatArchiveTab />)
    // 原来弱在哪：两条正则前缀各配 truthy，其实测的是同一个 span，各测半句；
    // 现在钉什么：整句逐字全等（渲染模板 ChatArchiveTab.tsx:354，原因码从 fixture 引用），
    // 并钉这条占位留痕落在 seq=8801 那一行——"照样占一行"要占对行。
    const brokenNote = await screen.findByText(`解不开：${RECORD_BROKEN.decrypt_error}（仍占位留痕，游标不会卡在这条）`)
    expect(brokenNote.closest('tr')).toHaveTextContent(String(RECORD_BROKEN.seq))
  })

  it('通道存档没开时如实标注"以下为历史留痕（若有）"', async () => {
    route({ status: { status: { ...STATUS, enabled: false } } })
    render(<ChatArchiveTab />)
    // 原来弱在哪：truthy + 前缀正则；现在钉整句（ChatArchiveTab.tsx:374），
    // 并与状态头部的'存档未开启'标签（:133 enabled=false 分支）互证同一状态。
    const hist = await screen.findByText(/该通道存档未开启/)
    expect(hist.textContent).toBe('该通道存档未开启，以下为历史留痕（若有）')
    expect(screen.getByText('存档未开启')).toBeInTheDocument()
  })
})
