package middleware

import (
	"net/http"

	"ai-scrm/internal/db"
	"ai-scrm/internal/service"

	"github.com/gin-gonic/gin"
)

// ============================================================
// 组织上下文中间件（四级用户体系）
//
// OrgResolve：JWT 认证后调用，从 DB 加载【实时】角色/部门路径并覆盖
// Context 中的 role（JWT 里的角色仅作登录凭证，权限以 DB 为准——
// 角色降级/调部门无需重签 token 即生效）。匿名请求直接放行。
//
// ReadonlyWriteGuard：readonly 角色写操作拦截（GET/HEAD/OPTIONS 放行）
// ============================================================

// OrgResolve 实时组织上下文解析（须挂在 TenantConsistency 之后）
func OrgResolve() gin.HandlerFunc {
	return func(c *gin.Context) {
		if db.DB == nil {
			c.Next()
			return
		}
		uidV, exists := c.Get("user_id")
		if !exists {
			c.Next() // 匿名/C端接口：无用户身份，跳过
			return
		}
		uid := toUint(uidV)
		if uid == 0 {
			c.Next()
			return
		}

		oc := service.LoadOrgContext(uid)
		if oc == nil {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": 403, "message": "账号不存在或已被删除", "data": nil,
			})
			return
		}
		if oc.Status != 1 {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": 403, "message": "账号已禁用", "data": nil,
			})
			return
		}

		// 租户一致性兜底：DB 实时租户必须与本次访问的生效租户一致
		// （super_admin 无租户归属，走 TenantConsistency 的显式指定逻辑，此处放行）
		effective := EffectiveTenantID(c)
		if oc.TenantID != 0 && effective != 0 && oc.TenantID != effective {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code": 403, "message": "账号所属租户与访问站点不一致", "data": nil,
			})
			return
		}

		// 用 DB 实时值覆盖 JWT 声明的角色/部门 → 数据范围随组织变化即时生效
		c.Set("role", oc.Role)
		c.Set("dept_id", oc.DeptID)
		c.Set("dept_path", oc.DeptPath)
		c.Next()
	}
}

// ReadonlyWriteGuard 只读角色写操作拦截（须挂在 OrgResolve 之后）
func ReadonlyWriteGuard() gin.HandlerFunc {
	return func(c *gin.Context) {
		if role, ok := c.Get("role"); ok {
			if r, _ := role.(string); r == "readonly" {
				switch c.Request.Method {
				case http.MethodGet, http.MethodHead, http.MethodOptions:
					c.Next()
					return
				}
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"code": 403, "message": "只读账号无写操作权限", "data": nil,
				})
				return
			}
		}
		c.Next()
	}
}
