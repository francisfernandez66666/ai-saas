// B4 修复(2026-09-14)：JWT 吊销支撑——凭据类变更后递增用户 token_version，
// 使携带旧版本号的未过期 token 即时失效（MustChangePasswordGuard 每请求核对）。
package api

import (
	"ai-scrm/internal/db"

	"gorm.io/gorm"
)

// bumpUserTokenVersionByID 按用户 ID 递增 token_version。
// 传入 tx 时在调用方事务内执行（与改密同事务，防"密码已改版本未升"窗口）。
func bumpUserTokenVersionByID(uid uint, tx ...*gorm.DB) error {
	d := db.DB
	if len(tx) > 0 && tx[0] != nil {
		d = tx[0]
	}
	return d.Exec("UPDATE tenant_users SET token_version = COALESCE(token_version, 0) + 1 WHERE id = ?", uid).Error
}

// bumpUserTokenVersionByUsername 按用户名递增（重置密码路径无 user_id 时）。
func bumpUserTokenVersionByUsername(username string) error {
	return db.DB.Exec("UPDATE tenant_users SET token_version = COALESCE(token_version, 0) + 1 WHERE username = ?", username).Error
}
