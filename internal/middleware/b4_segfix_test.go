// P0-6/P2-16 复核批（2026-09-15）回归：B4 吊销核对的 nil-DB 护栏 + 平台路径白名单段边界。
package middleware

import "testing"

// TestTokenRevokedNilDBNoPanic P0-6：TokenRevoked 在 DB 未初始化（单测/启动早期）时必须
// 安全返回 false——旧实现裸调 db.DB 会 nil panic，把"降级匿名/401"炸成 500。
func TestTokenRevokedNilDBNoPanic(t *testing.T) {
	// 本包测试均不初始化 DB（无 SetupTestDB 调用），db.DB 恒 nil
	if TokenRevoked(7, 0) {
		t.Error("DB 未初始化应视为无从核对（放行身份注入，由数据路径自身兜底）")
	}
	if TokenRevoked(0, 3) {
		t.Error("uid=0（平台系统层）应恒不吊销")
	}
}

// TestIsPlatformSuperPathSegmentBoundary P2-16：白名单从裸前缀收紧为"段边界"匹配，
// 同前缀撞名路径（/api/v1/superx、/api/v1/authorized、/api/v1/admin/config-backup）
// 不得继承豁免——否则未来新增租户作用域路由一旦撞上前缀，超管无 X-Tenant-ID 即绕过隔离。
func TestIsPlatformSuperPathSegmentBoundary(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api/v1/super/packs/stats", true},
		{"/api/v1/super", true},
		{"/api/v1/auth/me", true},
		{"/api/v1/admin/config", true},
		{"/api/v1/admin/config?category=billing", true}, // 查询串先剥离再判段
		{"/api/v1/admin/config/rollback", false},       // 租户覆盖层操作显式排除
		{"/api/v1/superx/anything", false},             // 撞名前缀必须不豁免
		{"/api/v1/authorized_keys", false},
		{"/api/v1/admin/config-backup", false},
		{"/api/v1/org/departments", false}, // 租户作用域路径照旧要显式租户头
	}
	for _, c := range cases {
		if got := isPlatformSuperPath(c.path); got != c.want {
			t.Errorf("isPlatformSuperPath(%q)=%v 期望 %v", c.path, got, c.want)
		}
	}
}
