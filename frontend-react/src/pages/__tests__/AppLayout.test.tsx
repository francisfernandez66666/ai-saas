// P1-8 角色矩阵冒烟（2026-09-20 审计批）：/app 顶栏「收银台」入口的显隐必须与后端
// AdminRequired 同集合（super_admin/tenant_admin/admin）——dept_admin/sales 均不可见
// （收银台下单/支付走 AdminRequired，看到入口点进去只会 403）。
import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import AppLayout from '../AppLayout'

// 以指定角色渲染 /app 布局壳（token 恒有值，聚焦角色过滤分支）
function renderAsRole(role: string) {
  localStorage.setItem('scrm_auth_token', 'tk_test')
  localStorage.setItem('role', role)
  return render(
    <MemoryRouter initialEntries={['/app']}>
      <AppLayout />
    </MemoryRouter>,
  )
}

describe('AppLayout 角色导航矩阵（P1-8）', () => {
  beforeEach(() => localStorage.clear())
  afterEach(() => localStorage.clear())

  // 管理岗三角色：收银台入口可见（与后端 AdminRequired 认同一集合）
  it.each(['super_admin', 'tenant_admin', 'admin'])('%s 可见收银台入口', (role) => {
    renderAsRole(role)
    expect(screen.getByText('收银台')).toBeTruthy()
  })

  // dept_admin 后端不认管理员 → 入口不再显示（P1-8 修复点，旧实现恒显示）
  it('dept_admin 不显示收银台入口（后端 AdminRequired 不认该角色）', () => {
    renderAsRole('dept_admin')
    expect(screen.queryByText('收银台')).toBeNull()
    // 其余所有登录成员可见的入口不受影响
    expect(screen.getByText('顾问台')).toBeTruthy()
    expect(screen.getByText('邀请')).toBeTruthy()
  })

  it('sales 成员：有顾问台/邀请，无收银台', () => {
    renderAsRole('sales')
    expect(screen.getByText('顾问台')).toBeTruthy()
    expect(screen.getByText('邀请')).toBeTruthy()
    expect(screen.queryByText('收银台')).toBeNull()
  })
})
