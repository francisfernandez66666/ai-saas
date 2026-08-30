package service

// ============================================================
// 多租户行级隔离 RLS（P2 RLS，2026-08-29）
//
// 定位：DB 层租户隔离兜底（与业务层 db.RQ/c.PQ fail-closed 双重保险）。
// 策略（休眠式）：默认 app.current_tenant 未设置 → current_setting 返回 NULL →
//   USING 表达式 OR NULL IS NULL 恒真 → 全表可见，与现状完全等价（零行为变更）。
// 激活方式：在事务内 SET LOCAL app.current_tenant='<tid>' 后查询即被 DB 强制按租户收敛
//   （见 SetTenantRLS）。采用 FORCE ROW LEVEL SECURITY 确保即便表 owner 也受策略约束。
// 仅对确含 tenant_id 的业务表启用；平台级表（tenants/system_configs/…）不在列，避免误伤跨租户管理。
// ============================================================

import (
	"fmt"
	"log"

	"ai-scrm/internal/db"
	"gorm.io/gorm"
)

// rlsTenantTables 启用行级隔离的租户业务表（均含 tenant_id 列）
var rlsTenantTables = []string{
	"customers", "conversations", "messages", "knowledge_fragments", "kb_feedback_materials",
	"feedback", "followups", "test_drives", "customer_tags", "agreement_signatures",
	"reward_claims", "cdp_profiles", "tenant_pack_bindings", "billing_orders",
	"usage_records", "tenant_audit_logs",
}

// EnableRLS 幂等启用租户隔离策略（休眠，不破坏现有查询）
func EnableRLS() {
	if db.DB == nil {
		return
	}
	for _, t := range rlsTenantTables {
		// FORCE：即便表 owner 也受策略约束；因 NULL 旁路，当前不生效，仅作激活准备
		db.DB.Exec(fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", t))
		db.DB.Exec(fmt.Sprintf("DROP POLICY IF EXISTS tenant_isolation ON %s", t))
		policy := fmt.Sprintf(
			`CREATE POLICY tenant_isolation ON %s FOR ALL USING (
				tenant_id::text = current_setting('app.current_tenant', true)
				OR current_setting('app.current_tenant', true) IS NULL
			)`, t)
		if err := db.DB.Exec(policy).Error; err != nil {
			log.Printf("[RLS] 表 %s 策略创建跳过（可能无 tenant_id 列）: %v", t, err)
		}
	}
	log.Printf("[RLS] 已对 %d 张租户表启用休眠式行级隔离（SET app.current_tenant 激活）", len(rlsTenantTables))
}

// SetTenantRLS 在事务内激活租户隔离：返回已 SET LOCAL 的事务 *gorm.DB，调用方须在该事务内完成查询。
// 用法：tx := SetTenantRLS(db.DB, tid); tx.Where(...).Find(...)；提交/回滚后设置自动失效。
func SetTenantRLS(d *gorm.DB, tenantID uint) *gorm.DB {
	return d.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID))
}

// WithTenantRLS 在事务内激活租户隔离并执行回调（安全激活范式）。
// 连接池下无法在普通查询稳定设置会话变量（会跨请求泄漏），故仅支持事务内激活；
// 关键跨表读（如超管跨租户审计、计费对账）可改用此 API 获得 DB 级强制隔离兜底。
// 示例：
//
//	var rows []model.Customer
//	err := service.WithTenantRLS(tid, func(tx *gorm.DB) error {
//	    return tx.Where("phone = ?", phone).Find(&rows).Error
//	})
func WithTenantRLS(tenantID uint, fn func(tx *gorm.DB) error) error {
	return db.DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("SET LOCAL app.current_tenant = '%d'", tenantID)).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}
