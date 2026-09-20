// CurrentUser 安全取键单测（P2-7，2026-09-20 批三）：ctx 键缺失/错型不得再 panic 冒 500，
// 一律零值返回（下游角色闸对 "" fail-closed）。
package middleware

import (
	"testing"

	"github.com/gin-gonic/gin"
)

// TestCurrentUserSafeExtraction 覆盖三态：正常键全类型命中、缺键、错型。
func TestCurrentUserSafeExtraction(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// 正常态（JWTAuth 注入形态：user_id 为 uint）
	c, _ := gin.CreateTestContext(nil)
	c.Set("user_id", uint(7))
	c.Set("username", "admin")
	c.Set("role", "tenant_admin")
	uid, uname, role := CurrentUser(c)
	if uid != 7 || uname != "admin" || role != "tenant_admin" {
		t.Fatalf("正常键应完整取回，实际 %v/%q/%q", uid, uname, role)
	}

	// 全缺键：零值不 panic（旧实现 userID.(uint) 在此直接 panic）
	c2, _ := gin.CreateTestContext(nil)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("缺键不得 panic，实际 %v", r)
		}
	}()
	uid2, uname2, role2 := CurrentUser(c2)
	if uid2 != 0 || uname2 != "" || role2 != "" {
		t.Fatalf("缺键应回零值，实际 %v/%q/%q", uid2, uname2, role2)
	}

	// 错型（如历史代码把 user_id 塞成 int）：该键回零值，其余正常
	c3, _ := gin.CreateTestContext(nil)
	c3.Set("user_id", int(9))
	c3.Set("role", "sales")
	uid3, _, role3 := CurrentUser(c3)
	if uid3 != 0 {
		t.Fatalf("错型 user_id 必须判缺（不得断言成功），实际 %v", uid3)
	}
	if role3 != "sales" {
		t.Fatalf("其余键不受错型影响，实际 %q", role3)
	}
}
