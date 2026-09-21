// ExpireCheck 到期摘除/分档提醒回归测试（商业缺口批 2026-09-16）。
// 覆盖：① active+已过期 → 摘除为 expired 且幂等；② 剩余恰好 7 天 → 写审计档位，重跑不重复；
// ③ 到期邮件触达为旁路（SMTP 未配置降级日志），只验证查询不炸主流程。
package billing

import (
	"fmt"
	"os"
	"testing"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"
	"ai-scrm/internal/testutil"
)

// TestExpireCheckLapseAndRemind 覆盖套餐到期检查：过期租户置为失效并发出提醒，SMTP 未配置时静默降级不报错。
func TestExpireCheckLapseAndRemind(t *testing.T) {
	testutil.SetupTestDB(t)
	tid := testutil.CreateTenant(t)
	defer testutil.CleanupTenant(t, tid)

	// 给租户挂一个带邮箱的 tenant_admin（到期邮件按此查询；SMTP 未配置应静默降级不报错）
	tidCopy := tid
	u := model.User{TenantID: &tidCopy, Username: fmt.Sprintf("expadmin_%d_%d", time.Now().UnixNano()%1e9, os.Getpid()),
		PasswordHash: "x", Role: model.RoleTenantAdmin, Status: 1, Email: "expadmin@example.com"}
	if err := db.DB.Create(&u).Error; err != nil {
		t.Fatalf("建管理员: %v", err)
	}
	// 造一个已过期租户 + 一个剩 7 天租户
	now := time.Now()
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Update("expired_at", now.Add(-2*time.Hour))
	soon := testutil.CreateTenantCode(t, "exp_soon")
	db.DB.Model(&model.Tenant{}).Where("id = ?", soon).Update("expired_at", now.Add(7*24*time.Hour+30*time.Minute))

	affected := ExpireCheck()
	if affected < 1 {
		t.Fatalf("应至少摘除 1 个过期租户，实际 affected=%d", affected)
	}
	var st string
	db.DB.Model(&model.Tenant{}).Where("id = ?", tid).Pluck("status", &st)
	if st != "expired" {
		t.Fatalf("过期租户应摘除为 expired，实际 %s", st)
	}
	// 临期租户不被摘除，但写入 7d 提醒档位审计
	db.DB.Model(&model.Tenant{}).Where("id = ?", soon).Pluck("status", &st)
	if st != "active" {
		t.Fatalf("剩7天租户应保持 active，实际 %s", st)
	}
	var sent int64
	db.DB.Model(&model.TenantAuditLog{}).Where("tenant_id = ? AND action = 'expire_remind_7d'", soon).Count(&sent)
	if sent != 1 {
		t.Fatalf("7d 档提醒审计应恰好 1 条，实际 %d", sent)
	}
	// 幂等：立即重跑不再重复写审计、不报错（邮件旁路自愈）
	ExpireCheck()
	db.DB.Model(&model.TenantAuditLog{}).Where("tenant_id = ? AND action = 'expire_remind_7d'", soon).Count(&sent)
	if sent != 1 {
		t.Fatalf("重跑后 7d 审计仍应 1 条，实际 %d", sent)
	}
}
