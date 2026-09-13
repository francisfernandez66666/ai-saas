// C6 前端异常采集纯逻辑测试（2026-09-13）
// 覆盖：指纹去重、普通/关键错误采样、payload 截断组装、fetch 上报请求。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  buildClientErrorPayload,
  errorFingerprint,
  reportClientError,
  shouldReportError,
} from '../errorReport'

beforeEach(() => {
  sessionStorage.clear()
  localStorage.clear()
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('error fingerprint', () => {
  it('错误对象按 message 指纹化', () => {
    expect(errorFingerprint(new Error('boom'))).toBe('boom')
  })

  it('非错误对象转字符串', () => {
    expect(errorFingerprint({ x: 1 })).toBe('[object Object]')
  })
})

describe('error sampling', () => {
  it('普通错误按随机数采样，同一错误会话内不重复上报', () => {
    expect(shouldReportError(new Error('normal crash'), '/client', () => 0.05)).toBe(true)
    expect(shouldReportError(new Error('normal crash'), '/client', () => 0.05)).toBe(false)
  })

  it('普通错误未命中采样率时丢弃', () => {
    expect(shouldReportError(new Error('normal crash'), '/client', () => 0.95)).toBe(false)
  })

  it('关键路径错误即使随机数超阈值也全量上报', () => {
    expect(shouldReportError(new Error('payment failed'), '/billing', () => 0.95)).toBe(true)
  })
})

describe('payload', () => {
  beforeEach(() => {
    delete (window as any).location
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { pathname: '/admin', href: 'https://example.com/admin?tab=channels' },
    })
  })

  it('stack 与 componentStack 合并', () => {
    const payload = buildClientErrorPayload(new Error('render crash'), '/admin', '  at Admin  ')
    expect(payload.message).toBe('render crash')
    expect(payload.stack).toContain('render crash')
    expect(payload.stack).toContain('at Admin')
    expect(payload.route).toBe('/admin')
    expect(payload.app).toBe('desktop')
  })

  it('移动端异常上报标记 app=mobile', () => {
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { pathname: '/app/settings', href: 'https://example.com/app/settings' },
    })
    const payload = buildClientErrorPayload(new Error('mobile crash'), '/app/settings', '  at AppSettings  ')
    expect(payload.app).toBe('mobile')
  })
})

describe('reportClientError', () => {
  it('采样命中后 POST 到 /client-errors', async () => {
    const fetchMock = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(Math, 'random').mockReturnValue(0.05)
    await reportClientError(new Error('network failed'), '/client', '')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('/api/v1/client-errors')
    expect(init.method).toBe('POST')
    expect(JSON.parse(String(init.body)).message).toBe('network failed')
  })

  it('采样未命中时不发请求', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(Math, 'random').mockReturnValue(0.95)
    await reportClientError(new Error('normal ignored'), '/client', '')
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
