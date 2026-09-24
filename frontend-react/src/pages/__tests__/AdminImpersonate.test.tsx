// 超管「代管租户」选择器单测（2026-09-24 残项收口）。
//
// 这块的历史缺陷是：/super/tenants 只有分页没有检索，下拉拿的是后端缺省首页（20 家），
// 清库后承载历史数据的种子租户排在最新若干家之外，超管在界面上**再也代管不到它**——
// e2e 当时靠直接写 localStorage 绕过。所以断言集中在三件事：
//   1. 一次问够候选（page_size=100），并且关键字真的进查询串（q=）而不是在前端挑
//   2. 换一批搜索结果，当前代管那一家必须还挂在下拉里——否则下拉显示空白，
//      看着像"代管被取消了"，而请求还在往那个租户发 X-Tenant-ID（这才是危险的错位）
//   3. 刷新后 localStorage 里只有一个裸 ID，页面要按 ID 回查出名字，而不是让人重选一次
// 网络层打桩 fetch（走真实 lib/api），形态对齐后端真实信封。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import Admin from '../Admin'
import { IMPERSONATE_TENANT_KEY, TOKEN_KEY } from '../../lib/api'

// jsdom 缺失的浏览器 API 垫片：useIsMobile 触达 matchMedia，TDesign 表格触达 ResizeObserver
if (!window.matchMedia) {
  Object.defineProperty(window, 'matchMedia', {
    configurable: true,
    value: (q: string) => ({
      matches: false, media: q, onchange: null,
      addListener: () => {}, removeListener: () => {},
      addEventListener: () => {}, removeEventListener: () => {}, dispatchEvent: () => false,
    }),
  })
}
if (!('ResizeObserver' in globalThis)) {
  ;(globalThis as Record<string, unknown>).ResizeObserver = class {
    observe() {} unobserve() {} disconnect() {}
  }
}

const OLD_TENANT = { id: 7, name: '极石汽车', code: 'rox' }
const NEW_TENANT = { id: 99, name: '新登记的店', code: 'newshop' }

const envelope = (list: unknown[], total?: number) => ({
  ok: true, status: 200,
  json: async () => ({ code: 0, data: { list, total: total ?? list.length, page: 1, page_size: 100 } }),
})
const plain = (data: unknown) => ({ ok: true, status: 200, json: async () => ({ code: 0, data }) })

/** 按 URL 分流打桩；返回捕获到的请求 URL 列表，供"关键字有没有真进查询串"断言。 */
function route(tenantsFor: (q: string | null) => typeof OLD_TENANT[]) {
  const urls: string[] = []
  globalThis.fetch = vi.fn(async (input: RequestInfo | URL) => {
    const url = String(input)
    urls.push(url)
    if (url.includes('/super/tenants')) {
      const raw = new URL('http://x' + url.replace(/^https?:\/\/[^/]+/, '')).searchParams.get('q')
      return envelope(tenantsFor(raw))
    }
    return plain([]) // /admin/config 等一律空态
  }) as unknown as typeof fetch
  return urls
}

beforeEach(() => {
  localStorage.clear()
  localStorage.setItem(TOKEN_KEY, 'jwt-super')
  localStorage.setItem('role', 'super_admin')
  localStorage.setItem('username', 'admin')
})

describe('Admin 代管租户选择器', () => {
  it('首屏一次问够候选，不带 q（不再吃后端缺省的 20 家首页）', async () => {
    const urls = route(() => [NEW_TENANT])
    render(<Admin />)
    await screen.findByText('新登记的店（newshop）')
    const call = urls.find((u) => u.includes('/super/tenants')) || ''
    expect(call).toContain('page_size=100')
    expect(call).not.toContain('q=')
  })

  it('关键字进查询串由服务端检索，下拉只列命中的那几家', async () => {
    const urls = route((q) => (q === '极石' ? [OLD_TENANT] : [NEW_TENANT, OLD_TENANT]))
    render(<Admin />)
    await screen.findByRole('option', { name: '新登记的店（newshop）' })

    fireEvent.change(screen.getByLabelText('搜索租户'), { target: { value: '极石' } })
    fireEvent.click(screen.getByRole('button', { name: '搜索' }))
    await waitFor(() => expect(urls.some((u) => u.includes('q=%E6%9E%81%E7%9F%B3'))).toBe(true))
    // 命中结果把候选收窄：未命中的那家不该还挂在下拉里当"可选项"
    const after = await screen.findByRole('option', { name: '极石汽车（rox）' })
    expect(after).toBeTruthy()
    expect(screen.queryByRole('option', { name: '新登记的店（newshop）' })).toBeNull()
  })

  it('换一批搜索结果，当前代管那一家仍留在下拉里（不清空成"看起来没在管谁"）', async () => {
    let qResult: typeof OLD_TENANT[] = [OLD_TENANT, NEW_TENANT]
    route((q) => (q === '新登记' ? [NEW_TENANT] : qResult))
    render(<Admin />)
    const sel = (await screen.findByRole('option', { name: '极石汽车（rox）' })).closest('select') as HTMLSelectElement
    fireEvent.change(sel, { target: { value: '7' } })
    await waitFor(() => expect(localStorage.getItem(IMPERSONATE_TENANT_KEY)).toBe('7'))

    qResult = [NEW_TENANT]
    fireEvent.change(screen.getByLabelText('搜索租户'), { target: { value: '新登记' } })
    fireEvent.click(screen.getByRole('button', { name: '搜索' }))
    await screen.findByRole('option', { name: '新登记的店（newshop）' })
    // 搜索结果里已经没有 7 了，但代管上下文还在生效——下拉必须仍显示它
    expect(sel.value).toBe('7')
    expect(Array.from(sel.options).some((o) => o.text === '极石汽车（rox）')).toBe(true)
  })

  it('刷新后 localStorage 里只有 ID：按 ID 回查把名字补回下拉', async () => {
    localStorage.setItem(IMPERSONATE_TENANT_KEY, '7')
    const urls = route((q) => (q === '7' ? [OLD_TENANT] : [NEW_TENANT]))
    render(<Admin />)
    // 首页看不到 7 号——旧版本到此就再也选不回来了
    await waitFor(() => expect(urls.some((u) => u.includes('q=7'))).toBe(true))
    const sel = (await screen.findByRole('option', { name: '极石汽车（rox）' })).closest('select') as HTMLSelectElement
    await waitFor(() => expect(sel.value).toBe('7'))
  })
})
