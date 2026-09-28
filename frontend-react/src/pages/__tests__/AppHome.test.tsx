// G-23(2026-09-25) 移动端首页角色分流冒烟：/app 六卡片里「收银台」的显隐必须与
// AppLayout 顶栏、后端 AdminRequired 用同一判据（super_admin/tenant_admin/admin）。
// 三处不同集合会得到同一种用户可感知故障：顶栏没有、首页卡片却有，点进去是禁用态页面。
import { render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import AppHome from '../AppHome'

// 以指定角色渲染首页（role 走 localStorage，与 AppLayout/AppSettings 同一读取口径）
function renderAsRole(role: string) {
  localStorage.setItem('scrm_auth_token', 'tk_test')
  localStorage.setItem('role', role)
  return render(<AppHome />)
}

// 原来弱在哪：getByText(/收银台/) 配 truthy 只证明页面上有"收银台"三个字——
// 挂在哪个卡片、跳哪儿都不管。现在钉：h3 全文（图标+标题，AppHome.tsx 渲染为
// `{icon} {title}`）+ 所属 <a> 的 href（与 cards 数组的 to 字段一一对应）。
function expectCard(title: string, href: string) {
  const heading = screen.getByText(title)
  expect(heading).toHaveTextContent(title)
  expect(heading.closest('a')).toHaveAttribute('href', href)
}

describe('AppHome 卡片角色分流（G-23）', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  it.each(['super_admin', 'tenant_admin', 'admin'])('%s 可见收银台卡片', (role) => {
    renderAsRole(role)
    expectCard('💰 收银台', '/app/billing')
  })

  it('成员角色（user/sales/dept_admin/readonly）不显示收银台，其余入口保留', () => {
    for (const role of ['user', 'sales', 'dept_admin', 'readonly']) {
      const { unmount } = renderAsRole(role)
      expect(screen.queryByText(/收银台/)).toBeNull()
      // 顾问台/邀请是销售岗核心工作台与个人资产，不得随收银台一起藏掉
      expectCard('🧑‍💼 顾问台', '/app/advisor')
      expectCard('🎁 邀请推广', '/app/referral')
      expectCard('⚙️ 账号设置', '/app/settings')
      unmount()
    }
  })

  it('未登录访客：注册漏斗入口保留（点进去由布局守卫甩到登录页），仅管理岗入口不显示', () => {
    render(<AppHome />)
    expect(screen.queryByText(/收银台/)).toBeNull()
    expectCard('💬 客户对话', '/app/chat')
    expectCard('🧾 定价', '/pricing')
  })
})
