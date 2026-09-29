// 移动端邀请推广页的链接单点回归（FIX-12，2026-09-29 端到端审计批）。
// 旧写法在后端没回 invite_url 时本地拼 `location.origin + '/register?ref='`：
// 白标/换域后服务端 invite_url 已指向新域，而本地兜底仍拼当前页 origin，
// 复制出去的是一条"看着能用、归因打在旧域"的链接——这种错位没人会报错。
// 本文件钉两件事：
// ① 后端给了 invite_url → 页面链接与剪贴板内容都必须**逐字**是后端那个值；
// ② 后端没给 → 页面上不许出现任何可点的邀请链接（空 href 点击等于跳回本页，
//    看起来"有链接"其实哪也去不了），文案如实说"生成中"、复制按钮禁用，
//    且**绝不回落到 location.origin**（jsdom 的 origin 是 http://localhost:3000/，
//    一旦回落到它，用例②里的"无链接"断言立刻红）。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import AppReferral from '../AppReferral'

vi.mock('../../lib/api', () => ({
  AUTH: vi.fn(),
  apiFetch: vi.fn(),
  getToken: () => 'test-token',
}))
vi.mock('../../lib/branding', () => ({
  useBrand: () => ({ brandName: 'AI-SCRM', logoUrl: '', primaryColor: '', secondaryColor: '' }),
}))

import { AUTH, apiFetch } from '../../lib/api'
const authMock = AUTH as ReturnType<typeof vi.fn>
const fetchMock = apiFetch as ReturnType<typeof vi.fn>

// 剪贴板桩：jsdom 不提供 navigator.clipboard，组件走可选链会静默跳过，
// 那样"复制到了什么"就无从断言——必须显式装一个可计数的 writeText。
let writeText: ReturnType<typeof vi.fn>
beforeEach(() => {
  localStorage.clear()
  localStorage.setItem('role', 'user')
  authMock.mockReset()
  fetchMock.mockReset()
  writeText = vi.fn()
  Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
  // 二维码与记录列表非本用例关注面，给最小成功桩即可（blob 走 jsdom 占位）
  fetchMock.mockResolvedValue({ ok: false, blob: async () => ({}) })
})

/** 以指定 invite_url 响应渲染页面（undefined = 后端字段缺失，模拟旧响应形态） */
async function renderWith(inviteUrl?: string) {
  authMock.mockImplementation(async (url: string) => {
    if (url.includes('/referral/info')) {
      return { code: 0, data: { referral: { invite_code: 'AB12CD', invited_count: 2, paid_count: 1 }, ...(inviteUrl ? { invite_url: inviteUrl } : {}) } }
    }
    return { code: 0, data: { list: [] } }
  })
  render(<AppReferral />)
  await waitFor(() => expect(authMock).toHaveBeenCalledWith(expect.stringContaining('/api/v1/advisor/referral/info')))
}

describe('AppReferral 邀请链接〔只信后端 invite_url〕', () => {
  const SERVER_URL = 'https://crm.example-acme.com/register?ref=AB12CD'

  it('后端给了链接：展示与复制都用服务端那一份，逐字不回落到本页 origin', async () => {
    await renderWith(SERVER_URL)
    const link = await screen.findByText(SERVER_URL)
    expect(link.tagName).toBe('A')
    // 反向半边：jsdom 的 location.origin 是 http://localhost:3000，
    // 一旦 href 是本地拼的，这条等值断言就红（弱断言 toContain('register') 会放过去）
    expect(link).toHaveAttribute('href', SERVER_URL)
    const btn = screen.getByRole('button', { name: '复制邀请链接' })
    expect(btn).toBeEnabled()
    fireEvent.click(btn)
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SERVER_URL))
  })

  it('后端没给链接：不渲染可点锚点、复制按钮禁用，且不回落到 location.origin', async () => {
    await renderWith(undefined)
    // 空态如实说"生成中"，而不是给一个 href='' 的假链接
    expect(await screen.findByText('生成中…')).toBeTruthy()
    expect(document.querySelector('a[href]')).toBeNull()
    // 字面锁：本地兜底那串一旦回来，这里必红
    expect(document.body.textContent).not.toContain('localhost:3000/register')
    const btn = screen.getByRole('button', { name: '复制邀请链接' })
    expect(btn).toBeDisabled()
    fireEvent.click(btn)
    expect(writeText).not.toHaveBeenCalled()
  })
})
