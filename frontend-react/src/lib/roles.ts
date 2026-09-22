// 前端角色单一事实源（P2-8a）
// 枚举值严格对齐 Go 后端 internal/model/user.go 的 RoleXxx 常量，勿凭空新增或改动字符串，
// 否则会与后端 middleware/auth.go 的 AdminRequired / SuperRequired 集合失配，导致前端显示与后端 403 不一致。
// 注意：后端 RoleSales 是 RoleUser("user") 的兼容别名，前端统一用 ROLES.user（"user"），不再使用 "sales" 字面量。

/** 用户角色枚举：字符串字面量集合，与后端 model.RoleXxx 一一对应。 */
export const ROLES = {
  /** 平台超管：可访问 /super 平台后台，对租户作用域路径需显式 X-Tenant-ID。 */
  superAdmin: 'super_admin',
  /** 租户管理员：租户级全量管理权限（AdminRequired 集合之一）。 */
  tenantAdmin: 'tenant_admin',
  /** 传统管理员：兼容旧数据的管理员角色（AdminRequired 集合之一）。 */
  admin: 'admin',
  /** 部门管理员：仅管辖本部门子树，无 /admin 平台接口权限。 */
  deptAdmin: 'dept_admin',
  /** 只读成员：仅查看，无写权限。 */
  readonly: 'readonly',
  /** 普通成员（含销售岗）：仅本人数据，对应后端 RoleUser("user")。 */
  user: 'user',
} as const

/** 具备后台管理权限的角色集合（可访问 /admin 收银台、配置等）：超管 / 租户管理员 / 传统管理员。
 *  对齐后端 middleware/auth.go 的 AdminRequired 集合（不含 dept_admin——部门管理员无 /admin 接口权限）。 */
export const ADMIN_ROLES: string[] = [ROLES.superAdmin, ROLES.tenantAdmin, ROLES.admin]

/** 可启停成员 / 操作组织用户的角色集合：超管、租户管理员、部门管理员。 */
export const USER_OP_ROLES: string[] = [ROLES.superAdmin, ROLES.tenantAdmin, ROLES.deptAdmin]

/** 可移动部门的角色集合（租户级）：超管、租户管理员。 */
export const DEPT_MOVE_ROLES: string[] = [ROLES.superAdmin, ROLES.tenantAdmin]

/** 判断给定角色是否管理岗（可访问管理后台与收银台入口）。 */
export function isStaff(role: string): boolean {
  return ADMIN_ROLES.includes(role)
}

/** 判断给定角色是否平台超管（仅 super_admin 可访问 /super 平台后台）。 */
export function isSuperAdmin(role: string): boolean {
  return role === ROLES.superAdmin
}
