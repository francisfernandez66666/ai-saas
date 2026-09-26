// FIX-6 验收（2026-09-27）：会话校验失败形态与守卫落点必须一一对应。
//
// 钉的是这条真实投诉路径：用户在地铁上/断网时刷新后台，被甩进 **/login?mcp=1（强制改密表单）**，
// 以为自己号出了问题或被安全策略踢了——而真相反倒没人能解释。旧守卫用
// "verifySession 返回 null 且本地仍有 token" 反推原因，于是"网络压根没通"（不清 token 的那条）
// 被推成了"服务端要求改密"。失败形态不同的东西返回值就必须不同形，落点才可能不同。
//
// 这里测的是守卫的**分支落点**（渲染层），verifySession 自身的六种返回形态在
// src/lib/__tests__/api.test.ts 里逐条钉；两层分开，才不会"改了返回值、忘了改 UI"还能全绿。
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { ProtectedRoute } from '../../App'
import { getToken, invalidateSession, setToken } from '../../lib/api'

// 登录页与受保护内容各放一句可断言的文本；URL 用探针组件读真实路由状态
// （断"没被甩走"要看路由，看 window.location 在 MemoryRouter 下永远是假的）
function LocationProbe() {
  const l = useLocation()
  return <span data-testid="url">{l.pathname + l.search}</span>
}

/** 挂一条 /admin（受保护）+ /login 的最小路由树，断言守卫把人留在了哪儿。 */
function renderGuard() {
  return render(
    <MemoryRouter initialEntries={['/admin']}>
      <Routes>
        <Route
          path="/admin"
          element={
            <>
              <LocationProbe />
              <ProtectedRoute>
                <div>后台机密内容</div>
              </ProtectedRoute>
            </>
          }
        />
        <Route
          path="/login"
          element={
            <>
              <LocationProbe />
              <div>登录页</div>
            </>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

// 自造的 /auth/me 响应替身：verifySession 只读 status / json() / clone().json()，
// 不依赖 Response 类（jsdom 下不保证有），故按"它实际读什么就给什么"造最小对象。
function meResponse(status: number, body: unknown) {
  return {
    status,
    json: () => Promise.resolve(body),
    clone: () => ({ json: () => Promise.resolve(body) }),
  } as unknown as Response
}

afterEach(() => {
  // meCache 是模块级 60s 缓存：不清的话下一条用例会命中上一条的身份，断言打的就不是本例分支
  invalidateSession()
  // stubGlobal 不受 restoreAllMocks 管，必须显式撤，否则 fetch 替身会漏进别的用例文件之外
  vi.unstubAllGlobals()
})

describe('ProtectedRoute 会话失败分支落点（FIX-6）', () => {
  it('fetch 直接 reject（断网）：留在原页 + 出现重试条，不跳 /login?mcp=1，token 不被清', async () => {
    setToken('jwt-still-valid')
    const fetchMock = vi.fn(() => Promise.reject(new TypeError('Failed to fetch')))
    vi.stubGlobal('fetch', fetchMock)

    renderGuard()
    // 首屏是"校验中"空白占位，故用 findBy 等分支落地
    expect(await screen.findByText(/网络异常/)).toBeTruthy()
    expect(screen.getByRole('button', { name: '重试' })).toBeTruthy()
    // URL 未变——本条验收的正身：旧写法在这里已经是 /login?mcp=1
    expect(screen.getByTestId('url').textContent).toBe('/admin')
    expect(screen.queryByText('登录页')).toBeNull()
    expect(screen.queryByText('后台机密内容')).toBeNull()
    // 会话没被判死 → token 必须原样留着（清掉就把一次断网升级成"你得重新登录"）
    expect(getToken()).toBe('jwt-still-valid')
    // 反证式核对：分支确实由 fetch 抛错驱动，不是用例自己摆出来的静态面板
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
  })

  it('点「重试」且网络恢复：原地放行受保护内容，全程没离开 /admin', async () => {
    setToken('jwt-still-valid')
    let online = false
    vi.stubGlobal('fetch', vi.fn(() => {
      if (!online) return Promise.reject(new TypeError('Failed to fetch'))
      return Promise.resolve(meResponse(200, { code: 0, data: { id: 1, username: 'admin', role: 'admin', tenant_id: 1 } }))
    }))

    renderGuard()
    await screen.findByText(/网络异常/)
    online = true
    fireEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByText('后台机密内容')).toBeTruthy()
    expect(screen.getByTestId('url').textContent).toBe('/admin')
  })

  it('403 且非改密原因（租户停用/权限）：原地提示，不销毁会话、不进改密表单', async () => {
    setToken('jwt-still-valid')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResponse(403, { code: 403, error_code: 'tenant_suspended' }))))

    renderGuard()
    expect(await screen.findByText(/暂时无法访问该页面/)).toBeTruthy()
    expect(screen.getByTestId('url').textContent).toBe('/admin')
    expect(screen.queryByRole('button', { name: '重试' })).toBeNull() // 重试只对网络异常有意义
    expect(getToken()).toBe('jwt-still-valid')
  })

  it('403 + must_change_password：这才是唯一该进 /login?mcp=1 的形态', async () => {
    setToken('jwt-still-valid')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResponse(403, { code: 403, error_code: 'must_change_password' }))))

    renderGuard()
    expect(await screen.findByText('登录页')).toBeTruthy()
    expect(screen.getByTestId('url').textContent).toBe('/login?mcp=1')
    // 改密接口要带这枚 token，跳过去的路上不能把它清掉
    expect(getToken()).toBe('jwt-still-valid')
  })

  it('401（服务端判死）：清 token 并带 redirect 回登录页', async () => {
    setToken('jwt-expired')
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(meResponse(401, { code: 401, error_code: 'token_expired' }))))

    renderGuard()
    expect(await screen.findByText('登录页')).toBeTruthy()
    expect(screen.getByTestId('url').textContent).toBe('/login?redirect=%2Fadmin')
    expect(getToken()).toBe('')
  })
})
