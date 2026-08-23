package service

import (
	"fmt"
	"log"
	"time"

	"ai-scrm/internal/db"
	"ai-scrm/internal/model"

	"gorm.io/gorm"
)

// ============================================================
// 商业包服务（商业化第一批 M2，2026-08-23）
//
// 三类包的发放语义（对齐实施文档 §二）：
//   free       注册试用包：max_ai_calls_monthly += 包含量（叠加，不覆盖）
//   paid       包月包：expired_at 顺延 duration_days、max_ai_calls_monthly = 包含量
//   increment  AI增量包：ai_call_balance += 包含量（买断资产，月度重置不触碰）
//
// 扣减优先级在 usage_service.CheckAIQuota 实现：
//   月配额未超 → 直接用；已超且增量余额>0 → 原子扣余额；都没有 → 降级规则话术
// ============================================================

// GrantPackage 按包类型发放权益到租户
// tx 传 nil 走全局 DB；订单确认链路传事务句柄保证"改单+发放"原子
func GrantPackage(tx *gorm.DB, tenantID uint, pkg *model.Package) error {
	if tx == nil {
		tx = db.DB
	}
	if tenantID == 0 || pkg == nil {
		return fmt.Errorf("发放参数不完整 tenant=%d pkg=%v", tenantID, pkg)
	}

	switch pkg.PType {
	case model.PackageTypeFree:
		// 试用包：月配额叠加（注册联动发 trial_ai_calls 次）
		res := tx.Model(&model.Tenant{}).Where("id = ?", tenantID).
			Update("max_ai_calls_monthly", gorm.Expr("COALESCE(max_ai_calls_monthly,0)+?", pkg.AICalls))
		if res.Error != nil {
			return res.Error
		}
		log.Printf("[Package] 试用包已发放 tenant=%d pkg=%s +%d次", tenantID, pkg.Code, pkg.AICalls)
		return nil

	case model.PackageTypePaid:
		// 包月包：expired_at 顺延 + 月配额设定为包含量
		var t model.Tenant
		if err := tx.Select("id, expired_at").Where("id = ?", tenantID).First(&t).Error; err != nil {
			return err
		}
		base := time.Now()
		// 未到期续订：从当前到期日顺延；已到期/首订：从现在起算
		if t.ExpiredAt != nil && t.ExpiredAt.After(base) {
			base = *t.ExpiredAt
		}
		newExpiry := base.AddDate(0, 0, pkg.DurationDays)
		now := time.Now()
		res := tx.Model(&model.Tenant{}).Where("id = ?", tenantID).Updates(map[string]interface{}{
			"max_ai_calls_monthly": pkg.AICalls,
			"expired_at":           newExpiry,
			"status":               "active",
			"subscribed_at":        now,
		})
		if res.Error != nil {
			return res.Error
		}
		log.Printf("[Package] 包月包已生效 tenant=%d pkg=%s 月配额=%d次 到期=%s",
			tenantID, pkg.Code, pkg.AICalls, newExpiry.Format("2006-01-02"))
		return nil

	case model.PackageTypeIncrement:
		// 增量包：买断余额累加（月度重置任务只清 used_ai_calls，不碰 ai_call_balance）
		res := tx.Model(&model.Tenant{}).Where("id = ?", tenantID).
			Update("ai_call_balance", gorm.Expr("COALESCE(ai_call_balance,0)+?", pkg.AICalls))
		if res.Error != nil {
			return res.Error
		}
		log.Printf("[Package] 增量包已入账 tenant=%d pkg=%s +%d次(余额制)", tenantID, pkg.Code, pkg.AICalls)
		return nil

	default:
		return fmt.Errorf("未知包类型: %s", pkg.PType)
	}
}

// DeductBalance 原子扣减增量余额：UPDATE ... WHERE balance>0 天然防并发超扣
// 返回 false = 无可用余额
func DeductBalance(tenantID uint) bool {
	res := db.DB.Model(&model.Tenant{}).
		Where("id = ? AND COALESCE(ai_call_balance,0) > 0", tenantID).
		UpdateColumn("ai_call_balance", gorm.Expr("ai_call_balance - 1"))
	if res.Error != nil {
		log.Printf("[Package] 增量余额扣减失败 tenant=%d: %v", tenantID, res.Error)
		return false
	}
	return res.RowsAffected > 0
}

// GetTenantQuotaView 读租户配额三要素（月已用/月上限/增量余额）
func GetTenantQuotaView(tenantID uint) (used, maxMonthly, balance int, err error) {
	var row struct {
		Used    int
		Max     int
		Balance int
	}
	err = db.DB.Table("tenants").
		Select(`COALESCE(used_ai_calls,0) as used,
			COALESCE(max_ai_calls_monthly,0) as max,
			COALESCE(ai_call_balance,0) as balance`).
		Where("id = ?", tenantID).Take(&row).Error
	return row.Used, row.Max, row.Balance, err
}

// ExpireCheck 到期巡检：到期提醒（提前3天推企微群）+ 过期摘除（active→expired）
// 由 main 每小时 ticker 调用（Redis 选主，与用量重置同节奏）；幂等可重跑
func ExpireCheck() int {
	affected := 0

	// 1. 过期摘除：expired_at 已过且仍是 active → expired
	// （TenantResolver 对 expired 租户写操作返回402，读放行=宽限期语义）
	res := db.DB.Model(&model.Tenant{}).
		Where("status = 'active' AND expired_at IS NOT NULL AND expired_at < NOW()").
		Update("status", "expired")
	if res.Error != nil {
		log.Printf("[Package] 到期摘除失败: %v", res.Error)
	} else if res.RowsAffected > 0 {
		affected += int(res.RowsAffected)
		log.Printf("[Package] 到期摘除：%d 个租户 active→expired", res.RowsAffected)
	}

	// 2. 到期提醒分档（M4，借鉴翻译助手三期§3.2）：7天档+3天档各自当日去重，
	// 避免一次性3天档提醒过早或遗漏；标记复用审计表 action 区分
	for _, b := range []struct {
		days   int
		action string
	}{
		{7, "expire_remind_7d"},
		{3, "expire_remind_3d"},
	} {
		var expiring []model.Tenant
		db.DB.Where("status IN ? AND expired_at IS NOT NULL AND expired_at > NOW()",
			[]string{"active", "trial"}).Find(&expiring)
		for _, t := range expiring {
			daysLeft := int(time.Until(*t.ExpiredAt).Hours() / 24)
			if daysLeft != b.days { // 每档只在剩余恰好 N 天当天提醒一次（幂等由审计去重保证）
				continue
			}
			var sent int64
			db.DB.Model(&model.TenantAuditLog{}).
				Where("tenant_id = ? AND action = ? AND created_at >= CURRENT_DATE", t.ID, b.action).
				Count(&sent)
			if sent > 0 {
				continue
			}
			NotifyGroup(fmt.Sprintf("【到期提醒】租户「%s」(%s) 将于 %s 到期（剩%d天），请联系续费",
				t.Name, t.Code, t.ExpiredAt.Format("2006-01-02"), daysLeft))
			db.DB.Create(&model.TenantAuditLog{
				TenantID: t.ID, Action: b.action, Resource: fmt.Sprintf("tenant:%d", t.ID),
				Detail: fmt.Sprintf(`{"expired_at":"%s","days_left":%d}`, t.ExpiredAt.Format(time.RFC3339), daysLeft),
			})
			affected++
		}
	}
	return affected
}
