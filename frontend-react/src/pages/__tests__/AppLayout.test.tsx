// P1-8 角色矩阵冒烟（2026-09-20 审计批）：/app 顶栏「收银台」入口的显隐必须与后端
// AdminRequired 同集合（super_admin/tenant_admin/admin）——dept_admin/sales 均不可见
// （收银台下单/支付走 AdminRequired，看到入口点进去只会 403）。
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import AppLayout from '../AppLayout'
import { I18nProvider } from '../../i18n/I18nProvider'

// 以指定角色/语言渲染 /app 布局壳（token 恒有值，聚焦角色过滤分支）。
// P2-14：必须显式钉 scrm_lang——jsdom 的 navigator.language 默认 en-US，
// 不钉语言的话 detectLanguage 会走浏览器偏好把界面渲染成英文，中文断言全漂。
function renderAsRole(role: string, lang = 'zh-CN') {
  localStorage.setItem('scrm_auth_token', 'tk_test')
  localStorage.setItem('role', role)
  localStorage.setItem('scrm_lang', lang)
  return render(
    <MemoryRouter initialEntries={['/app']}>
      <I18nProvider>
        <AppLayout />
      </I18nProvider>
    </MemoryRouter>,
  )
}

// 原来弱在哪：getByText('收银台') 配 truthy 只证明"有这三个字"，不证明它是那条
// 导航链接——标题文案、别的说明文字都能喂绿。现在钉：label 所在 <a> 的 href
// 与 AppLayout.tsx link(to,label) 的 to 一一对应（/app/billing、/app/advisor、/app/referral）。
function expectLink(label: string, href: string) {
  const el = screen.getByText(label)
  expect(el).toHaveTextContent(label)
  expect(el.closest('a')).toHaveAttribute('href', href)
}

describe('AppLayout 角色导航矩阵（P1-8）', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  // 管理岗三角色：收银台入口可见（与后端 AdminRequired 认同一集合）
  it.each(['super_admin', 'tenant_admin', 'admin'])('%s 可见收银台入口', (role) => {
    renderAsRole(role)
    expectLink('收银台', '/app/billing')
  })

  // dept_admin 后端不认管理员 → 入口不再显示（P1-8 修复点，旧实现恒显示）
  it('dept_admin 不显示收银台入口（后端 AdminRequired 不认该角色）', () => {
    renderAsRole('dept_admin')
    expect(screen.queryByText('收银台')).toBeNull()
    // 其余所有登录成员可见的入口不受影响
    expectLink('顾问台', '/app/advisor')
    expectLink('邀请', '/app/referral')
  })

  it('sales 成员：有顾问台/邀请，无收银台', () => {
    renderAsRole('sales')
    expectLink('顾问台', '/app/advisor')
    expectLink('邀请', '/app/referral')
    expect(screen.queryByText('收银台')).toBeNull()
  })
})

// P2-14：语言切换经 localStorage 键驱动全站文案——切到 en-US 后同一批导航链
// 必须变英文且 href 不变（证明 t() 换的是文案不是路由）
describe('AppLayout 语言切换（P2-14）', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  it('en-US 下顶栏导航变英文、href 不变', () => {
    renderAsRole('admin', 'en-US')
    expectLink('Billing', '/app/billing')
    expectLink('Advisor', '/app/advisor')
    expectLink('Invite', '/app/referral')
    expectLink('Settings', '/app/settings')
    expect(screen.queryByText('收银台')).toBeNull()
    expect(screen.queryByText('顾问台')).toBeNull()
  })

  it('zh-CN 下切回中文（默认语言仍为中文，回归防漂）', () => {
    renderAsRole('admin', 'zh-CN')
    expectLink('收银台', '/app/billing')
    expect(screen.queryByText('Billing')).toBeNull()
  })
})
