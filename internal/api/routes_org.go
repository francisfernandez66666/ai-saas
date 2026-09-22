// 组织架构 + CDP 路由（D2a，2026-09-12）
package api

import (
	"github.com/gin-gonic/gin"

	"ai-scrm/internal/middleware"
)

// registerOrg 组织架构管理（四级用户体系）：tenant_admin 全租户；dept_admin 本子树。
func registerOrg(v1 *gin.RouterGroup) {
	orgGroup := v1.Group("/org")
	orgGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(),
		middleware.MustChangePasswordGuard(), middleware.ReadonlyWriteGuard(), OrgManageRequired())
	// 注册期鉴权记录：组织架构管理需登录 + 组织管理闸（整组共享）。
	RecordAuth("*", "/api/v1/org", "jwt", "org_manage")
	{
		orgGroup.GET("/departments/tree", GetDepartmentTree)
		orgGroup.POST("/departments", CreateDepartment)
		orgGroup.PUT("/departments/:id", UpdateDepartment)
		orgGroup.DELETE("/departments/:id", DeleteDepartment)
		orgGroup.GET("/users", GetManagedUsers)
		orgGroup.POST("/users", CreateUser)
		orgGroup.PUT("/users/:id", UpdateUser)
	}
}

// registerCDP CDP OpenAPI 只读出口（Phase A）：服务端二次校验租户归属 + 字段脱敏 + 只查不写。
func registerCDP(v1 *gin.RouterGroup) {
	cdpGroup := v1.Group("/cdp")
	cdpGroup.Use(middleware.JWTAuth(), middleware.TenantConsistency(), middleware.OrgResolve(), middleware.MustChangePasswordGuard())
	// 注册期鉴权记录：CDP 只读出口需登录（整组共享）。
	RecordAuth("*", "/api/v1/cdp", "jwt")
	{
		cdpGroup.GET("/profiles/:one_id", GetCDPProfile)
		cdpGroup.GET("/segments", GetCDPSegment)
		cdpGroup.GET("/tag-defs", ListCDPTagDefs)
	}
}
