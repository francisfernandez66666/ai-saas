// FIX-D（2026-09-28 审计复核批）：`/super` 补路由级角色闸。
//
// 缺陷现场：`/admin` 一直是 `ProtectedRoute`（只验登录态）+ `RequireRole`（再验角色）两道，
// 而 `/super` 只挂了 `ProtectedRoute`——"是不是超管"完全靠 SuperAdmin 页内读 localStorage 自判。
// 于是深链直达 `/super` 时**页面组件先被实例化**（拆懒加载后还会发 Tab 数据请求），
// 有没有挡住取决于组件内部那几行有没有漏，路由表上根本看不出来——看代码的人以为已经守住了。
//
// 本文件两层，缺一层都守不住：
//   ① 判据本体（RequireRole 组件）：非超管被挡、超管放行，两个方向都测。
//   ② 挂载位（App.tsx 路由表）：读源码断"挂 SuperAdmin 那条 Route 的 element 里真有 RequireRole"。
// 为什么必须有 ②：页内那道兜底判定**和路由闸的可见结果一样**（都是"进不去超管台"）。
// 只测 ① 或只测"渲染后看不到超管台"，把路由闸整行删掉照样绿——兜底闸会把这次删除完全吞掉，
// 于是"两道闸"静默退回一道。这是本项目反复遇到的形态：有自动降级/兜底的链路，
// 「结果看起来对」不算验收判据，必须钉"这一道防线自己确实还在"。
// 源文本取法见下方 ② 组注释（Vite `?raw`，不用 node:fs / import.meta.url，原因写在那儿）。
import appSrc from '../../App.tsx?raw'

import { render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'

import { RequireRole } from '../../components/RequireRole'
import { ADMIN_ROLES, ROLES } from '../../lib/roles'

describe('/super 路由级角色闸（FIX-D）①：判据本体', () => {
  afterEach(() => {
    localStorage.clear()
  })

  /** 按 App.tsx 的真实挂法搭两棵树：/super 只准超管，/admin 准三种管理角色。 */
  function renderAt(entry: string) {
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route
            path="/super"
            element={
              <RequireRole allow={[ROLES.superAdmin]}>
                <div>平台超管台</div>
              </RequireRole>
            }
          />
          <Route
            path="/admin"
            element={
              <RequireRole allow={ADMIN_ROLES}>
                <div>租户后台</div>
              </RequireRole>
            }
          />
          <Route path="/login" element={<div>登录页</div>} />
        </Routes>
      </MemoryRouter>,
    )
  }

  it.each([ROLES.admin, ROLES.tenantAdmin, 'sales', ''])(
    '角色=%s 访问 /super 被挡在路由层，且落点是带 redirect 的登录页',
    (role) => {
      localStorage.setItem('role', role)
      renderAt('/super')
      expect(screen.queryByText('平台超管台')).toBeNull()
      // 落点也要断：静默渲染空白会造成"挡住了"的假象，实际用户看到白屏不知去哪
      expect(screen.getByText('登录页')).toBeInTheDocument()
    },
  )

  it('super_admin 访问 /super 必须放行（正向对照，缺它"谁都进不去"也是绿）', () => {
    localStorage.setItem('role', ROLES.superAdmin)
    renderAt('/super')
    expect(screen.getByText('平台超管台')).toBeInTheDocument()
    expect(screen.queryByText('登录页')).toBeNull()
  })

  it('两处允许集合确实不同：租户 admin 进得了 /admin、进不了 /super', () => {
    localStorage.setItem('role', ROLES.admin)
    renderAt('/admin')
    expect(screen.getByText('租户后台')).toBeInTheDocument()
    expect(screen.queryByText('平台超管台')).toBeNull()
  })
})

describe('/super 路由级角色闸（FIX-D）②：挂载位在路由表上（防兜底闸吞掉删除）', () => {
  // 路由表这一行有没有闸是**结构事实**，按字面断最稳。源文本走 Vite 的 `?raw`（见文件顶部说明）：
  // 本仓 tsc 没有 @types/node，`readFileSync` 会 TS2307 卡住契约层；`import.meta.url` 在 jsdom 下
  // 又不是 file: 协议（`new URL(..., import.meta.url)` 直接抛错，本文件首跑就是这么死的）。
  const routeLine = (path: string) =>
    appSrc.split('\n').find((l) => l.includes(`path="${path}"`) && l.includes('element='))

  it('检查器自证：确实读到了 App 本体（读空/读错文件时下面的红是检查器坏，不是路由坏）', () => {
    expect(appSrc.length, 'App.tsx 源文本为空——?raw 解析没生效').toBeGreaterThan(500)
    expect(appSrc).toContain('export default function App')
    expect(appSrc).toContain("path=\"/super\"")
  })

  it('/super 那条 Route 的 element 里挂着 RequireRole，且允许集合是超管单角色', () => {
    const line = routeLine('/super')
    expect(line, '路由表里找不到 path="/super" 这条 Route（改名或挪走了，本闸失去意义）').toBeDefined()
    expect(line).toContain('RequireRole')
    expect(line).toContain('ROLES.superAdmin')
    // 挂的是 SuperAdmin 本体——防"RequireRole 包了个别的东西"这种错位绿
    expect(line).toContain('SuperAdmin')
  })

  it('/admin 那条 Route 同样挂着 RequireRole（两条必须成对在场，改一条时另一条会被想起）', () => {
    const line = routeLine('/admin')
    expect(line, '路由表里找不到 path="/admin" 这条 Route').toBeDefined()
    expect(line).toContain('RequireRole')
    expect(line).toContain('ADMIN_ROLES')
  })

  it('反证自证：公开路由 /pricing 确实无闸（同一判据会说"没有"）', () => {
    // 缺这条，`expect(line).toContain('RequireRole')` 会在一个恒真的匹配器上假绿
    // （比如判据写成"整个文件里出现过 RequireRole"，任何一行有都算过）。
    const line = routeLine('/pricing')
    expect(line, '路由表里找不到 path="/pricing" 这条 Route').toBeDefined()
    expect(line).not.toContain('RequireRole')
  })

  it('反证自证：把 /super 那行的闸包裹剥掉，同一判据必须立刻判为"无闸"', () => {
    // 上面那条证"会说没有"，这条证**说的那个"没有"就是删掉闸的形状**——
    // 等价于对本文件头描述的失效形态（有人把路由闸整行删掉、页内兜底仍挡住）做一次内存变异，
    // 而不去真改 App.tsx（生产码变异会被拦，且没有必要：判据的判别力在这里已经钉死）。
    const line = routeLine('/super')
    expect(line, '路由表里找不到 path="/super" 这条 Route').toBeDefined()
    const stripped = String(line)
      .replace('<RequireRole allow={[ROLES.superAdmin]}>', '')
      .replace('</RequireRole>', '')
    expect(stripped).not.toContain('RequireRole')
    expect(stripped).toContain('SuperAdmin') // 剥掉闸后页面本体还在：渲染层看不出差别，正是必须靠结构断言的理由
  })
})
